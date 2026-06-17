package main

import (
	"context"
	"errors"
	"fmt"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// ─── DTOs ────────────────────────────────────────────────────────────────────

type pullRequestResponse struct {
	ID         string  `json:"id"`
	ProjectID  string  `json:"project_id"`
	RepoID     string  `json:"repo_id"`
	PRNumber   int     `json:"pr_number"`
	GitHubPRID int64   `json:"github_pr_id"`
	Title      string  `json:"title"`
	State      string  `json:"state"`
	HTMLURL    string  `json:"html_url"`
	HeadBranch string  `json:"head_branch"`
	BaseBranch string  `json:"base_branch"`
	Author     string  `json:"author"`
	MergedAt   *string `json:"merged_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// ─── GET /tasks/:taskId/github/pull-requests ──────────────────────────────────

func (p *githubPlugin) listTaskPRs(req *plugin.Request, res *plugin.Response) {
	taskID := req.PathParam("taskId")

	result, err := p.db.Query(`
		SELECT pr.id, pr.project_id, pr.repo_id, pr.pr_number, pr.github_pr_id,
		       pr.title, pr.state, pr.html_url, pr.head_branch, pr.base_branch,
		       pr.author, pr.merged_at, pr.created_at, pr.updated_at
		FROM github_pull_requests pr
		JOIN github_task_pr_links l ON l.pull_request_id = pr.id
		WHERE l.task_id = $1
		ORDER BY l.created_at ASC
	`, taskID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}

	items := make([]pullRequestResponse, 0, len(result.Rows))
	for _, row := range result.Rows {
		sc := newRowScanner(result.Columns, row)
		pr := pullRequestResponse{
			ID:         sc.str("id"),
			ProjectID:  sc.str("project_id"),
			RepoID:     sc.str("repo_id"),
			PRNumber:   sc.intVal("pr_number"),
			GitHubPRID: sc.int64Val("github_pr_id"),
			Title:      sc.str("title"),
			State:      sc.str("state"),
			HTMLURL:    sc.str("html_url"),
			HeadBranch: sc.str("head_branch"),
			BaseBranch: sc.str("base_branch"),
			Author:     sc.str("author"),
			MergedAt:   sc.strPtr("merged_at"),
			CreatedAt:  sc.str("created_at"),
			UpdatedAt:  sc.str("updated_at"),
		}
		items = append(items, pr)
	}
	ok(res, items)
}

// ─── POST /tasks/:taskId/github/pull-requests/link ───────────────────────────

func (p *githubPlugin) linkPRToTask(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	taskID := req.PathParam("taskId")

	type bodyT struct {
		RepoID   string `json:"repo_id"`
		PRNumber int    `json:"pr_number"`
	}
	b, err := plugin.JSONBody[bodyT](req)
	if err != nil || b.RepoID == "" || b.PRNumber == 0 {
		apiError(res, 400, "BAD_REQUEST", "repo_id and pr_number are required")
		return
	}

	token, err := p.decryptToken(projectID)
	if err != nil {
		writeAppError(res, err)
		return
	}

	// Get repository details.
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
	ghPR, err := ghc.getPullRequest(context.Background(), owner, repoName, b.PRNumber)
	if err != nil {
		var apiErr *ghAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				apiError(res, 404, "GITHUB_PR_NOT_FOUND", fmt.Sprintf("PR #%d not found in %s/%s", b.PRNumber, owner, repoName))
				return
			case 401, 403:
				apiError(res, 403, "GITHUB_TOKEN_INSUFFICIENT_PERMISSIONS", "Token does not have permission to read pull requests")
				return
			}
		}
		apiError(res, 502, "INTERNAL_ERROR", fmt.Sprintf("failed to fetch pull request: %s", err))
		return
	}

	state := ghPR.State
	if ghPR.Merged {
		state = "merged"
	}

	now := nowStr()

	var mergedAtStr *string
	if ghPR.MergedAt != nil {
		s := ghPR.MergedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		mergedAtStr = &s
	}

	// Upsert the PR cache; let PostgreSQL generate id on insert, RETURNING gives us id+created_at.
	upserted, err := p.db.Query(`
		INSERT INTO github_pull_requests
			(project_id, repo_id, pr_number, github_pr_id, title, state, html_url, head_branch, base_branch, author, merged_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (repo_id, pr_number) DO UPDATE SET
			title=$5, state=$6, html_url=$7, head_branch=$8, base_branch=$9,
			author=$10, merged_at=$11, updated_at=$13
		RETURNING id, created_at
	`, projectID, b.RepoID, b.PRNumber, ghPR.ID, ghPR.Title, state,
		ghPR.HTMLURL, ghPR.Head.Ref, ghPR.Base.Ref, ghPR.User.Login, mergedAtStr, now, now)
	if err != nil || len(upserted.Rows) == 0 {
		if err != nil {
			apiError(res, 500, "INTERNAL_ERROR", err.Error())
		} else {
			apiError(res, 500, "INTERNAL_ERROR", "upsert returned no rows")
		}
		return
	}
	prSc := newRowScanner(upserted.Columns, upserted.Rows[0])
	prID := prSc.str("id")
	prCreatedAt := prSc.str("created_at")

	// Link the PR to the task.
	rowsAffected, lErr := p.db.Exec(`
		INSERT INTO github_task_pr_links (task_id, pull_request_id, created_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (task_id, pull_request_id) DO NOTHING
	`, taskID, prID, now)
	if lErr != nil {
		apiError(res, 500, "INTERNAL_ERROR", lErr.Error())
		return
	}
	if rowsAffected == 0 {
		apiError(res, 409, "GITHUB_PR_ALREADY_LINKED", "Pull request is already linked to this task")
		return
	}

	plugin.EmitEvent("github.pr_linked", map[string]any{
		"project_id": projectID,
		"task_id":    taskID,
		"repo_id":    b.RepoID,
		"pr_number":  b.PRNumber,
	})

	// Return the PR details.
	ok(res, pullRequestResponse{
		ID:         prID,
		ProjectID:  projectID,
		RepoID:     b.RepoID,
		PRNumber:   b.PRNumber,
		GitHubPRID: ghPR.ID,
		Title:      ghPR.Title,
		State:      state,
		HTMLURL:    ghPR.HTMLURL,
		HeadBranch: ghPR.Head.Ref,
		BaseBranch: ghPR.Base.Ref,
		Author:     ghPR.User.Login,
		MergedAt:   mergedAtStr,
		CreatedAt:  prCreatedAt,
		UpdatedAt:  now,
	})
}

// ─── POST /tasks/:taskId/github/pull-requests ─────────────────────────────────

func (p *githubPlugin) createPullRequest(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	taskID := req.PathParam("taskId")

	type bodyT struct {
		RepoID     string `json:"repo_id"`
		Title      string `json:"title"`
		HeadBranch string `json:"head_branch"`
		BaseBranch string `json:"base_branch"`
		Body       string `json:"body"`
	}
	b, err := plugin.JSONBody[bodyT](req)
	if err != nil || b.RepoID == "" || b.Title == "" || b.HeadBranch == "" || b.BaseBranch == "" {
		apiError(res, 400, "BAD_REQUEST", "repo_id, title, head_branch, and base_branch are required")
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
	ghPR, err := ghc.createPullRequest(context.Background(), owner, repoName, b.Title, b.HeadBranch, b.BaseBranch, b.Body)
	if err != nil {
		var apiErr *ghAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				apiError(res, 403, "GITHUB_TOKEN_INSUFFICIENT_PERMISSIONS", "Token does not have permission to create pull requests")
				return
			case 422:
				apiError(res, 422, "GITHUB_PR_VALIDATION_ERROR", fmt.Sprintf("GitHub validation error: %s", apiErr.Message))
				return
			}
		}
		apiError(res, 502, "INTERNAL_ERROR", fmt.Sprintf("failed to create pull request: %s", err))
		return
	}

	state := ghPR.State
	if ghPR.Merged {
		state = "merged"
	}

	now := nowStr()

	var mergedAtStr *string
	if ghPR.MergedAt != nil {
		s := ghPR.MergedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		mergedAtStr = &s
	}

	upserted, err := p.db.Query(`
		INSERT INTO github_pull_requests
			(project_id, repo_id, pr_number, github_pr_id, title, state, html_url, head_branch, base_branch, author, merged_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		ON CONFLICT (repo_id, pr_number) DO UPDATE SET
			title=$5, state=$6, html_url=$7, head_branch=$8, base_branch=$9,
			author=$10, merged_at=$11, updated_at=$12
		RETURNING id
	`, projectID, b.RepoID, ghPR.Number, ghPR.ID, ghPR.Title, state,
		ghPR.HTMLURL, ghPR.Head.Ref, ghPR.Base.Ref, ghPR.User.Login, mergedAtStr, now)
	if err != nil || len(upserted.Rows) == 0 {
		if err != nil {
			apiError(res, 500, "INTERNAL_ERROR", err.Error())
		} else {
			apiError(res, 500, "INTERNAL_ERROR", "upsert returned no rows")
		}
		return
	}
	prID := newRowScanner(upserted.Columns, upserted.Rows[0]).str("id")

	_, lErr := p.db.Exec(`
		INSERT INTO github_task_pr_links (task_id, pull_request_id, created_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (task_id, pull_request_id) DO NOTHING
	`, taskID, prID, now)
	if lErr != nil {
		p.log.Error("failed to link PR to task: " + lErr.Error())
	}

	plugin.EmitEvent("github.pr_created", map[string]any{
		"project_id": projectID,
		"task_id":    taskID,
		"repo_id":    b.RepoID,
		"pr_number":  ghPR.Number,
		"pr_url":     ghPR.HTMLURL,
	})

	created(res, pullRequestResponse{
		ID:         prID,
		ProjectID:  projectID,
		RepoID:     b.RepoID,
		PRNumber:   ghPR.Number,
		GitHubPRID: ghPR.ID,
		Title:      ghPR.Title,
		State:      state,
		HTMLURL:    ghPR.HTMLURL,
		HeadBranch: ghPR.Head.Ref,
		BaseBranch: ghPR.Base.Ref,
		Author:     ghPR.User.Login,
		MergedAt:   mergedAtStr,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// ─── DELETE /tasks/:taskId/github/pull-requests/:prId ────────────────────────

func (p *githubPlugin) unlinkPRFromTask(req *plugin.Request, res *plugin.Response) {
	taskID := req.PathParam("taskId")
	prID := req.PathParam("prId")

	rowsAffected, err := p.db.Exec(
		`DELETE FROM github_task_pr_links WHERE task_id = $1 AND pull_request_id = $2`,
		taskID, prID,
	)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if rowsAffected == 0 {
		apiError(res, 404, "GITHUB_PR_LINK_NOT_FOUND", "Pull request link not found")
		return
	}
	noContent(res)
}
