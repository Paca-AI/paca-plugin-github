package main

import (
	"context"
	"errors"
	"fmt"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// ─── DTOs ────────────────────────────────────────────────────────────────────

type taskBranchResponse struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	RepoID     string `json:"repo_id"`
	BranchName string `json:"branch_name"`
	CreatedAt  string `json:"created_at"`
}

type createBranchResponse struct {
	BranchName string `json:"branch_name"`
}

// ─── POST /tasks/:taskId/github/branches ──────────────────────────────────────

func (p *githubPlugin) createBranch(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	taskID := req.PathParam("taskId")

	type bodyT struct {
		RepoID       string `json:"repo_id"`
		BranchName   string `json:"branch_name"`
		SourceBranch string `json:"source_branch"`
	}
	b, err := plugin.JSONBody[bodyT](req)
	if err != nil || b.RepoID == "" || b.BranchName == "" {
		apiError(res, 400, "BAD_REQUEST", "repo_id and branch_name are required")
		return
	}

	token, err := p.decryptToken(projectID)
	if err != nil {
		writeAppError(res, err)
		return
	}

	// Get repository details.
	repoResult, rErr := p.db.Query(
		`SELECT owner, repo_name, default_branch FROM github_repositories WHERE id = $1 AND project_id = $2`,
		b.RepoID, projectID,
	)
	if rErr != nil {
		apiError(res, 500, "INTERNAL_ERROR", rErr.Error())
		return
	}
	if len(repoResult.Rows) == 0 {
		apiError(res, 404, "GITHUB_REPOSITORY_NOT_FOUND", "Repository not found")
		return
	}
	rSc := newRowScanner(repoResult.Columns, repoResult.Rows[0])
	owner := rSc.str("owner")
	repoName := rSc.str("repo_name")
	defaultBranch := rSc.str("default_branch")

	sourceBranch := b.SourceBranch
	if sourceBranch == "" {
		sourceBranch = defaultBranch
	}

	ghc := newGHClient(token)
	if err := ghc.createBranch(context.Background(), owner, repoName, b.BranchName, sourceBranch); err != nil {
		var apiErr *ghAPIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 403 {
				apiError(res, 403, "GITHUB_TOKEN_INSUFFICIENT_PERMISSIONS", "Token does not have permission to create branches")
				return
			}
			apiError(res, 400, "BAD_REQUEST", fmt.Sprintf("GitHub API error: %s", apiErr.Message))
			return
		}
		apiError(res, 502, "INTERNAL_ERROR", fmt.Sprintf("failed to create branch: %s", err))
		return
	}

	// Link the branch to the task.
	now := nowStr()
	rowsAffected, dbErr := p.db.Exec(`
		INSERT INTO github_task_branches (task_id, repo_id, branch_name, created_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (task_id, repo_id, branch_name) DO NOTHING
	`, taskID, b.RepoID, b.BranchName, now)
	if dbErr != nil {
		p.log.Error("failed to link branch to task: " + dbErr.Error())
	} else if rowsAffected == 0 {
		apiError(res, 409, "GITHUB_BRANCH_ALREADY_LINKED", "Branch is already linked to this task")
		return
	}

	plugin.EmitEvent("github.branch_linked", map[string]any{
		"project_id":  projectID,
		"task_id":     taskID,
		"repo_id":     b.RepoID,
		"branch_name": b.BranchName,
	})

	created(res, createBranchResponse{BranchName: b.BranchName})
}

// ─── POST /tasks/:taskId/branches/link ────────────────────────────────────────

func (p *githubPlugin) linkBranchToTask(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	taskID := req.PathParam("taskId")

	type bodyT struct {
		RepoID     string `json:"repo_id"`
		BranchName string `json:"branch_name"`
	}
	b, err := plugin.JSONBody[bodyT](req)
	if err != nil || b.RepoID == "" || b.BranchName == "" {
		apiError(res, 400, "BAD_REQUEST", "repo_id and branch_name are required")
		return
	}

	token, err := p.decryptToken(projectID)
	if err != nil {
		writeAppError(res, err)
		return
	}

	repoResult, rErr := p.db.Query(
		`SELECT owner, repo_name FROM github_repositories WHERE id = $1 AND project_id = $2`,
		b.RepoID, projectID,
	)
	if rErr != nil {
		apiError(res, 500, "INTERNAL_ERROR", rErr.Error())
		return
	}
	if len(repoResult.Rows) == 0 {
		apiError(res, 404, "GITHUB_REPOSITORY_NOT_FOUND", "Repository not found")
		return
	}
	rSc := newRowScanner(repoResult.Columns, repoResult.Rows[0])
	owner := rSc.str("owner")
	repoName := rSc.str("repo_name")

	ghc := newGHClient(token)
	if err := ghc.branchExists(context.Background(), owner, repoName, b.BranchName); err != nil {
		var apiErr *ghAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			apiError(res, 404, "GITHUB_BRANCH_NOT_FOUND",
				fmt.Sprintf("Branch %q not found in %s/%s", b.BranchName, owner, repoName))
			return
		}
		apiError(res, 502, "INTERNAL_ERROR", fmt.Sprintf("failed to verify branch: %s", err))
		return
	}

	now := nowStr()
	inserted, dbErr := p.db.Query(`
		INSERT INTO github_task_branches (task_id, repo_id, branch_name, created_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (task_id, repo_id, branch_name) DO NOTHING
		RETURNING id, task_id, repo_id, branch_name, created_at
	`, taskID, b.RepoID, b.BranchName, now)
	if dbErr != nil {
		apiError(res, 500, "INTERNAL_ERROR", dbErr.Error())
		return
	}
	if len(inserted.Rows) == 0 {
		apiError(res, 409, "GITHUB_BRANCH_ALREADY_LINKED", "Branch is already linked to this task")
		return
	}

	plugin.EmitEvent("github.branch_linked", map[string]any{
		"project_id":  projectID,
		"task_id":     taskID,
		"repo_id":     b.RepoID,
		"branch_name": b.BranchName,
	})

	sc := newRowScanner(inserted.Columns, inserted.Rows[0])
	created(res, taskBranchResponse{
		ID:         sc.str("id"),
		TaskID:     sc.str("task_id"),
		RepoID:     sc.str("repo_id"),
		BranchName: sc.str("branch_name"),
		CreatedAt:  sc.str("created_at"),
	})
}

// ─── GET /tasks/:taskId/github/branches ───────────────────────────────────────

func (p *githubPlugin) listTaskBranches(req *plugin.Request, res *plugin.Response) {
	taskID := req.PathParam("taskId")

	result, err := p.db.Query(`
		SELECT id, task_id, repo_id, branch_name, created_at
		FROM github_task_branches WHERE task_id = $1 ORDER BY created_at ASC
	`, taskID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}

	items := make([]taskBranchResponse, 0, len(result.Rows))
	for _, row := range result.Rows {
		sc := newRowScanner(result.Columns, row)
		items = append(items, taskBranchResponse{
			ID:         sc.str("id"),
			TaskID:     sc.str("task_id"),
			RepoID:     sc.str("repo_id"),
			BranchName: sc.str("branch_name"),
			CreatedAt:  sc.str("created_at"),
		})
	}
	ok(res, items)
}
