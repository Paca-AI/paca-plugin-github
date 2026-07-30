package main

import (
	"context"
	"encoding/json"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// This file implements the automation-graph Condition and Action node
// types this plugin contributes, registered in Init via ctx.Condition and
// ctx.Action. Node types must match exactly what's declared in plugin.json
// under "automation" (see AutomationManifest in the core's
// domain/plugin/entity.go) — reverse-DNS namespaced under "com.paca.github".
//
// Both handlers resolve the calling project from req.Config.ProjectID
// (embedded in the node's config at graph-authoring time via the config
// form, same as any other plugin node config field) rather than from
// req.Task, since a task by itself doesn't carry which GitHub repository/PR
// it's linked to — that's resolved through github_task_pr_links the same
// way the HTTP handlers in pull_requests.go do it.

const (
	// automationConditionPRState checks the linked pull request's state
	// (open/closed/merged) against a configured expected value.
	automationConditionPRState = "com.paca.github.pr_state"

	// automationActionMergePR merges the linked pull request.
	automationActionMergePR = "com.paca.github.merge_pr"
	// automationActionCommentPR posts a comment on the linked pull request.
	automationActionCommentPR = "com.paca.github.comment_pr"
)

// registerAutomationNodes wires this plugin's Condition/Action handlers
// into ctx. Called once from Init.
func (p *githubPlugin) registerAutomationNodes(ctx *plugin.Context) {
	ctx.Condition(automationConditionPRState, p.conditionPRState)
	ctx.Action(automationActionMergePR, p.actionMergePR)
	ctx.Action(automationActionCommentPR, p.actionCommentPR)
}

// ─── shared: resolve the most recently linked PR for a task ──────────────────

// pluginLinkedPR is what resolveLinkedPRForAutomation returns: enough to
// call the GitHub API plus the plugin's own repo_id/pr row id for logging.
type pluginLinkedPR struct {
	Owner    string
	RepoName string
	PRNumber int
}

// resolveLinkedPRForAutomation finds the most recently linked PR for a
// task, the same many-PRs-per-task relationship listTaskPRs exposes over
// HTTP — automation nodes act on the newest link since that's virtually
// always the PR the automation graph author means ("the PR for this task").
func (p *githubPlugin) resolveLinkedPRForAutomation(projectID, taskID string) (*pluginLinkedPR, error) {
	result, err := p.db.Query(`
		SELECT r.owner, r.repo_name, pr.pr_number
		FROM github_pull_requests pr
		JOIN github_task_pr_links l ON l.pull_request_id = pr.id
		JOIN github_repositories r ON r.id = pr.repo_id
		WHERE l.task_id = $1 AND pr.project_id = $2
		ORDER BY l.created_at DESC
		LIMIT 1
	`, taskID, projectID)
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, &appError{code: "GITHUB_PR_LINK_NOT_FOUND", status: 404, msg: "No pull request linked to this task"}
	}
	sc := newRowScanner(result.Columns, result.Rows[0])
	return &pluginLinkedPR{
		Owner:    sc.str("owner"),
		RepoName: sc.str("repo_name"),
		PRNumber: sc.intVal("pr_number"),
	}, nil
}

// automationProjectID extracts the project_id every node config in this
// plugin's automation contributions requires — the automation graph itself
// is scoped to a project, but the plugin's own DB rows are keyed by
// project_id too, so each node config carries it explicitly rather than
// relying on cross-referencing the automation run.
func automationProjectID(config json.RawMessage) (string, error) {
	var v struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(config, &v); err != nil {
		return "", err
	}
	if v.ProjectID == "" {
		return "", &appError{code: "GITHUB_MISSING_PROJECT_ID", status: 400, msg: "config.project_id is required"}
	}
	return v.ProjectID, nil
}

// ─── Condition: com.paca.github.pr_state ─────────────────────────────────────

func (p *githubPlugin) conditionPRState(req *plugin.ConditionRequest) plugin.ConditionResult {
	var cfg struct {
		ProjectID     string `json:"project_id"`
		ExpectedState string `json:"expected_state"` // "open" | "closed" | "merged"
	}
	if err := json.Unmarshal(req.Config, &cfg); err != nil || cfg.ProjectID == "" || cfg.ExpectedState == "" {
		p.log.Error("github: pr_state condition: invalid config")
		return plugin.ConditionResult{Matched: false}
	}

	linked, err := p.resolveLinkedPRForAutomation(cfg.ProjectID, req.Task.ID)
	if err != nil {
		p.log.Info("github: pr_state condition: " + err.Error())
		return plugin.ConditionResult{Matched: false}
	}

	token, err := p.decryptToken(cfg.ProjectID)
	if err != nil {
		p.log.Error("github: pr_state condition: decrypt token: " + err.Error())
		return plugin.ConditionResult{Matched: false}
	}

	ghc := newGHClient(token)
	ghPR, err := ghc.getPullRequest(context.Background(), linked.Owner, linked.RepoName, linked.PRNumber)
	if err != nil {
		p.log.Error("github: pr_state condition: fetch PR: " + err.Error())
		return plugin.ConditionResult{Matched: false}
	}

	state := ghPR.State
	if ghPR.Merged {
		state = "merged"
	}
	return plugin.ConditionResult{Matched: state == cfg.ExpectedState}
}

// ─── Action: com.paca.github.merge_pr ─────────────────────────────────────────

func (p *githubPlugin) actionMergePR(req *plugin.ActionRequest) plugin.ActionResult {
	var cfg struct {
		ProjectID   string `json:"project_id"`
		MergeMethod string `json:"merge_method"` // "merge" | "squash" | "rebase"; defaults to "merge"
	}
	if err := json.Unmarshal(req.Config, &cfg); err != nil || cfg.ProjectID == "" {
		return plugin.ActionResult{Applied: false, Error: "invalid config: project_id is required"}
	}
	if cfg.MergeMethod == "" {
		cfg.MergeMethod = "merge"
	}

	linked, err := p.resolveLinkedPRForAutomation(cfg.ProjectID, req.Task.ID)
	if err != nil {
		return plugin.ActionResult{Applied: false, Error: err.Error()}
	}

	token, err := p.decryptToken(cfg.ProjectID)
	if err != nil {
		return plugin.ActionResult{Applied: false, Error: "decrypt token: " + err.Error()}
	}
	ghc := newGHClient(token)
	ctx := context.Background()

	// Idempotency: a plugin action can be retried by the automation
	// engine, so check current state first — merging an already-merged PR
	// is a no-op success, not an error, mirroring how built-in actions
	// treat "already at the desired state".
	ghPR, err := ghc.getPullRequest(ctx, linked.Owner, linked.RepoName, linked.PRNumber)
	if err != nil {
		return plugin.ActionResult{Applied: false, Error: "fetch PR: " + err.Error()}
	}
	if ghPR.Merged {
		return plugin.ActionResult{Applied: false}
	}

	if err := ghc.mergePullRequest(ctx, linked.Owner, linked.RepoName, linked.PRNumber, cfg.MergeMethod); err != nil {
		return plugin.ActionResult{Applied: false, Error: "merge PR: " + err.Error()}
	}
	return plugin.ActionResult{Applied: true}
}

// ─── Action: com.paca.github.comment_pr ───────────────────────────────────────

func (p *githubPlugin) actionCommentPR(req *plugin.ActionRequest) plugin.ActionResult {
	var cfg struct {
		ProjectID string `json:"project_id"`
		Body      string `json:"body"`
	}
	if err := json.Unmarshal(req.Config, &cfg); err != nil || cfg.ProjectID == "" || cfg.Body == "" {
		return plugin.ActionResult{Applied: false, Error: "invalid config: project_id and body are required"}
	}

	linked, err := p.resolveLinkedPRForAutomation(cfg.ProjectID, req.Task.ID)
	if err != nil {
		return plugin.ActionResult{Applied: false, Error: err.Error()}
	}

	token, err := p.decryptToken(cfg.ProjectID)
	if err != nil {
		return plugin.ActionResult{Applied: false, Error: "decrypt token: " + err.Error()}
	}
	ghc := newGHClient(token)

	if err := ghc.createIssueComment(context.Background(), linked.Owner, linked.RepoName, linked.PRNumber, cfg.Body); err != nil {
		return plugin.ActionResult{Applied: false, Error: "comment PR: " + err.Error()}
	}
	return plugin.ActionResult{Applied: true}
}
