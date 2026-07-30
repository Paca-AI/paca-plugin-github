package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

const (
	ghBaseURL    = "https://api.github.com"
	ghAPIVersion = "2022-11-28"
)

// ghRepository is a minimal GitHub repository representation.
type ghRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// ghPullRequest is a minimal representation of a GitHub pull request.
type ghPullRequest struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"` // "open" | "closed"
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
}

// ghCombinedStatus is the response of GET /commits/{ref}/status — the legacy
// Status API used by CI integrations that post plain commit statuses rather
// than check runs (e.g. classic CircleCI/Travis integrations).
type ghCombinedStatus struct {
	State    string `json:"state"` // "pending" | "success" | "failure" | "error"
	Statuses []struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	} `json:"statuses"`
}

// ghCheckRunsResponse is the response of GET /commits/{ref}/check-runs — the
// modern Checks API used by GitHub Actions and most GitHub Apps.
type ghCheckRunsResponse struct {
	CheckRuns []struct {
		Name       string  `json:"name"`
		Status     string  `json:"status"`     // "queued" | "in_progress" | "completed"
		Conclusion *string `json:"conclusion"` // "success" | "failure" | ... | null while not completed
		HTMLURL    string  `json:"html_url"`
	} `json:"check_runs"`
}

// ghAPIError carries a non-2xx HTTP status and the GitHub error message.
type ghAPIError struct {
	StatusCode int
	Message    string
	Details    string
}

func (e *ghAPIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("github: API error %d: %s (%s)", e.StatusCode, e.Message, e.Details)
	}
	return fmt.Sprintf("github: API error %d: %s", e.StatusCode, e.Message)
}

// ghClient is a GitHub REST API v3 client authenticated via a PAT.
// All outbound HTTP is proxied through the paca.fetch WASM host function,
// which enforces domain allowlisting configured in plugin.json.
type ghClient struct {
	token string
}

func newGHClient(token string) *ghClient {
	return &ghClient{token: token}
}

func (c *ghClient) headers() map[string]string {
	return map[string]string{
		"Authorization":        "Bearer " + c.token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": ghAPIVersion,
	}
}

func (c *ghClient) get(_ context.Context, rawURL string, out any) error {
	resp, err := plugin.Fetch("GET", rawURL, c.headers(), "")
	if err != nil {
		return fmt.Errorf("githubclient: execute request: %w", err)
	}
	if resp.Status >= 400 {
		return ghParseAPIError(resp.Status, resp.Body)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(resp.Body), out); err != nil {
			return fmt.Errorf("githubclient: decode response: %w", err)
		}
	}
	return nil
}

func (c *ghClient) post(_ context.Context, rawURL string, body, out any) error {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("githubclient: encode body: %w", err)
	}
	hdrs := c.headers()
	hdrs["Content-Type"] = "application/json"
	resp, err := plugin.Fetch("POST", rawURL, hdrs, string(bodyJSON))
	if err != nil {
		return fmt.Errorf("githubclient: execute request: %w", err)
	}
	if resp.Status >= 400 {
		return ghParseAPIError(resp.Status, resp.Body)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(resp.Body), out); err != nil {
			return fmt.Errorf("githubclient: decode response: %w", err)
		}
	}
	return nil
}

func (c *ghClient) doDelete(_ context.Context, rawURL string) error {
	resp, err := plugin.Fetch("DELETE", rawURL, c.headers(), "")
	if err != nil {
		return fmt.Errorf("githubclient: execute request: %w", err)
	}
	if resp.Status == 404 || resp.Status == 204 {
		return nil
	}
	if resp.Status >= 400 {
		return ghParseAPIError(resp.Status, resp.Body)
	}
	return nil
}

func ghParseAPIError(statusCode int, body string) error {
	var errBody struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal([]byte(body), &errBody)
	if errBody.Message == "" {
		errBody.Message = fmt.Sprintf("HTTP %d", statusCode)
	}
	details := make([]string, 0, len(errBody.Errors))
	for _, e := range errBody.Errors {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		if e.Field != "" {
			details = append(details, fmt.Sprintf("%s: %s", e.Field, msg))
		} else {
			details = append(details, msg)
		}
	}
	return &ghAPIError{
		StatusCode: statusCode,
		Message:    errBody.Message,
		Details:    strings.Join(details, "; "),
	}
}

// ─── API methods ─────────────────────────────────────────────────────────────

func (c *ghClient) validateToken(ctx context.Context) error {
	var user struct {
		Login string `json:"login"`
	}
	return c.get(ctx, ghBaseURL+"/user", &user)
}

func (c *ghClient) listRepositories(ctx context.Context) ([]ghRepository, error) {
	seen := make(map[string]struct{})
	var all []ghRepository

	addRepo := func(r ghRepository) {
		if _, ok := seen[r.FullName]; ok {
			return
		}
		seen[r.FullName] = struct{}{}
		all = append(all, r)
	}

	// 1. Repos directly accessible to the user (owned + collaborator + org member).
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/user/repos?visibility=all&affiliation=owner,collaborator,organization_member&per_page=100&page=%d", ghBaseURL, page)
		var batch []ghRepository
		if err := c.get(ctx, url, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			addRepo(r)
		}
		if len(batch) < 100 {
			break
		}
	}

	// 2. List all orgs the user belongs to, then fetch each org's repos
	//    explicitly. This catches orgs where the membership is private or
	//    where the token has "read:org" but not full org repo visibility.
	var orgs []struct {
		Login string `json:"login"`
	}
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/user/orgs?per_page=100&page=%d", ghBaseURL, page)
		var batch []struct {
			Login string `json:"login"`
		}
		if err := c.get(ctx, url, &batch); err != nil {
			// Non-fatal: proceed with what we have from /user/repos.
			break
		}
		orgs = append(orgs, batch...)
		if len(batch) < 100 {
			break
		}
	}

	for _, org := range orgs {
		for page := 1; ; page++ {
			url := fmt.Sprintf("%s/orgs/%s/repos?type=all&per_page=100&page=%d", ghBaseURL, org.Login, page)
			var batch []ghRepository
			if err := c.get(ctx, url, &batch); err != nil {
				// Skip orgs where the token lacks access.
				break
			}
			for _, r := range batch {
				addRepo(r)
			}
			if len(batch) < 100 {
				break
			}
		}
	}

	return all, nil
}

func (c *ghClient) getRepository(ctx context.Context, owner, repo string) (*ghRepository, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", ghBaseURL, owner, repo)
	var r ghRepository
	if err := c.get(ctx, url, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *ghClient) createWebhook(ctx context.Context, owner, repo, webhookURL, secret string, events []string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks", ghBaseURL, owner, repo)
	body := map[string]any{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          webhookURL,
			"content_type": "json",
			"secret":       secret,
		},
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := c.post(ctx, url, body, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (c *ghClient) deleteWebhook(ctx context.Context, owner, repo string, hookID int64) error {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks/%d", ghBaseURL, owner, repo, hookID)
	return c.doDelete(ctx, url)
}

func (c *ghClient) getPullRequest(ctx context.Context, owner, repo string, prNumber int) (*ghPullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", ghBaseURL, owner, repo, prNumber)
	var pr ghPullRequest
	if err := c.get(ctx, url, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func (c *ghClient) createPullRequest(ctx context.Context, owner, repo, title, head, base, body string) (*ghPullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", ghBaseURL, owner, repo)
	reqBody := map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
	}
	if body != "" {
		reqBody["body"] = body
	}
	var pr ghPullRequest
	if err := c.post(ctx, url, reqBody, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// getPullRequestDiff fetches the unified diff for a pull request via GitHub's
// diff media type. Unlike get(), the response body is raw diff text, not
// JSON, so it bypasses get()'s json.Unmarshal step.
func (c *ghClient) getPullRequestDiff(_ context.Context, owner, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", ghBaseURL, owner, repo, prNumber)
	hdrs := c.headers()
	hdrs["Accept"] = "application/vnd.github.v3.diff"
	resp, err := plugin.Fetch("GET", url, hdrs, "")
	if err != nil {
		return "", fmt.Errorf("githubclient: execute request: %w", err)
	}
	if resp.Status >= 400 {
		return "", ghParseAPIError(resp.Status, resp.Body)
	}
	return resp.Body, nil
}

// createPullRequestReview submits a review on a pull request. event must be
// one of "APPROVE", "REQUEST_CHANGES", or "COMMENT" (GitHub requires a
// non-empty body for the latter two).
func (c *ghClient) createPullRequestReview(ctx context.Context, owner, repo string, prNumber int, event, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", ghBaseURL, owner, repo, prNumber)
	reqBody := map[string]string{"event": event}
	if body != "" {
		reqBody["body"] = body
	}
	return c.post(ctx, url, reqBody, nil)
}

// createIssueComment adds a general (non-review) comment to a pull request.
// GitHub represents PR comments as issue comments under /issues/{number}/comments.
func (c *ghClient) createIssueComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", ghBaseURL, owner, repo, prNumber)
	return c.post(ctx, url, map[string]string{"body": body}, nil)
}

// getCombinedStatus fetches legacy commit statuses posted for ref (a SHA,
// branch, or tag name).
func (c *ghClient) getCombinedStatus(ctx context.Context, owner, repo, ref string) (*ghCombinedStatus, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s/status", ghBaseURL, owner, repo, ref)
	var status ghCombinedStatus
	if err := c.get(ctx, url, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// getCheckRuns fetches check runs (GitHub Actions and other Checks-API-based
// CI) for ref (a SHA, branch, or tag name).
func (c *ghClient) getCheckRuns(ctx context.Context, owner, repo, ref string) (*ghCheckRunsResponse, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs", ghBaseURL, owner, repo, ref)
	var result ghCheckRunsResponse
	if err := c.get(ctx, url, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ghClient) branchExists(ctx context.Context, owner, repo, branch string) error {
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", ghBaseURL, owner, repo, branch)
	return c.get(ctx, refURL, &struct{}{})
}

func (c *ghClient) createBranch(ctx context.Context, owner, repo, newBranch, sourceBranch string) error {
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", ghBaseURL, owner, repo, sourceBranch)
	var refResp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.get(ctx, refURL, &refResp); err != nil {
		return fmt.Errorf("resolve source branch %q: %w", sourceBranch, err)
	}

	createURL := fmt.Sprintf("%s/repos/%s/%s/git/refs", ghBaseURL, owner, repo)
	return c.post(ctx, createURL, map[string]string{
		"ref": "refs/heads/" + newBranch,
		"sha": refResp.Object.SHA,
	}, nil)
}
