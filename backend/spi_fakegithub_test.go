package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// fakeGitHub answers the subset of the GitHub API the provider uses, from
// seeded in-memory data. It enforces what GitHub enforces and what bugs in the
// provider would violate: App JWTs must be validly signed, every other call
// needs a token minted by this fake, minted tokens record their scope, and
// state-changing calls change state the reads then show.
type fakeGitHub struct {
	t   *testing.T
	mu  sync.Mutex
	key *rsa.PrivateKey

	appID          int64
	installationID int64
	installs       map[int64]fakeInstall // installations of the App
	validTokens    map[string]int64      // token → installation it was minted for
	mints          []mintCall
	calls          []string
	tokenSeq       int

	prs         map[int]*fakePR
	nextNumber  int
	comments    []fakeComment
	nextID      int64
	threads     map[string]*fakeThread
	forceError  map[string]fakeResponse // "METHOD /path" → canned answer
	userLookups []bool                  // for each GET /users/{login}: was it authenticated?
}

type fakeInstall struct {
	Login string
	Type  string
	// Selected lists the repositories (names) a "selected" installation covers;
	// nil means all of the account's repositories.
	Selected []string
}

func (in fakeInstall) selection() string {
	if in.Selected != nil {
		return "selected"
	}
	return "all"
}

type mintCall struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
	Unscoped     bool
}

type fakePR struct {
	Number    int
	Title     string
	Body      string
	Draft     bool
	Merged    bool
	State     string
	HeadRef   string
	HeadSHA   string
	Labels    []string
	Reviewers []string
	Files     []string
	Diff      string
}

type fakeComment struct {
	ID     int64
	Number int
	Body   string
	Login  string
}

type fakeThread struct {
	ID       string
	Resolved bool
	Replies  int
}

type fakeResponse struct {
	status  int
	body    string
	headers map[string]string
}

const (
	fakeWebhookSecret = "whsec_fake_secret_value"
	fakeSlug          = "paca-bot"
	fakeAppRowID      = "app-row-1"
)

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGitHub{
		t: t, key: key, appID: 4242, installationID: 99, validTokens: map[string]int64{}, installs: map[int64]fakeInstall{99: {"acme", "Organization", nil}, 100: {"other-org", "Organization", []string{"lib"}}},
		prs: map[int]*fakePR{}, nextNumber: 2, nextID: 9000, forceError: map[string]fakeResponse{},
		threads: map[string]*fakeThread{"PRRT_1": {ID: "PRRT_1"}},
	}
	g.prs[1] = &fakePR{
		Number: 1, Title: "Add login", Body: "body", State: "open", HeadRef: "feat/PACA-12-login", HeadSHA: "abc123",
		Files: []string{"docs/guide.md", "src/main.go", "src/util.go"},
		Diff: "diff --git a/docs/guide.md b/docs/guide.md\n--- a/docs/guide.md\n+++ b/docs/guide.md\n@@ -1 +1,2 @@\n # Guide\n+more\n" +
			"diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1,3 +1,4 @@\n package main\n+// hi\n" +
			"diff --git a/src/util.go b/src/util.go\n--- a/src/util.go\n+++ b/src/util.go\n@@ -1 +1,2 @@\n package main\n+func f() {}\n",
	}
	g.comments = []fakeComment{{ID: 8001, Number: 1, Body: "first", Login: "octocat"}}
	oldFetch := fetch
	fetch = g.fetch
	t.Cleanup(func() { fetch = oldFetch })
	return g
}

func (g *fakeGitHub) pemKey() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(g.key)}))
}

func (g *fakeGitHub) called(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func resp(status int, v any) (*plugin.FetchResponse, error) {
	b, _ := json.Marshal(v)
	return &plugin.FetchResponse{Status: status, Body: string(b), Headers: map[string]string{}}, nil
}

func text(status int, s string) (*plugin.FetchResponse, error) {
	return &plugin.FetchResponse{Status: status, Body: s, Headers: map[string]string{}}, nil
}

func notFound() (*plugin.FetchResponse, error) {
	return resp(404, map[string]string{"message": "Not Found"})
}

// verifyJWT checks an App JWT's RS256 signature, issuer and expiry.
func (g *fakeGitHub) verifyJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&g.key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	_ = json.Unmarshal(raw, &c)
	return c.Iss == strconv.FormatInt(g.appID, 10) && c.Exp > time.Now().Unix() && c.Exp <= time.Now().Add(10*time.Minute).Unix()
}

func (g *fakeGitHub) fetch(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "api.github.com" {
		g.t.Errorf("request to a host outside the allow-list: %s", rawURL)
		return nil, fmt.Errorf("blocked")
	}
	path := u.Path
	g.calls = append(g.calls, method+" "+path)
	if f, ok := g.forceError[method+" "+path]; ok {
		return &plugin.FetchResponse{Status: f.status, Body: f.body, Headers: f.headers}, nil
	}
	auth := strings.TrimPrefix(headers["Authorization"], "Bearer ")
	var payload map[string]any
	if body != "" {
		_ = json.Unmarshal([]byte(body), &payload)
	}

	switch {
	case strings.HasPrefix(path, "/app-manifests/"):
		if path != "/app-manifests/c0de/conversions" {
			return resp(404, map[string]string{"message": "Not Found"})
		}
		return resp(201, map[string]any{"id": g.appID, "slug": fakeSlug, "name": "Paca", "html_url": "https://github.com/apps/" + fakeSlug,
			"webhook_secret": fakeWebhookSecret, "pem": g.pemKey(), "owner": map[string]string{"login": "acme"}})

	case strings.HasPrefix(path, "/app/"):
		if !g.verifyJWT(auth) {
			return resp(401, map[string]string{"message": "A JSON web token could not be decoded"})
		}
		rest := strings.TrimPrefix(path, "/app/installations")
		switch {
		case method == "GET" && path == "/app/installations":
			var out []map[string]any
			for _, id := range []int64{99, 100, 199, 205} {
				if in, ok := g.installs[id]; ok {
					out = append(out, map[string]any{"id": id, "repository_selection": in.selection(), "account": map[string]string{"login": in.Login, "type": in.Type}})
				}
			}
			return resp(200, out)
		case method == "POST" && strings.HasSuffix(rest, "/access_tokens"):
			id, _ := strconv.ParseInt(strings.Trim(strings.TrimSuffix(rest, "/access_tokens"), "/"), 10, 64)
			in, ok := g.installs[id]
			if !ok {
				return notFound()
			}
			var mc mintCall
			_ = json.Unmarshal([]byte(body), &mc)
			mc.Unscoped = len(mc.Repositories) == 0
			for _, r := range mc.Repositories {
				_, owned := fakeRepos()[in.Login+"/"+r]
				if in.Selected != nil && !slices.Contains(in.Selected, r) {
					owned = false
				}
				if !owned {
					return resp(422, map[string]string{"message": "There is at least one repository that does not exist or is not accessible to the parent installation."})
				}
			}
			g.mints = append(g.mints, mc)
			g.tokenSeq++
			tok := fmt.Sprintf("ghs_token_%d", g.tokenSeq)
			g.validTokens[tok] = id
			return resp(201, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case method == "GET" && strings.HasPrefix(rest, "/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(rest, "/"), 10, 64)
			in, ok := g.installs[id]
			if !ok {
				return notFound()
			}
			return resp(200, map[string]any{"id": id, "repository_selection": in.selection(), "account": map[string]string{"login": in.Login, "type": in.Type}})
		}
		return notFound()

	case strings.HasPrefix(path, "/users/") && method == "GET":
		// A public endpoint: anonymous or an installation token work; an App JWT
		// is only valid on /app/* and GitHub answers 401 to it here.
		if auth != "" && g.validTokens[auth] == 0 {
			return resp(401, map[string]string{"message": "Bad credentials"})
		}
		g.userLookups = append(g.userLookups, auth != "")
		ids := map[string]int{"acme": 555, "other-org": 556}
		id, ok := ids[strings.TrimPrefix(path, "/users/")]
		if !ok {
			return notFound()
		}
		return resp(200, map[string]any{"id": id, "login": strings.TrimPrefix(path, "/users/")})
	}

	if g.validTokens[auth] == 0 {
		return resp(401, map[string]string{"message": "Bad credentials"})
	}
	return g.repoAPI(method, path, u.Query(), headers, payload, body)
}

func fakeRepos() map[string]map[string]any {
	return map[string]map[string]any{
		"acme/web":      {"id": 1, "full_name": "acme/web", "default_branch": "main", "private": true, "description": "web app"},
		"acme/api":      {"id": 2, "full_name": "acme/api", "default_branch": "main", "private": false},
		"other-org/lib": {"id": 3, "full_name": "other-org/lib", "default_branch": "main", "private": false},
		// public, so any token can read it, but the installation does not cover it
		"other-org/public-extra": {"id": 4, "full_name": "other-org/public-extra", "default_branch": "main", "private": false},
	}
}

func (g *fakeGitHub) repoAPI(method, path string, q url.Values, headers map[string]string, payload map[string]any, raw string) (*plugin.FetchResponse, error) {
	// A token only sees the repositories of the account its installation is on.
	account := g.installs[g.validTokens[strings.TrimPrefix(headers["Authorization"], "Bearer ")]].Login
	repos := map[string]map[string]any{}
	for name, r := range fakeRepos() {
		if strings.HasPrefix(name, account+"/") {
			repos[name] = r
		}
	}
	if path == "/installation/repositories" {
		per, _ := strconv.Atoi(q.Get("per_page"))
		pg, _ := strconv.Atoi(q.Get("page"))
		var all []map[string]any
		for _, name := range []string{"acme/api", "acme/web", "other-org/lib"} {
			if r, ok := repos[name]; ok {
				all = append(all, r)
			}
		}
		start := (pg - 1) * per
		if start > len(all) {
			start = len(all)
		}
		end := start + per
		if end > len(all) {
			end = len(all)
		}
		return resp(200, map[string]any{"total_count": len(all), "repositories": all[start:end]})
	}
	if path == "/graphql" {
		return g.graphql(payload)
	}
	rest, ok := strings.CutPrefix(path, "/repos/")
	if !ok {
		return notFound()
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || repos[parts[0]+"/"+parts[1]] == nil {
		return notFound()
	}
	repo := repos[parts[0]+"/"+parts[1]]
	sub := ""
	if len(parts) == 3 {
		sub = parts[2]
	}
	if sub == "" {
		return resp(200, repo)
	}
	return g.repoSub(method, sub, q, headers, payload, raw)
}

func (g *fakeGitHub) prJSON(pr *fakePR) map[string]any {
	labels := []map[string]string{}
	for _, l := range pr.Labels {
		labels = append(labels, map[string]string{"name": l})
	}
	revs := []map[string]string{}
	for _, r := range pr.Reviewers {
		revs = append(revs, map[string]string{"login": r})
	}
	return map[string]any{
		"node_id": fmt.Sprintf("PR_%d", pr.Number), "number": pr.Number, "title": pr.Title, "body": pr.Body, "state": pr.State,
		"draft": pr.Draft, "merged": pr.Merged, "html_url": fmt.Sprintf("https://github.com/acme/web/pull/%d", pr.Number),
		"user": map[string]string{"login": "octocat", "type": "User"}, "labels": labels, "requested_reviewers": revs,
		"head": map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA, "repo": map[string]string{"full_name": "acme/web"}},
		"base": map[string]any{"ref": "main", "sha": "base000", "repo": map[string]string{"full_name": "acme/web"}},
	}
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func (g *fakeGitHub) repoSub(method, sub string, q url.Values, headers map[string]string, payload map[string]any, raw string) (*plugin.FetchResponse, error) {
	seg := strings.Split(sub, "/")
	num := func(i int) (*fakePR, bool) {
		if len(seg) <= i {
			return nil, false
		}
		n, _ := strconv.Atoi(seg[i])
		pr, ok := g.prs[n]
		return pr, ok
	}
	switch {
	// ── pull requests ──
	case method == "POST" && sub == "pulls":
		head := str(payload, "head")
		for _, pr := range g.prs {
			if pr.HeadRef == head && pr.State == "open" {
				return resp(422, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"message": "A pull request already exists for acme:" + head + "."}}})
			}
		}
		pr := &fakePR{Number: g.nextNumber, Title: str(payload, "title"), Body: str(payload, "body"), State: "open", HeadRef: head, HeadSHA: "sha-" + head}
		pr.Draft, _ = payload["draft"].(bool)
		g.nextNumber++
		g.prs[pr.Number] = pr
		return resp(201, g.prJSON(pr))
	case seg[0] == "pulls" && len(seg) == 2 && method == "GET":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		if strings.Contains(headers["Accept"], "diff") {
			return text(200, pr.Diff)
		}
		return resp(200, g.prJSON(pr))
	case seg[0] == "pulls" && len(seg) == 2 && method == "PATCH":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		if v := str(payload, "title"); v != "" {
			pr.Title = v
		}
		if v := str(payload, "state"); v != "" {
			pr.State = v
		}
		return resp(200, g.prJSON(pr))
	case len(seg) == 3 && seg[0] == "pulls" && seg[2] == "files":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		files := []map[string]any{}
		for _, f := range pr.Files {
			files = append(files, map[string]any{"filename": f, "status": "modified", "additions": 1, "deletions": 0})
		}
		return resp(200, files)
	case len(seg) == 3 && seg[0] == "pulls" && seg[2] == "reviews" && method == "POST":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		if cs, _ := payload["comments"].([]any); len(cs) > 0 {
			for _, c := range cs {
				path := str(c.(map[string]any), "path")
				found := false
				for _, f := range pr.Files {
					found = found || f == path
				}
				if !found {
					return resp(422, map[string]string{"message": "Unprocessable Entity: Pull request review thread path could not be resolved"})
				}
			}
		}
		g.nextID++
		return resp(200, map[string]any{"id": g.nextID, "html_url": "https://github.com/acme/web/pull/1#pullrequestreview-1"})
	case len(seg) == 3 && seg[0] == "pulls" && seg[2] == "requested_reviewers" && method == "POST":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		for _, r := range payload["reviewers"].([]any) {
			pr.Reviewers = append(pr.Reviewers, r.(string))
		}
		return resp(201, g.prJSON(pr))
	case len(seg) == 3 && seg[0] == "pulls" && seg[2] == "merge" && method == "PUT":
		pr, ok := num(1)
		if !ok {
			return notFound()
		}
		if str(payload, "sha") != pr.HeadSHA {
			return resp(409, map[string]string{"message": "Head branch was modified. Review and try the merge again."})
		}
		pr.Merged, pr.State = true, "closed"
		return resp(200, map[string]any{"merged": true, "sha": "merge-sha"})

	// ── issues and comments ──
	case seg[0] == "issues" && len(seg) == 2 && method == "GET":
		n, _ := strconv.Atoi(seg[1])
		if n != 7 && g.prs[n] == nil {
			return notFound()
		}
		return resp(200, map[string]any{"number": n, "title": "Bug report", "body": "it broke", "state": "open",
			"html_url": fmt.Sprintf("https://github.com/acme/web/issues/%d", n), "user": map[string]string{"login": "reader"},
			"labels": []map[string]string{{"name": "bug"}}, "assignees": []map[string]string{}})
	case seg[0] == "issues" && len(seg) == 2 && method == "PATCH":
		return resp(200, map[string]any{"number": 7})
	case len(seg) == 3 && seg[0] == "issues" && seg[2] == "comments":
		n, _ := strconv.Atoi(seg[1])
		if n != 7 && g.prs[n] == nil {
			return notFound()
		}
		if method == "POST" {
			g.nextID++
			c := fakeComment{ID: g.nextID, Number: n, Body: str(payload, "body"), Login: "paca-bot[bot]"}
			g.comments = append(g.comments, c)
			return resp(201, map[string]any{"id": c.ID, "html_url": fmt.Sprintf("https://github.com/acme/web/pull/%d#issuecomment-%d", n, c.ID), "body": c.Body, "user": map[string]string{"login": c.Login, "type": "Bot"}})
		}
		out := []map[string]any{}
		for _, c := range g.comments {
			if c.Number == n {
				out = append(out, map[string]any{"id": c.ID, "body": c.Body, "html_url": "https://github.com/acme/web/pull/1#c", "created_at": "2026-01-01T00:00:00Z", "user": map[string]string{"login": c.Login, "type": "User"}})
			}
		}
		return resp(200, out)
	case len(seg) == 3 && seg[0] == "issues" && seg[2] == "labels" && method == "POST":
		n, _ := strconv.Atoi(seg[1])
		pr, ok := g.prs[n]
		if !ok && n != 7 {
			return notFound()
		}
		if ok {
			for _, l := range payload["labels"].([]any) {
				pr.Labels = append(pr.Labels, l.(string))
			}
		}
		return resp(200, []any{})

	// ── checks ──
	case len(seg) == 3 && seg[0] == "commits" && seg[2] == "check-runs":
		if seg[1] != "abc123" {
			return resp(200, map[string]any{"check_runs": []any{}})
		}
		return resp(200, map[string]any{"check_runs": []map[string]any{
			{"id": 5001, "name": "ci/test", "status": "completed", "conclusion": "failure", "html_url": "https://github.com/acme/web/runs/5001", "output": map[string]string{"title": "1 test failed"}},
			{"id": 5002, "name": "ci/lint", "status": "completed", "conclusion": "success", "html_url": "https://github.com/acme/web/runs/5002"},
			{"id": 5003, "name": "ci/e2e", "status": "in_progress"},
		}})
	case len(seg) == 3 && seg[0] == "commits" && seg[2] == "status":
		return resp(200, map[string]any{"statuses": []map[string]string{{"context": "legacy/ci", "state": "success"}}})
	case sub == "actions/jobs/5001/logs":
		return text(200, "setup\nrun tests\nFAIL: TestLogin (0.01s)\nexit status 1\n")
	case strings.HasPrefix(sub, "actions/jobs/"):
		return notFound()
	case sub == "check-runs/5003":
		return resp(200, map[string]any{"output": map[string]string{"title": "Running", "summary": "still going"}})
	case sub == "check-runs" && method == "POST":
		g.nextID++
		return resp(201, map[string]any{"id": g.nextID})
	case len(seg) == 2 && seg[0] == "check-runs" && method == "PATCH":
		return resp(200, map[string]any{"id": 1})

	// ── policy, files ──
	case len(seg) == 3 && seg[0] == "collaborators" && seg[2] == "permission":
		perm := map[string]string{"octocat": "write", "reader": "read", "owner": "admin", "lead": "maintain"}[seg[1]]
		if perm == "" {
			return resp(404, map[string]string{"message": "Not Found"})
		}
		legacy := perm
		if perm == "maintain" {
			legacy = "write"
		}
		return resp(200, map[string]string{"permission": legacy, "role_name": perm})
	case seg[0] == "branches" && len(seg) == 2:
		if seg[1] != "main" {
			return notFound()
		}
		return resp(200, map[string]any{"protected": true, "protection": map[string]any{"required_status_checks": map[string]any{"strict": true}}})
	case seg[0] == "contents":
		if strings.Join(seg[1:], "/") != "README.md" {
			return notFound()
		}
		return resp(200, map[string]any{"type": "file", "sha": "f1", "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte("# Hello Paca\nsome text\n"))})
	}
	return notFound()
}

func gqlErr(typ, msg string) (*plugin.FetchResponse, error) {
	return resp(200, map[string]any{"data": nil, "errors": []map[string]string{{"type": typ, "message": msg}}})
}

func (g *fakeGitHub) graphql(payload map[string]any) (*plugin.FetchResponse, error) {
	q := str(payload, "query")
	vars, _ := payload["variables"].(map[string]any)
	switch {
	case strings.Contains(q, "reviewThreads"):
		if n, _ := vars["number"].(float64); int(n) != 1 {
			return gqlErr("NOT_FOUND", "Could not resolve to a PullRequest with the number of 999.")
		}
		nodes := []map[string]any{}
		for _, id := range []string{"PRRT_1"} {
			th := g.threads[id]
			nodes = append(nodes, map[string]any{"id": th.ID, "isResolved": th.Resolved, "isOutdated": false, "path": "src/main.go", "line": 3,
				"comments": map[string]any{"nodes": []map[string]any{{"databaseId": 7001, "body": "please rename", "url": "https://github.com/acme/web/pull/1#discussion_r7001",
					"createdAt": "2026-01-01T00:00:00Z", "author": map[string]string{"login": "octocat", "__typename": "User"}}}}})
		}
		return resp(200, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": nodes}}}}})
	case strings.Contains(q, "addPullRequestReviewThreadReply"):
		th := g.threads[str(vars, "id")]
		if th == nil {
			return gqlErr("NOT_FOUND", "Could not resolve to a node with the global id of '"+str(vars, "id")+"'")
		}
		th.Replies++
		g.nextID++
		return resp(200, map[string]any{"data": map[string]any{"addPullRequestReviewThreadReply": map[string]any{"comment": map[string]any{"databaseId": g.nextID, "url": "https://github.com/acme/web/pull/1#discussion_r1"}}}})
	case strings.Contains(q, "resolveReviewThread") || strings.Contains(q, "unresolveReviewThread"):
		th := g.threads[str(vars, "id")]
		if th == nil {
			return gqlErr("NOT_FOUND", "Could not resolve to a node with the global id of '"+str(vars, "id")+"'")
		}
		th.Resolved = strings.Contains(q, "mutation($id:ID!){ resolveReviewThread")
		return resp(200, map[string]any{"data": map[string]any{}})
	case strings.Contains(q, "markPullRequestReadyForReview"), strings.Contains(q, "convertPullRequestToDraft"):
		return resp(200, map[string]any{"data": map[string]any{}})
	}
	return gqlErr("UNKNOWN", "unsupported query in the fake")
}

// revokeTokens invalidates every installation token handed out so far, as
// GitHub does when an App is uninstalled (or a token is revoked).
func (g *fakeGitHub) revokeTokens() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.validTokens = map[string]int64{}
}

// uninstall removes an installation and revokes the tokens issued for it.
func (g *fakeGitHub) uninstall(id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.installs, id)
	for tok, inst := range g.validTokens {
		if inst == id {
			delete(g.validTokens, tok)
		}
	}
}
