// Package main implements the com.paca.github backend WASM plugin.
//
// It manages GitHub integration for projects: PAT storage, repository linking
// with automatic webhook creation, pull-request caching, branch creation, and
// webhook event processing.
package main

import (
	"encoding/json"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// nowStr returns the current UTC time as an RFC3339Nano string.
func nowStr() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// githubPlugin implements plugin.Plugin.
type githubPlugin struct {
	db  *plugin.DB
	log *plugin.Logger
	cfg *plugin.Config
}

// Init registers all routes and event handlers on the provided context.
func (p *githubPlugin) Init(ctx *plugin.Context) error {
	p.db = ctx.DB()
	p.log = ctx.Log()
	p.cfg = ctx.Config()

	// Event handlers
	ctx.On("task.deleted", p.handleTaskDeleted)
	ctx.On("project.deleted", p.handleProjectDeleted)

	// ── Integration (GitHub token / connection) ───────────────────────────────
	ctx.Route("GET", "/integration", p.getIntegration)
	ctx.Route("POST", "/integration/token", p.setToken)
	ctx.Route("DELETE", "/integration/token", p.deleteToken)
	ctx.Route("GET", "/integration/accessible-repos", p.listAccessibleRepos)

	// ── Repositories (repos linked to the project) ────────────────────────────
	ctx.Route("GET", "/repositories", p.listRepositories)
	ctx.Route("POST", "/repositories", p.linkRepository)
	ctx.Route("DELETE", "/repositories/:repoId", p.unlinkRepository)
	ctx.Route("GET", "/repositories/:repoId/clone-info", p.getRepoCloneInfo)

	// ── Task resources ────────────────────────────────────────────────────────
	ctx.Route("GET", "/tasks/:taskId/pull-requests", p.listTaskPRs)
	ctx.Route("POST", "/tasks/:taskId/pull-requests", p.createPullRequest)
	ctx.Route("POST", "/tasks/:taskId/pull-requests/link", p.linkPRToTask)
	ctx.Route("DELETE", "/tasks/:taskId/pull-requests/:prId", p.unlinkPRFromTask)
	ctx.Route("GET", "/tasks/:taskId/pull-requests/:prId", p.getPullRequestDetails)
	ctx.Route("GET", "/tasks/:taskId/pull-requests/:prId/ci-status", p.getPullRequestCIStatus)
	ctx.Route("POST", "/tasks/:taskId/pull-requests/:prId/comments", p.addPullRequestComment)
	ctx.Route("POST", "/tasks/:taskId/pull-requests/:prId/reviews", p.createReview)
	ctx.Route("GET", "/tasks/:taskId/branches", p.listTaskBranches)
	ctx.Route("POST", "/tasks/:taskId/branches", p.createBranch)

	// ── Webhook ───────────────────────────────────────────────────────────────
	ctx.Route("POST", "/webhook", p.receiveWebhook)

	return nil
}

// Shutdown is a no-op for this plugin.
func (p *githubPlugin) Shutdown() {}

// ─── envelope helpers ────────────────────────────────────────────────────────

type envelope struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
}

func ok(res *plugin.Response, data any) {
	res.JSON(200, envelope{Success: true, Data: data})
}

func created(res *plugin.Response, data any) {
	res.JSON(201, envelope{Success: true, Data: data})
}

func noContent(res *plugin.Response) {
	res.NoContent()
}

func apiError(res *plugin.Response, code int, errCode, message string) {
	res.JSON(code, map[string]any{
		"success":    false,
		"error":      message,
		"error_code": errCode,
	})
}

// ─── event handlers ──────────────────────────────────────────────────────────

// handleTaskDeleted cleans up branches and PR links when a task is deleted.
func (p *githubPlugin) handleTaskDeleted(evt *plugin.Event) {
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(evt.Payload, &payload); err != nil || payload.TaskID == "" {
		return
	}
	_, _ = p.db.Exec(`DELETE FROM github_task_branches WHERE task_id = $1`, payload.TaskID)
	_, _ = p.db.Exec(`DELETE FROM github_task_pr_links WHERE task_id = $1`, payload.TaskID)
}

// handleProjectDeleted cleans up the full integration when a project is deleted.
func (p *githubPlugin) handleProjectDeleted(evt *plugin.Event) {
	var payload struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(evt.Payload, &payload); err != nil || payload.ProjectID == "" {
		return
	}
	// Cascade via FK, but also be explicit.
	_, _ = p.db.Exec(`DELETE FROM github_integrations WHERE project_id = $1`, payload.ProjectID)
}
