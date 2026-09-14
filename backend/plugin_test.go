package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func TestLinkBranchToTask_MissingBody(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID}).
		WithJSONBody(map[string]string{})
	res := tc.Call("POST", "/tasks/:taskId/branches/link", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestLinkBranchToTask_MissingRepoID(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID}).
		WithJSONBody(map[string]string{"branch_name": "dev"})
	res := tc.Call("POST", "/tasks/:taskId/branches/link", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestLinkBranchToTask_MissingBranchName(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID}).
		WithJSONBody(map[string]string{"repo_id": "00000000-0000-0000-0000-000000000001"})
	res := tc.Call("POST", "/tasks/:taskId/branches/link", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestCreateBranch_MissingBranchName(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID}).
		WithJSONBody(map[string]string{"repo_id": "00000000-0000-0000-0000-000000000001"})
	res := tc.Call("POST", "/tasks/:taskId/branches", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestCreateBranch_MissingRepoID(t *testing.T) {
	tc := setupPlugin(t)
	req := reqWithPathParams(map[string]string{"taskId": testTaskID}).
		WithJSONBody(map[string]string{"branch_name": "dev"})
	res := tc.Call("POST", "/tasks/:taskId/branches", req)

	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, res.BodyString())
	}
}

// ── Cross-project task ownership guards ──────────────────────────────────────
//
// Every handler below previously trusted the URL's :taskId without
// verifying it belongs to the caller's own project (req.Caller.ProjectID) —
// only the request body's repo_id/pr_number were project-checked. A caller
// with tasks.read/write on their own project could substitute a foreign
// taskId to read or attach records to a completely different project's
// task. taskBelongsToProject now runs before any of that, so these all
// resolve to a 404 before ever reaching a GitHub API call or a DB write —
// which is also what keeps them unit-testable, since the outbound GitHub
// call itself always errors outside a real WASM build (see the comment
// above the PR review/comment tests).

const otherTaskID = "task-2" // seeded under a project the default caller is not a member of

func foreignTaskReq() plugintest.Request {
	return plugintest.Request{
		Caller: plugin.CallerIdentity{
			ProjectID:  testProjectID, // the default caller's own project
			CallerID:   "member-1",
			CallerRole: "PROJECT_MEMBER",
		},
		PathParams: map[string]string{"taskId": otherTaskID}, // but a foreign task
	}
}

// setupWithForeignTask re-seeds tasks with both the default task and a
// second task belonging to a different project.
func setupWithForeignTask(t *testing.T) *plugintest.Context {
	t.Helper()
	tc := setupPlugin(t)
	tc.DB.SeedRows("tasks", []string{"id", "project_id", "deleted_at"}, [][]any{
		{testTaskID, testProjectID, nil},
		{otherTaskID, "other-project", nil},
	})
	return tc
}

func TestListTaskPRs_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("GET", "/tasks/:taskId/pull-requests", foreignTaskReq())
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestListTaskBranches_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("GET", "/tasks/:taskId/branches", foreignTaskReq())
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

// TestListTaskBranches_ReturnsOwnProjectBranches is the happy-path
// counterpart to the cross-project test above — it exercises the handler's
// main query past taskBelongsToProject, which a rejection test alone
// wouldn't reach.
func TestListTaskBranches_ReturnsOwnProjectBranches(t *testing.T) {
	tc := setupPlugin(t)
	tc.DB.SeedRows("github_repositories",
		[]string{"id", "project_id", "integration_id", "owner", "repo_name", "full_name",
			"webhook_id", "webhook_secret_enc", "default_branch", "created_at", "updated_at"},
		[][]any{{"repo-1", testProjectID, "integration-1", "octocat", "hello-world", "octocat/hello-world",
			"wh-1", "enc-secret", "main", "now", "now"}})
	tc.DB.SeedRows("github_task_branches",
		[]string{"id", "task_id", "repo_id", "branch_name", "created_at"},
		[][]any{{"branch-1", testTaskID, "repo-1", "feature/x", "now"}})

	res := tc.Call("GET", "/tasks/:taskId/branches", reqWithPathParams(map[string]string{"taskId": testTaskID}))
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, res.BodyString())
	}
	var env struct {
		Data []taskBranchResponse `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 || env.Data[0].BranchName != "feature/x" {
		t.Fatalf("expected the seeded branch to be returned, got %+v", env.Data)
	}
}

// TestListTaskBranches_SkipsRepoFromAnotherProject mirrors
// TestListTaskPRs_SkipsLinkFromAnotherProject: github_task_branches has no
// project_id of its own, only repo_id, so a branch row pointing at a repo
// that belongs to a different project must be filtered out even when the
// task itself is the caller's own.
func TestListTaskBranches_SkipsRepoFromAnotherProject(t *testing.T) {
	tc := setupPlugin(t)
	tc.DB.SeedRows("github_repositories",
		[]string{"id", "project_id", "integration_id", "owner", "repo_name", "full_name",
			"webhook_id", "webhook_secret_enc", "default_branch", "created_at", "updated_at"},
		[][]any{{"repo-foreign", "other-project", "integration-1", "octocat", "other-repo", "octocat/other-repo",
			"wh-1", "enc-secret", "main", "now", "now"}})
	tc.DB.SeedRows("github_task_branches",
		[]string{"id", "task_id", "repo_id", "branch_name", "created_at"},
		[][]any{{"branch-1", testTaskID, "repo-foreign", "feature/x", "now"}})

	res := tc.Call("GET", "/tasks/:taskId/branches", reqWithPathParams(map[string]string{"taskId": testTaskID}))
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, res.BodyString())
	}
	var env struct {
		Data []taskBranchResponse `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 0 {
		t.Fatalf("expected the cross-project repo's branch to be filtered out, got %+v", env.Data)
	}
}

func TestCreateBranch_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("POST", "/tasks/:taskId/branches",
		foreignTaskReq().WithJSONBody(map[string]string{
			"repo_id": "00000000-0000-0000-0000-000000000001", "branch_name": "dev",
		}))
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestLinkBranchToTask_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("POST", "/tasks/:taskId/branches/link",
		foreignTaskReq().WithJSONBody(map[string]string{
			"repo_id": "00000000-0000-0000-0000-000000000001", "branch_name": "dev",
		}))
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestCreatePullRequest_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("POST", "/tasks/:taskId/pull-requests",
		foreignTaskReq().WithJSONBody(map[string]string{
			"repo_id": "00000000-0000-0000-0000-000000000001", "title": "x", "head_branch": "a", "base_branch": "main",
		}))
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestLinkPRToTask_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	res := tc.Call("POST", "/tasks/:taskId/pull-requests/link",
		foreignTaskReq().WithJSONBody(map[string]any{
			"repo_id": "00000000-0000-0000-0000-000000000001", "pr_number": 1,
		}))
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestUnlinkPRFromTask_CrossProjectTaskRejected(t *testing.T) {
	tc := setupWithForeignTask(t)
	req := foreignTaskReq()
	req.PathParams["prId"] = "pr-1"
	res := tc.Call("DELETE", "/tasks/:taskId/pull-requests/:prId", req)
	if res.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %s", res.StatusCode, res.BodyString())
	}
}

// TestListTaskPRs_SkipsLinkFromAnotherProject covers the defense-in-depth
// layer on top of taskBelongsToProject: even for the caller's own,
// legitimate task, a github_task_pr_links row pointing at a PR that
// actually belongs to a different project (e.g. one created by a pre-fix
// caller exploiting the taskId substitution above) must not be returned.
func TestListTaskPRs_SkipsLinkFromAnotherProject(t *testing.T) {
	tc := setupPlugin(t)
	tc.DB.SeedRows("github_pull_requests",
		[]string{"id", "project_id", "repo_id", "pr_number", "github_pr_id", "title",
			"state", "html_url", "head_branch", "base_branch", "author", "merged_at", "created_at", "updated_at"},
		[][]any{
			{"pr-foreign", "other-project", "repo-1", 1, int64(1), "Someone else's PR",
				"open", "https://example.com", "feature", "main", "octocat", nil, "now", "now"},
		})
	tc.DB.SeedRows("github_task_pr_links",
		[]string{"id", "task_id", "pull_request_id", "created_at"},
		[][]any{{"link-1", testTaskID, "pr-foreign", "now"}})

	res := tc.Call("GET", "/tasks/:taskId/pull-requests", reqWithPathParams(map[string]string{"taskId": testTaskID}))
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, res.BodyString())
	}
	var env struct {
		Data []pullRequestResponse `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 0 {
		t.Fatalf("expected the cross-project-linked PR to be filtered out, got %+v", env.Data)
	}
}

// ── Webhook: project scoping + signature verification ────────────────────────
//
// receiveWebhook always responds 204 regardless of outcome (so GitHub
// doesn't retry on an application-level rejection), so the status code
// can't distinguish "accepted" from "rejected" the way it can for the
// authenticated API routes elsewhere in this file. These tests instead
// assert on tc.Log (plugintest.CapturingLogger), using the distinct log
// line each outcome takes — an event that reaches handleWebhookEvent's
// `default:` case only gets there after signature verification succeeds,
// which is what makes "unhandled event type" a reliable positive signal.

const testEncryptionKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64 hex chars = 32 bytes

func signedWebhookRequest(projectID string, payload []byte, secret string) plugintest.Request {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return plugintest.Request{
		PathParams: map[string]string{"projectId": projectID},
		Headers: map[string]string{
			"X-Github-Event":      "workflow_run", // any type receiveWebhook doesn't specifically handle
			"X-Hub-Signature-256": sig,
		},
		Body: payload,
	}
}

func seedEncryptedRepo(t *testing.T, tc *plugintest.Context, repoID, projectID, fullName, secret string) {
	t.Helper()
	tc.Config.Set("ENCRYPTION_KEY", testEncryptionKey)
	encSecret, err := encryptAES(secret, testEncryptionKey)
	if err != nil {
		t.Fatalf("failed to encrypt test secret: %v", err)
	}
	tc.DB.SeedRows("github_repositories",
		[]string{"id", "project_id", "integration_id", "owner", "repo_name", "full_name",
			"webhook_id", "webhook_secret_enc", "default_branch", "created_at", "updated_at"},
		append(tc.DB.AllRows("github_repositories"), []any{
			repoID, projectID, "integration-1", "octocat", "hello-world", fullName,
			"wh-1", encSecret, "main", "now", "now",
		}))
}

// TestReceiveWebhook_ScopesRepositoryLookupToURLProject pins the fix for a
// real cross-tenant interference bug: github_repositories.full_name is only
// unique per-project, so two projects can legitimately link the same repo.
// The old lookup (`WHERE full_name = $1`, no project_id) could resolve to
// whichever row Postgres happened to return first — using a DIFFERENT
// project's secret to verify a delivery meant for this one, which then
// fails signature verification and silently drops a legitimate delivery.
// This seeds the same full_name under two different projects with two
// different secrets, and confirms a delivery signed with project-1's own
// secret, addressed to project-1's URL, still verifies successfully even
// though project-2's row for the same repo full_name exists and was seeded
// first (so a naive full_name-only lookup would find it before project-1's).
func TestReceiveWebhook_ScopesRepositoryLookupToURLProject(t *testing.T) {
	tc := setupPlugin(t)
	seedEncryptedRepo(t, tc, "repo-other", "other-project", "octocat/hello-world", "other-projects-secret")
	seedEncryptedRepo(t, tc, "repo-mine", testProjectID, "octocat/hello-world", "my-projects-secret")

	payload := []byte(`{"repository":{"full_name":"octocat/hello-world"}}`)
	res := tc.Call("POST", "/webhook", signedWebhookRequest(testProjectID, payload, "my-projects-secret"))
	if res.StatusCode != 204 {
		t.Fatalf("expected 204, got %d: %s", res.StatusCode, res.BodyString())
	}
	if tc.Log.HasMessage("invalid webhook signature") {
		t.Fatal("signature verified against the wrong project's secret — repository lookup is not scoped to the URL's project")
	}
	if !tc.Log.HasMessage("unhandled event type") {
		t.Fatalf("expected signature verification to succeed and reach event dispatch; log entries: %+v", tc.Log.Entries())
	}
}

// TestReceiveWebhook_RejectsMissingSecret pins the fix for a fail-open path:
// a repository row with no webhook secret configured previously skipped
// HMAC verification entirely and trusted the payload; it must now be
// refused instead, without ever reaching event dispatch.
func TestReceiveWebhook_RejectsMissingSecret(t *testing.T) {
	tc := setupPlugin(t)
	tc.DB.SeedRows("github_repositories",
		[]string{"id", "project_id", "integration_id", "owner", "repo_name", "full_name",
			"webhook_id", "webhook_secret_enc", "default_branch", "created_at", "updated_at"},
		[][]any{{"repo-1", testProjectID, "integration-1", "octocat", "hello-world", "octocat/hello-world",
			"wh-1", "", "main", "now", "now"}})

	payload := []byte(`{"repository":{"full_name":"octocat/hello-world"}}`)
	req := plugintest.Request{
		PathParams: map[string]string{"projectId": testProjectID},
		Headers:    map[string]string{"X-Github-Event": "workflow_run", "X-Hub-Signature-256": "sha256=deadbeef"},
		Body:       payload,
	}
	res := tc.Call("POST", "/webhook", req)
	if res.StatusCode != 204 {
		t.Fatalf("expected 204, got %d: %s", res.StatusCode, res.BodyString())
	}
	if !tc.Log.HasMessage("no webhook secret configured") {
		t.Fatalf("expected the missing-secret delivery to be refused before dispatch; log entries: %+v", tc.Log.Entries())
	}
	if tc.Log.HasMessage("unhandled event type") {
		t.Fatal("delivery with no configured secret reached event dispatch — HMAC verification was skipped instead of failing closed")
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
