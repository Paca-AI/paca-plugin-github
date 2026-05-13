package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/google/uuid"
)

// branchTaskRefRe matches a task-ID prefix in a branch name (e.g. "PROJ-42").
var branchTaskRefRe = regexp.MustCompile(`(?i)\b([A-Z][A-Z0-9]{1,19})-(\d{1,6})\b`)

// ─── POST /webhook ────────────────────────────────────────────────────────────

func (p *githubPlugin) receiveWebhook(req *plugin.Request, res *plugin.Response) {
	event := req.Headers["x-github-event"]
	signature := req.Headers["x-hub-signature-256"]

	body := req.Body
	if len(body) == 0 {
		res.NoContent()
		return
	}

	repoFullName := extractRepoFullName(body)
	if repoFullName == "" {
		res.NoContent()
		return
	}

	// Always 204 so GitHub does not retry on application errors.
	_ = p.handleWebhookEvent(repoFullName, event, signature, body)
	res.NoContent()
}

func (p *githubPlugin) handleWebhookEvent(repoFullName, event, signature string, payload []byte) error {
	// Look up the repository by full name.
	result, err := p.db.Query(`
		SELECT id, project_id, integration_id, owner, repo_name, full_name, default_branch, webhook_secret_enc
		FROM github_repositories WHERE full_name = $1
	`, repoFullName)
	if err != nil || len(result.Rows) == 0 {
		return nil
	}
	sc := newRowScanner(result.Columns, result.Rows[0])
	repoID := sc.str("id")
	projectID := sc.str("project_id")
	webhookSecretEnc := sc.str("webhook_secret_enc")

	// Verify HMAC signature.
	if webhookSecretEnc != "" {
		secret, dErr := p.decrypt(webhookSecretEnc)
		if dErr == nil && !verifyHMAC(payload, secret, signature) {
			return nil // silently drop invalid signatures
		}
	}

	switch event {
	case "pull_request":
		return p.handlePREvent(repoID, projectID, payload)
	case "push":
		return p.handlePushEvent(repoID, projectID, payload)
	}
	return nil
}

func (p *githubPlugin) handlePREvent(repoID, projectID string, payload []byte) error {
	var event struct {
		Action      string        `json:"action"`
		PullRequest ghPullRequest `json:"pull_request"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil
	}
	gh := &event.PullRequest
	state := gh.State
	if gh.Merged {
		state = "merged"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	var mergedAtStr *string
	if gh.MergedAt != nil {
		s := gh.MergedAt.UTC().Format(time.RFC3339Nano)
		mergedAtStr = &s
	}

	// Check if PR already cached.
	existResult, _ := p.db.Query(
		`SELECT id, created_at FROM github_pull_requests WHERE repo_id = $1 AND pr_number = $2`,
		repoID, gh.Number,
	)
	prID := uuid.New().String()
	prCreatedAt := now
	if existResult != nil && len(existResult.Rows) > 0 {
		eSc := newRowScanner(existResult.Columns, existResult.Rows[0])
		prID = eSc.str("id")
		prCreatedAt = eSc.str("created_at")
	}

	// Upsert the PR cache.
	_, err := p.db.Exec(`
		INSERT INTO github_pull_requests
			(id, project_id, repo_id, pr_number, github_pr_id, title, state, html_url, head_branch, base_branch, author, merged_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (repo_id, pr_number) DO UPDATE SET
			title=$6, state=$7, html_url=$8, head_branch=$9, base_branch=$10,
			author=$11, merged_at=$12, updated_at=$14
	`, prID, projectID, repoID, gh.Number, gh.ID, gh.Title, state,
		gh.HTMLURL, gh.Head.Ref, gh.Base.Ref, gh.User.Login, mergedAtStr, prCreatedAt, now)
	if err != nil {
		return err
	}

	// On "opened"/"reopened": auto-link to task if head branch is already linked.
	if event.Action == "opened" || event.Action == "reopened" {
		brResult, _ := p.db.Query(
			`SELECT task_id FROM github_task_branches WHERE repo_id = $1 AND branch_name = $2`,
			repoID, gh.Head.Ref,
		)
		if brResult != nil && len(brResult.Rows) > 0 {
			taskID := newRowScanner(brResult.Columns, brResult.Rows[0]).str("task_id")
			linkID := uuid.New().String()
			_, _ = p.db.Exec(`
				INSERT INTO github_task_pr_links (id, task_id, pull_request_id, created_at)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (task_id, pull_request_id) DO NOTHING
			`, linkID, taskID, prID, now)

			plugin.EmitEvent("github.pr_linked", map[string]any{
				"project_id": projectID,
				"task_id":    taskID,
				"repo_id":    repoID,
				"pr_number":  gh.Number,
			})
		}
	}

	// Emit PR updated for all linked tasks.
	linkedResult, _ := p.db.Query(`SELECT task_id FROM github_task_pr_links WHERE pull_request_id = $1`, prID)
	if linkedResult != nil {
		for _, row := range linkedResult.Rows {
			sc := newRowScanner(linkedResult.Columns, row)
			plugin.EmitEvent("github.pr_updated", map[string]any{
				"project_id": projectID,
				"task_id":    sc.str("task_id"),
				"repo_id":    repoID,
				"pr_number":  gh.Number,
				"action":     event.Action,
			})
		}
	}
	return nil
}

func (p *githubPlugin) handlePushEvent(repoID, projectID string, payload []byte) error {
	var event struct {
		Ref     string `json:"ref"`
		Created bool   `json:"created"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || !event.Created || event.Deleted {
		return nil
	}

	// Extract "refs/heads/branch-name" → "branch-name".
	branchName := strings.TrimPrefix(event.Ref, "refs/heads/")
	if branchName == event.Ref {
		return nil
	}

	prefix, taskNumber, ok := extractBranchTaskRef(branchName)
	if !ok {
		return nil
	}

	// Look up the task by project prefix and task number.
	taskResult, tErr := p.db.Query(`
		SELECT t.id FROM tasks t
		JOIN projects pr ON pr.id = t.project_id
		WHERE UPPER(pr.task_id_prefix) = UPPER($1) AND t.task_number = $2
		LIMIT 1
	`, prefix, taskNumber)
	if tErr != nil || len(taskResult.Rows) == 0 {
		return nil
	}
	taskID := newRowScanner(taskResult.Columns, taskResult.Rows[0]).str("id")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	branchID := uuid.New().String()
	_, _ = p.db.Exec(`
		INSERT INTO github_task_branches (id, task_id, repo_id, branch_name, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (task_id, repo_id, branch_name) DO NOTHING
	`, branchID, taskID, repoID, branchName, now)

	plugin.EmitEvent("github.branch_linked", map[string]any{
		"project_id":  projectID,
		"task_id":     taskID,
		"repo_id":     repoID,
		"branch_name": branchName,
	})
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func verifyHMAC(payload []byte, secret, signatureHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	expected := signatureHeader[len(prefix):]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	got := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(expected))
}

func extractRepoFullName(payload []byte) string {
	var v struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return ""
	}
	return v.Repository.FullName
}

func extractBranchTaskRef(branchName string) (prefix string, taskNumber int64, ok bool) {
	m := branchTaskRefRe.FindStringSubmatch(branchName)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.ToUpper(m[1]), n, true
}
