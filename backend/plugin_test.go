package main

import (
	"encoding/json"
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

const (
	testProjectID = "project-1"
	testTaskID    = "task-1"
)

func setupPlugin(t *testing.T) *plugintest.Context {
	t.Helper()
	tc := plugintest.NewContext(t)

	// Seed the shared tables referenced by FK.
	tc.DB.SeedRows("tasks", []string{"id", "project_id", "deleted_at"}, [][]any{
		{testTaskID, testProjectID, nil},
	})

	// Seed empty plugin tables so queries return empty result sets instead of errors.
	tc.DB.SeedRows("github_integrations",
		[]string{"id", "project_id", "access_token_enc", "created_at", "updated_at"},
		nil)
	tc.DB.SeedRows("github_repositories",
		[]string{"id", "project_id", "integration_id", "owner", "repo_name", "full_name",
			"webhook_id", "webhook_secret_enc", "default_branch", "created_at", "updated_at"},
		nil)
	tc.DB.SeedRows("github_pull_requests",
		[]string{"id", "project_id", "repo_id", "pr_number", "github_pr_id", "title",
			"state", "html_url", "head_branch", "base_branch", "author", "merged_at", "created_at", "updated_at"},
		nil)
	tc.DB.SeedRows("github_task_pr_links",
		[]string{"id", "task_id", "pull_request_id", "created_at"},
		nil)
	tc.DB.SeedRows("github_task_branches",
		[]string{"id", "task_id", "repo_id", "branch_name", "created_at"},
		nil)

	var p githubPlugin
	if err := p.Init(tc.PluginContext()); err != nil {
		t.Fatal("Init failed:", err)
	}
	return tc
}

func callerReq() plugintest.Request {
	return plugintest.Request{
		Caller: plugin.CallerIdentity{
			ProjectID:  testProjectID,
			CallerID:   "member-1",
			CallerRole: "PROJECT_MEMBER",
		},
		PathParams: map[string]string{},
	}
}

// ── Integration tests ─────────────────────────────────────────────────────────

func TestGetIntegration_NotConnected(t *testing.T) {
	tc := setupPlugin(t)
	res := tc.Call("GET", "/integration", callerReq())

	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, res.BodyString())
	}
	var env struct {
		Success bool                `json:"success"`
		Data    integrationResponse `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		t.Fatal(err)
	}
	if !env.Success {
		t.Fatal("expected success=true")
	}
	if env.Data.Connected {
		t.Fatal("expected Connected=false when no token is set")
	}
	if env.Data.ProjectID != testProjectID {
		t.Fatalf("expected project_id=%s, got %s", testProjectID, env.Data.ProjectID)
	}
}

// ── PR review/comment tests ────────────────────────────────────────────────────
//
// These cover the paths reachable without an outbound GitHub API call
// (validation and "PR not linked to this task"). The actual GitHub call
// (ghClient -> plugin.Fetch) always errors outside a WASM build — see
// plugin-sdk-go's native_backends.go — so the happy path for these three
// handlers, like the existing createPullRequest handler, isn't unit-testable
// here and is covered manually / by integration testing instead.

func reqWithPathParams(params map[string]string) plugintest.Request {
	r := callerReq()
	for k, v := range params {
		r.PathParams[k] = v
	}
	return r
}

func TestGetPullRequestDetails_NotLinkedToTask(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"})
	res := tc.Call("GET", "/tasks/:taskId/pull-requests/:prId", req)

	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestAddPullRequestComment_MissingBody(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"}).
		WithJSONBody(map[string]string{})
	res := tc.Call("POST", "/tasks/:taskId/pull-requests/:prId/comments", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestAddPullRequestComment_NotLinkedToTask(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"}).
		WithJSONBody(map[string]string{"body": "looks good"})
	res := tc.Call("POST", "/tasks/:taskId/pull-requests/:prId/comments", req)

	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestCreateReview_InvalidEvent(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"}).
		WithJSONBody(map[string]string{"event": "NOT_A_REAL_EVENT"})
	res := tc.Call("POST", "/tasks/:taskId/pull-requests/:prId/reviews", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestCreateReview_NotLinkedToTask(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"}).
		WithJSONBody(map[string]string{"event": "APPROVE"})
	res := tc.Call("POST", "/tasks/:taskId/pull-requests/:prId/reviews", req)

	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestGetPullRequestCIStatus_NotLinkedToTask(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID, "prId": "pr-1"})
	res := tc.Call("GET", "/tasks/:taskId/pull-requests/:prId/ci-status", req)

	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

// ── overallCIState ─────────────────────────────────────────────────────────────

func TestOverallCIState_NoChecks(t *testing.T) {
	if got := overallCIState(nil); got != "unknown" {
		t.Fatalf("expected unknown, got %s", got)
	}
}

func TestOverallCIState_AllSuccess(t *testing.T) {
	checks := []ciCheckResponse{
		{Status: "completed", Conclusion: "success"},
		{Status: "completed", Conclusion: "neutral"},
	}
	if got := overallCIState(checks); got != "success" {
		t.Fatalf("expected success, got %s", got)
	}
}

func TestOverallCIState_OneFailureWins(t *testing.T) {
	checks := []ciCheckResponse{
		{Status: "completed", Conclusion: "success"},
		{Status: "completed", Conclusion: "failure"},
	}
	if got := overallCIState(checks); got != "failure" {
		t.Fatalf("expected failure, got %s", got)
	}
}

func TestOverallCIState_StillRunningIsPending(t *testing.T) {
	checks := []ciCheckResponse{
		{Status: "completed", Conclusion: "success"},
		{Status: "in_progress", Conclusion: ""},
	}
	if got := overallCIState(checks); got != "pending" {
		t.Fatalf("expected pending, got %s", got)
	}
}

func TestOverallCIState_FailureBeatsPending(t *testing.T) {
	checks := []ciCheckResponse{
		{Status: "in_progress", Conclusion: ""},
		{Status: "completed", Conclusion: "failure"},
	}
	if got := overallCIState(checks); got != "failure" {
		t.Fatalf("expected failure, got %s", got)
	}
}
