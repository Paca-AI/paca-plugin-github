package main

import (
	"context"
	"encoding/json"
	"errors"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

var bg = context.Background()

func codeOf(t *testing.T, err error) gp.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var pe *gp.Error
	if !errors.As(err, &pe) {
		t.Fatalf("not a *gitprovider.Error: %T %v", err, err)
	}
	return pe.Code
}

func webTarget() gp.Target {
	return gp.Target{Identity: gp.IdentityRef{ID: fakeAppRowID}, Repo: gp.RepoRef{Host: githubHost, FullPath: "acme/web"}}
}

func TestDescribe_DeclaresWhatGitHubCanDo(t *testing.T) {
	p, _, _ := seededProvider(t)
	c := p.Describe().Capabilities
	for _, trig := range []gp.TriggerKind{gp.TriggerMention, gp.TriggerLabel, gp.TriggerReviewRequest, gp.TriggerAssignment} {
		if !c.SupportsTrigger(trig) {
			t.Errorf("%s should be declared", trig)
		}
	}
	if c.CredentialModel != gp.CredentialEphemeral || c.WebhookScope != gp.WebhookApp || c.StatusKind != gp.StatusCheckRun {
		t.Errorf("unexpected model: %+v", c)
	}
	// Two kinds of identity: an App (ephemeral credentials, app-level webhook,
	// installed per account) and a token (static, a webhook per repository).
	app, tok := c.Profile(gp.IdentityApp), c.Profile(gp.IdentityToken)
	if app.CredentialModel != gp.CredentialEphemeral || app.WebhookScope != gp.WebhookApp || !app.AccessGrants {
		t.Errorf("app profile = %+v", app)
	}
	if tok.CredentialModel != gp.CredentialStatic || tok.WebhookScope != gp.WebhookRepo || tok.AccessGrants || !c.WebhookManagement {
		t.Errorf("token profile = %+v (webhook management %v)", tok, c.WebhookManagement)
	}
	if !strings.Contains(strings.Join(c.CIConfigGlobs, ","), ".github/workflows/**") {
		t.Errorf("CI config globs = %v", c.CIConfigGlobs)
	}
	if c.CodeSearch || c.RerunChecks || c.Reactions {
		t.Errorf("operations that are not implemented must not be declared: %+v", c)
	}
}

func TestMintCredential_IsNarrowedToOneRepoAndWhatGitNeeds(t *testing.T) {
	p, g, _ := seededProvider(t)
	for access, want := range map[gp.Access]string{gp.AccessRead: "read", gp.AccessWrite: "write"} {
		cred, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: access})
		if err != nil {
			t.Fatal(err)
		}
		last := g.mints[len(g.mints)-1]
		if len(last.Repositories) != 1 || last.Repositories[0] != "web" {
			t.Errorf("%s: token must be limited to the one repository, got %v", access, last.Repositories)
		}
		if last.Permissions["contents"] != want || last.Permissions["metadata"] != "read" || len(last.Permissions) != 2 {
			t.Errorf("%s: permissions = %v, want exactly contents:%s + metadata:read (no pull_requests, workflows, ...)", access, last.Permissions, want)
		}
		if cred.Username != "x-access-token" || !strings.HasPrefix(cred.Secret, "ghs_token_") || time.Until(cred.ExpiresAt) < 30*time.Minute {
			t.Errorf("credential = %+v", cred)
		}
	}
	if len(g.mints) != 2 {
		t.Fatalf("scoped tokens are never reused: %d mints for 2 requests", len(g.mints))
	}
}

func TestMintCredential_RepositoryOutsideTheInstallation(t *testing.T) {
	p, _, _ := seededProvider(t)
	tg := webTarget()
	tg.Repo.FullPath = "acme/not-selected" // GitHub answers 422 for it
	_, err := p.MintCredential(bg, gp.MintRequest{Target: tg, Access: gp.AccessRead})
	if codeOf(t, err) != gp.CodeNotFound {
		t.Fatalf("got %v, want not_found", err)
	}
}

func TestInstallationTokenIsCachedUntilNearExpiry(t *testing.T) {
	p, g, st := seededProvider(t)
	for i := 0; i < 3; i++ {
		if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.mints) != 1 {
		t.Fatalf("three API calls should share one installation token, minted %d", len(g.mints))
	}
	// A token about to expire is not served from the cache.
	st.cacheSet("inst:inst-1", "stale", time.Now().Add(time.Minute))
	if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"}); err != nil {
		t.Fatal(err)
	}
	if len(g.mints) != 2 {
		t.Fatalf("a near-expiry token must be replaced: %d mints", len(g.mints))
	}
}

func TestSuspendedInstallationIsRefused(t *testing.T) {
	p, g, st := seededProvider(t)
	st.installs[fakeAppRowID][0].Suspended = true
	_, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"})
	if codeOf(t, err) != gp.CodeForbidden || len(g.mints) != 0 {
		t.Fatalf("suspended: %v mints=%d", err, len(g.mints))
	}
	page, err := p.ListRepositories(bg, gp.ListReposRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("a suspended installation lists nothing: %+v %v", page, err)
	}
}

func TestErrorsAreClassifiedAndSafe(t *testing.T) {
	p, g, _ := seededProvider(t)
	pr := "GET /repos/acme/web/pulls/1"
	cases := []struct {
		name string
		resp fakeResponse
		want gp.ErrorCode
	}{
		{"rate limit via 403 message", fakeResponse{403, `{"message":"API rate limit exceeded for installation"}`, map[string]string{"Retry-After": "42"}}, gp.CodeRateLimited},
		{"secondary rate limit", fakeResponse{403, `{"message":"You have exceeded a secondary rate limit"}`, nil}, gp.CodeRateLimited},
		{"429", fakeResponse{429, `{}`, nil}, gp.CodeRateLimited},
		{"forbidden", fakeResponse{403, `{"message":"Resource not accessible by integration"}`, nil}, gp.CodeForbidden},
		{"bad credentials", fakeResponse{401, `{"message":"Bad credentials"}`, nil}, gp.CodeForbidden},
		{"server error", fakeResponse{502, `<html>oops</html>`, nil}, gp.CodeTransient},
		{"validation", fakeResponse{422, `{"message":"Validation Failed"}`, nil}, gp.CodeValidation},
		{"conflict", fakeResponse{409, `{"message":"conflict"}`, nil}, gp.CodeConflict},
		{"unreadable success body", fakeResponse{200, `not json`, nil}, gp.CodeInternal},
	}
	for _, c := range cases {
		g.forceError[pr] = c.resp
		_, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"})
		if codeOf(t, err) != c.want {
			t.Errorf("%s: got %v, want %s", c.name, err, c.want)
		}
		for _, secret := range []string{"ghs_token_", "BEGIN RSA", fakeWebhookSecret} {
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error leaks %q: %v", c.name, secret, err)
			}
		}
	}
	g.forceError[pr] = fakeResponse{403, `{"message":"API rate limit exceeded"}`, map[string]string{"Retry-After": "42"}}
	_, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"})
	if gp.AsError(err).RetryAfterSeconds != 42 {
		t.Errorf("retry-after = %d, want 42", gp.AsError(err).RetryAfterSeconds)
	}
}

func TestNetworkFailureIsTransient(t *testing.T) {
	p, _, _ := seededProvider(t)
	old := fetch
	defer func() { fetch = old }()
	fetch = func(string, string, map[string]string, string) (*plugin.FetchResponse, error) {
		return nil, errors.New("dial tcp: boom")
	}
	_, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"})
	if codeOf(t, err) != gp.CodeTransient {
		t.Fatalf("got %v", err)
	}
}

func TestNumbersAreValidatedBeforeTheyReachAURL(t *testing.T) {
	p, g, _ := seededProvider(t)
	for _, n := range []string{"", "1/../../x", "abc", "-1", "1?x=y"} {
		if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: n}); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("number %q: %v", n, err)
		}
	}
	bad := webTarget()
	bad.Repo.FullPath = "acme/web/../../admin"
	if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: bad, Number: "1"}); codeOf(t, err) != gp.CodeValidation {
		t.Error("a repository path with extra segments must be rejected")
	}
	if g.called("GET /repos") != 0 {
		t.Error("nothing should have reached GitHub")
	}
}

func TestParseRepoInput(t *testing.T) {
	ok := map[string][2]string{
		"acme/web":                           {"acme", "web"},
		"https://github.com/acme/web":        {"acme", "web"},
		"https://github.com/acme/web.git":    {"acme", "web"},
		"https://github.com/acme/web/":       {"acme", "web"},
		"https://github.com/acme/web/pull/3": {"acme", "web"},
		"github.com/acme/web":                {"acme", "web"},
		"git@github.com:acme/web.git":        {"acme", "web"},
		"  acme/web  ":                       {"acme", "web"},
	}
	for in, want := range ok {
		o, n, err := parseRepoInput(in)
		if err != nil || o != want[0] || n != want[1] {
			t.Errorf("%q → %s/%s %v", in, o, n, err)
		}
	}
	for _, in := range []string{"", "web", "https://gitlab.com/acme/web", "git@gitlab.com:acme/web.git", "/"} {
		if _, _, err := parseRepoInput(in); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("%q should be a validation error, got %v", in, err)
		}
	}
}

func TestListRepositoriesRejectsABadCursor(t *testing.T) {
	p, _, _ := seededProvider(t)
	for _, c := range []string{"x", "1", "a:b", "-1:1", "0:0"} {
		if _, err := p.ListRepositories(bg, gp.ListReposRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, Cursor: c}); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
}

func TestMentionsHandle(t *testing.T) {
	yes := []string{"@paca-bot", "hey @paca-bot, fix", "@Paca-Bot please", "(@paca-bot)", "line\n@paca-bot", "@paca-bot[bot] hi", "thanks @paca-bot."}
	no := []string{"", "paca-bot", "dev@paca-bot.example", "@paca-bot-2 hi", "@paca-botty", "x@paca-bot", "@@paca-bot", "@paca"}
	for _, s := range yes {
		if !mentionsHandle(s, "paca-bot") {
			t.Errorf("%q should mention", s)
		}
	}
	for _, s := range no {
		if mentionsHandle(s, "paca-bot") {
			t.Errorf("%q should not mention", s)
		}
	}
	if mentionsHandle("@anything", "") {
		t.Error("an empty handle mentions nobody")
	}
}

func TestCuttingKeepsUTF8Valid(t *testing.T) {
	s := "héllo wörld ✓ done"
	for max := 1; max < len(s); max++ {
		h, ht := cutHead(s, max)
		tl, tt := cutTail(s, max)
		if !utf8.ValidString(h) || !utf8.ValidString(tl) || len(h) > max || len(tl) > max || !ht || !tt {
			t.Fatalf("max=%d head=%q tail=%q", max, h, tl)
		}
	}
	if h, tr := cutHead(s, len(s)); h != s || tr {
		t.Error("no cut needed")
	}
	if h, tr := cutHead(s, 0); h != s || tr {
		t.Error("0 means no limit")
	}
}

func TestDiffSection(t *testing.T) {
	diff := "diff --git a/a.txt b/a.txt\n+one\ndiff --git a/dir/b.txt b/dir/b.txt\n+two\ndiff --git a/old.txt b/new.txt\nrename\n"
	if s, ok := diffSection(diff, "dir/b.txt"); !ok || !strings.Contains(s, "+two") || strings.Contains(s, "+one") || strings.Contains(s, "rename") {
		t.Errorf("dir/b.txt: %q %v", s, ok)
	}
	if s, ok := diffSection(diff, "new.txt"); !ok || !strings.Contains(s, "rename") {
		t.Errorf("renamed file: %q %v", s, ok)
	}
	if _, ok := diffSection(diff, "b.txt"); ok {
		t.Error("a suffix of another path must not match")
	}
}

// ── webhooks ─────────────────────────────────────────────────────────────────

func parseOne(t *testing.T, p *ghProvider, req gp.WebhookRequest) gp.Event {
	t.Helper()
	evs, err := p.VerifyAndParseWebhook(bg, req)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events=%d err=%v", len(evs), err)
	}
	return evs[0]
}

func TestWebhook_Details(t *testing.T) {
	p, _, _ := seededProvider(t)
	pull := func(action string, extra map[string]any) gp.WebhookRequest {
		pr := map[string]any{"number": 5, "title": "T", "body": "B", "user": userSender, "draft": true,
			"head": map[string]any{"ref": "fork-branch", "sha": "s", "repo": map[string]string{"full_name": "someone/web"}}, "base": map[string]any{"ref": "main"}}
		for k, v := range extra {
			pr[k] = v
		}
		return delivery("pull_request", "dx-"+action, map[string]any{"action": action, "repository": repoPayload, "sender": userSender, "pull_request": pr, "label": map[string]string{"name": "paca"}})
	}

	ev := parseOne(t, p, pull("opened", nil))
	if ev.Text != "T" || !ev.Subject.Draft || !ev.Subject.Head.FromFork || ev.Subject.Head.Repo != "someone/web" || ev.Subject.Base != "main" || ev.Subject.Author != "octocat" {
		t.Errorf("opened: %+v subject=%+v head=%+v", ev, ev.Subject, ev.Subject.Head)
	}
	if got := parseOne(t, p, pull("closed", map[string]any{"merged": true})); got.Action != "merged" {
		t.Errorf("a merged close is a merge: %q", got.Action)
	}
	if got := parseOne(t, p, pull("closed", nil)); got.Action != "closed" {
		t.Errorf("closed: %q", got.Action)
	}
	if got := parseOne(t, p, pull("synchronize", nil)); got.Action != "synchronized" {
		t.Errorf("synchronize: %q", got.Action)
	}
	if got := parseOne(t, p, pull("ready_for_review", nil)); got.Action != "ready" {
		t.Errorf("ready: %q", got.Action)
	}
	if got := parseOne(t, p, pull("labeled", nil)); got.Kind != gp.EventLabel || got.Label != "paca" {
		t.Errorf("labeled: %+v", got)
	}
	// Requests and assignments that do not name this app are dropped.
	for _, a := range []string{"unlabeled", "assigned", "review_requested", "locked"} {
		if evs, _ := p.VerifyAndParseWebhook(bg, pull(a, nil)); len(evs) != 0 {
			t.Errorf("%s should be dropped", a)
		}
	}
	// A pull request whose head repository was deleted can only have come from a fork.
	deleted := pull("opened", map[string]any{"head": map[string]any{"ref": "x", "sha": "s", "repo": nil}})
	if got := parseOne(t, p, deleted); !got.Subject.Head.FromFork {
		t.Error("deleted head repo must count as a fork")
	}

	// Edits do not re-address the bot, so editing a comment cannot re-trigger a run.
	edited := delivery("issue_comment", "de", map[string]any{"action": "edited", "repository": repoPayload, "sender": userSender,
		"issue": map[string]any{"number": 1}, "comment": map[string]any{"id": 1, "body": "@paca-bot again"}})
	if got := parseOne(t, p, edited); got.Action != "edited" || len(got.Addressed) != 0 {
		t.Errorf("edited: %+v", got)
	}
	// An issue body is the first message: opening an issue that mentions the bot addresses it.
	issue := delivery("issues", "di", map[string]any{"action": "opened", "repository": repoPayload, "sender": userSender,
		"issue": map[string]any{"number": 9, "title": "Crash", "body": "@paca-bot please look", "user": userSender}})
	if got := parseOne(t, p, issue); got.Kind != gp.EventComment || got.Action != "created" || got.Subject.Kind != gp.SubjectIssue || len(got.Addressed) != 1 || !strings.Contains(got.Text, "Crash") {
		t.Errorf("issue opened: %+v", got)
	}
	// Check conclusions are normalized.
	for in, want := range map[string]string{"success": "success", "neutral": "success", "skipped": "skipped", "cancelled": "cancelled", "timed_out": "failure", "action_required": "failure"} {
		chk := delivery("check_run", "dc-"+in, map[string]any{"action": "completed", "repository": repoPayload, "sender": userSender,
			"check_run": map[string]any{"id": 1, "name": "n", "head_sha": "s", "conclusion": in}})
		if got := parseOne(t, p, chk); got.Check.Conclusion != want || got.Check.RunID != "1" {
			t.Errorf("conclusion %s → %+v", in, got.Check)
		}
	}
	// An incomplete check run is not a result.
	running := delivery("check_run", "dr", map[string]any{"action": "completed", "repository": repoPayload, "sender": userSender, "check_run": map[string]any{"id": 1, "name": "n", "conclusion": nil}})
	if evs, _ := p.VerifyAndParseWebhook(bg, running); len(evs) != 0 {
		t.Error("a check run without a conclusion must be dropped")
	}
}

func TestWebhook_VerificationFailures(t *testing.T) {
	p, _, _ := seededProvider(t)
	good := delivery("ping", "x", map[string]any{"zen": "z"})

	noSig := good
	noSig.Headers = map[string]string{"x-github-event": "ping"}
	wrongSecret := delivery("ping", "x", map[string]any{"zen": "z"})
	wrongSecret.Headers["x-hub-signature-256"] = sign(wrongSecret.Body, "another-secret")
	tampered := good
	tampered.Body = append([]byte(nil), good.Body...)
	tampered.Body[2] = 'X'
	for name, req := range map[string]gp.WebhookRequest{"no signature": noSig, "wrong secret": wrongSecret, "tampered body": tampered} {
		evs, err := p.VerifyAndParseWebhook(bg, req)
		if codeOf(t, err) != gp.CodeInvalidSignature || len(evs) != 0 {
			t.Errorf("%s: %v %v", name, evs, err)
		}
		if err != nil && strings.Contains(err.Error(), fakeWebhookSecret) {
			t.Errorf("%s: the secret leaked into the error", name)
		}
	}
	unknown := delivery("ping", "x", map[string]any{})
	unknown.Identity.ID = "nope"
	if _, err := p.VerifyAndParseWebhook(bg, unknown); codeOf(t, err) != gp.CodeNotFound {
		t.Errorf("unknown identity: %v", err)
	}
	// A signed body that is not JSON is a validation error, not a panic.
	junk := gp.WebhookRequest{Identity: good.Identity, Body: []byte("not json"), Headers: map[string]string{"x-hub-signature-256": sign([]byte("not json"), fakeWebhookSecret), "x-github-event": "push"}}
	if _, err := p.VerifyAndParseWebhook(bg, junk); codeOf(t, err) != gp.CodeValidation {
		t.Errorf("junk: %v", err)
	}
	// Without a delivery id the event id is still deterministic.
	req := delivery("push", "", map[string]any{"ref": "refs/heads/x", "repository": repoPayload, "sender": userSender})
	delete(req.Headers, "x-github-delivery")
	a, b := parseOne(t, p, req), parseOne(t, p, req)
	if a.ID == "" || a.ID != b.ID {
		t.Errorf("ids %q %q", a.ID, b.ID)
	}
}

// ── identity setup ───────────────────────────────────────────────────────────

func beginReq() gp.BeginSetupRequest {
	return gp.BeginSetupRequest{Scope: gp.SetupShared, Host: githubHost, CallbackURL: "https://paca.example.com/plugin-pages/x/cb", WebhookURL: "https://paca.example.com/api/v1/git/webhooks/key1", ReturnURL: "https://paca.example.com/settings"}
}

func TestSetup_BeginBuildsTheManifest(t *testing.T) {
	p, _, st := seededProvider(t)
	req := beginReq()
	req.Params = map[string]string{"name": "Acme Paca", "owner_org": "acme", "public": "true"}
	step, err := p.BeginIdentitySetup(bg, req)
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != gp.StepFormPost || !strings.HasPrefix(step.URL, "https://github.com/organizations/acme/settings/apps/new?state=") || step.State == "" {
		t.Fatalf("step = %+v", step)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(step.Fields["manifest"]), &m); err != nil {
		t.Fatal(err)
	}
	hook := m["hook_attributes"].(map[string]any)
	if hook["url"] != req.WebhookURL || hook["active"] != true {
		t.Errorf("the webhook must go to core's endpoint: %v", hook)
	}
	if m["redirect_url"] != req.CallbackURL || m["setup_url"] != req.CallbackURL || m["name"] != "Acme Paca" || m["public"] != true || m["url"] != "https://paca.example.com" {
		t.Errorf("manifest = %v", m)
	}
	perms := m["default_permissions"].(map[string]any)
	if perms["checks"] != "write" || perms["contents"] != "write" {
		t.Errorf("permissions = %v", perms)
	}
	if s, err := st.loadSetup(step.State); err != nil || s.Scope != "shared" || !s.ExpiresAt.After(time.Now()) {
		t.Errorf("session = %+v %v", s, err)
	}
	// A local webhook URL is switched off rather than left failing.
	local := beginReq()
	local.WebhookURL = "http://localhost:8080/api/v1/git/webhooks/k"
	step, _ = p.BeginIdentitySetup(bg, local)
	_ = json.Unmarshal([]byte(step.Fields["manifest"]), &m)
	if m["hook_attributes"].(map[string]any)["active"] != false {
		t.Error("an unreachable webhook URL must be inactive")
	}
}

func TestSetup_BeginValidation(t *testing.T) {
	p, _, _ := seededProvider(t)
	mutate := map[string]func(*gp.BeginSetupRequest){
		"host":          func(r *gp.BeginSetupRequest) { r.Host = "gitlab.com" },
		"scope":         func(r *gp.BeginSetupRequest) { r.Scope = "everyone" },
		"callback":      func(r *gp.BeginSetupRequest) { r.CallbackURL = "" },
		"webhook":       func(r *gp.BeginSetupRequest) { r.WebhookURL = "" },
		"relative cb":   func(r *gp.BeginSetupRequest) { r.CallbackURL = "/relative" },
		"bad org":       func(r *gp.BeginSetupRequest) { r.Params = map[string]string{"owner_org": "-bad-"} },
		"name too long": func(r *gp.BeginSetupRequest) { r.Params = map[string]string{"name": strings.Repeat("x", 40)} },
	}
	for name, f := range mutate {
		r := beginReq()
		f(&r)
		if _, err := p.BeginIdentitySetup(bg, r); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("%s: %v", name, err)
		}
	}
	a := beginReq()
	a.Scope, a.AgentName = gp.SetupAgent, "Dev"
	step, err := p.BeginIdentitySetup(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(step.Fields["manifest"]), &m)
	if m["name"] != "Paca Dev" {
		t.Errorf("agent apps are named after the agent: %v", m["name"])
	}
}

func TestSetup_CreatesTheAppAndStopsThere(t *testing.T) {
	p, g, st := seededProvider(t)
	step, _ := p.BeginIdentitySetup(bg, beginReq())

	// The manifest code is the only input; an installation id alone is not a setup.
	if _, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: step.State, Params: map[string]string{"installation_id": "99"}}); codeOf(t, err) != gp.CodeValidation {
		t.Errorf("no code: %v", err)
	}
	id, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: step.State, Params: map[string]string{"code": "c0de"}})
	if err != nil {
		t.Fatal(err)
	}
	if id.Ref.ID == "" || id.Ref.Handle != fakeSlug || id.Kind != gp.IdentityApp || id.Host != githubHost || id.DisplayName != "Paca" {
		t.Fatalf("identity = %+v", id)
	}
	if id.Next != nil {
		t.Fatalf("the App is installed later, per account, when a project links a repository there; got a next step %+v", id.Next)
	}
	if len(st.installs[id.Ref.ID]) != 0 {
		t.Error("setup must not install the App anywhere")
	}
	stored := st.apps[id.Ref.ID]
	if stored.WebhookSecret != fakeWebhookSecret || !strings.Contains(stored.PrivateKeyPEM, "RSA PRIVATE KEY") {
		t.Errorf("credentials must be kept: %+v", stored.Slug)
	}
	if b, _ := json.Marshal(id); strings.Contains(string(b), "RSA PRIVATE KEY") || strings.Contains(string(b), fakeWebhookSecret) {
		t.Errorf("secrets must not appear in the identity returned to core: %s", b)
	}
	// The one-time code cannot be exchanged again, and the finished state is dead.
	if _, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: step.State, Params: map[string]string{"code": "c0de"}}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("replayed code: %v", err)
	}
	if g.called("POST /app-manifests/") != 1 {
		t.Errorf("the code was exchanged %d times", g.called("POST /app-manifests/"))
	}
}

func TestSetup_StateIsRequiredAndExpires(t *testing.T) {
	p, _, st := seededProvider(t)
	if _, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: "unknown", Params: map[string]string{"code": "c0de"}}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("unknown state: %v", err)
	}
	step, _ := p.BeginIdentitySetup(bg, beginReq())
	s := st.setups[step.State]
	s.ExpiresAt = time.Now().Add(-time.Minute)
	st.setups[step.State] = s
	if _, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: step.State, Params: map[string]string{"code": "c0de"}}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("expired state: %v", err)
	}
}

func TestSetup_AnAccessGrantStateCannotCreateAnApp(t *testing.T) {
	p, g, _ := seededProvider(t)
	grant, err := p.BeginAccessGrant(bg, gp.BeginAccessRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, CallbackURL: "https://paca.example.com/cb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{State: grant.State, Params: map[string]string{"code": "c0de"}}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("a state issued for an access grant must not complete an identity setup: %v", err)
	}
	if g.called("POST /app-manifests/") != 0 {
		t.Error("the manifest code must not be exchanged")
	}
}

func TestDeleteIdentity(t *testing.T) {
	p, _, st := seededProvider(t)
	if _, err := p.DeleteIdentity(bg, gp.DeleteIdentityRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}}); err != nil {
		t.Fatal(err)
	}
	if len(st.apps) != 0 {
		t.Error("app not removed")
	}
	if _, err := p.DeleteIdentity(bg, gp.DeleteIdentityRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}}); err != nil {
		t.Errorf("deleting twice must be fine: %v", err)
	}
}

// ── writes ───────────────────────────────────────────────────────────────────

func TestSetStatus_UsesTheCRHeadAndUpdatesByReference(t *testing.T) {
	p, g, _ := seededProvider(t)
	subj := gp.SubjectRef{Kind: gp.SubjectChangeRequest, Number: "1"}
	res, err := p.SetStatus(bg, gp.SetStatusRequest{Target: webTarget(), Subject: subj, State: gp.RunInProgress, Title: "Paca / dev"})
	if err != nil || res.StatusRef == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if g.called("GET /repos/acme/web/pulls/1") != 1 {
		t.Error("the head sha should come from the pull request")
	}
	if _, err := p.SetStatus(bg, gp.SetStatusRequest{Target: webTarget(), Subject: subj, State: gp.RunSuccess, Title: "Paca / dev", StatusRef: res.StatusRef}); err != nil {
		t.Fatal(err)
	}
	if g.called("PATCH /repos/acme/web/check-runs/"+res.StatusRef) != 1 {
		t.Errorf("calls: %v", g.calls)
	}
	for name, req := range map[string]gp.SetStatusRequest{
		"no title":  {Target: webTarget(), Subject: subj, State: gp.RunQueued},
		"bad state": {Target: webTarget(), Subject: subj, State: "weird", Title: "t"},
		"no sha":    {Target: webTarget(), State: gp.RunQueued, Title: "t"},
		"bad ref":   {Target: webTarget(), Subject: subj, State: gp.RunQueued, Title: "t", StatusRef: "../x"},
	} {
		if _, err := p.SetStatus(bg, req); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOpenChangeRequest_ReportsWhatWasApplied(t *testing.T) {
	p, _, _ := seededProvider(t)
	cr, err := p.OpenChangeRequest(bg, gp.OpenCRRequest{Target: webTarget(), Title: "T", Head: "feat/z", Base: "main", Labels: []string{"paca"}, Reviewers: []string{"reader"}})
	if err != nil {
		t.Fatal(err)
	}
	if cr.State != gp.CROpen || len(cr.Labels) != 1 || len(cr.Reviewers) != 1 || cr.Head.Ref != "feat/z" {
		t.Errorf("cr = %+v", cr)
	}
}

func TestSubmitReview_MapsVerdictAndSides(t *testing.T) {
	p, g, _ := seededProvider(t)
	_ = g
	if _, err := p.SubmitReview(bg, gp.SubmitReviewRequest{Target: webTarget(), Number: "1", Verdict: gp.VerdictRequestChanges, Summary: "no",
		Comments: []gp.InlineComment{{Path: "src/main.go", Line: 2, Side: "old", Body: "x"}}}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]gp.InlineComment{"no line": {Path: "a", Body: "x"}, "no body": {Path: "a", Line: 1}, "no path": {Line: 1, Body: "x"}} {
		_, err := p.SubmitReview(bg, gp.SubmitReviewRequest{Target: webTarget(), Number: "1", Verdict: gp.VerdictComment, Comments: []gp.InlineComment{c}})
		if codeOf(t, err) != gp.CodeValidation {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGetChecks_StatesAndIds(t *testing.T) {
	p, _, _ := seededProvider(t)
	checks, err := p.GetChecks(bg, gp.ChecksRequest{Target: webTarget(), Ref: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]gp.CheckState{}
	for _, c := range checks {
		got[c.ID] = c.State
	}
	want := map[string]gp.CheckState{"run:5001": gp.CheckFailure, "run:5002": gp.CheckSuccess, "run:5003": gp.CheckPending, "status:legacy/ci": gp.CheckSuccess}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("%s = %q, want %q (all: %v)", id, got[id], st, got)
		}
	}
	// A running check has no job log; its reported output is the fallback.
	logs, err := p.GetCheckLogs(bg, gp.CheckLogsRequest{Target: webTarget(), CheckID: "run:5003"})
	if err != nil || !strings.Contains(logs.Text, "still going") {
		t.Errorf("fallback logs: %+v %v", logs, err)
	}
	for _, id := range []string{"status:legacy/ci", "run:abc", "garbage"} {
		if _, err := p.GetCheckLogs(bg, gp.CheckLogsRequest{Target: webTarget(), CheckID: id}); codeOf(t, err) != gp.CodeNotFound {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestGetBranchProtection(t *testing.T) {
	p, _, _ := seededProvider(t)
	bp, err := p.GetBranchProtection(bg, gp.BranchProtectionRequest{Target: webTarget(), Branch: "main"})
	if err != nil || !bp.Protected || !bp.RequiresStatusChecks {
		t.Errorf("main: %+v %v", bp, err)
	}
	bp, err = p.GetBranchProtection(bg, gp.BranchProtectionRequest{Target: webTarget(), Branch: "scratch"})
	if err != nil || bp.Protected {
		t.Errorf("an unknown branch is not protected: %+v %v", bp, err)
	}
	if _, err := p.GetBranchProtection(bg, gp.BranchProtectionRequest{Target: webTarget()}); codeOf(t, err) != gp.CodeValidation {
		t.Error("branch required")
	}
}

func TestJWTIsShortLivedAndSignedByTheAppKey(t *testing.T) {
	p, g, _ := seededProvider(t)
	// The fake rejects an App call whose JWT is badly signed, for the wrong
	// issuer, or lives longer than GitHub's 10 minute cap, so a successful mint
	// proves all three.
	if _, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: gp.AccessRead}); err != nil {
		t.Fatal(err)
	}
	p.store.(*memStore).apps[fakeAppRowID] = func() appRecord { a := p.store.(*memStore).apps[fakeAppRowID]; a.GitHubAppID = 1; return a }()
	_ = g
	if _, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: gp.AccessRead}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("wrong issuer must be rejected by GitHub: %v", err)
	}
}

// ── wire ─────────────────────────────────────────────────────────────────────

func TestSPIRoutesAreMountedOnTheInternalPrefix(t *testing.T) {
	g := newFakeGitHub(t)
	_ = g
	tc := plugintest.NewContext(t)
	var pl githubPlugin
	if err := pl.Init(tc.PluginContext()); err != nil {
		t.Fatal(err)
	}
	res := tc.Call("POST", "/_git/:op", plugintest.Request{PathParams: map[string]string{"op": "describe"}})
	if res.StatusCode != 200 {
		t.Fatalf("describe: %d %s", res.StatusCode, res.BodyString())
	}
	var env gp.Envelope
	if err := json.Unmarshal(res.Body, &env); err != nil || !env.OK {
		t.Fatalf("envelope: %v %s", err, res.BodyString())
	}
	var info gp.Info
	if err := json.Unmarshal(env.Result, &info); err != nil || info.ID != githubPluginID || info.SPIVersion != gp.SPIVersion {
		t.Fatalf("info: %+v %v", info, err)
	}
	res = tc.Call("POST", "/_git/:op", plugintest.Request{PathParams: map[string]string{"op": "mint_credential"}, Body: []byte(`{"target":{"identity":{"id":"nope"},"repo":{"full_path":"a/b"}},"access":"read"}`)})
	if res.StatusCode != gp.CodeNotFound.HTTPStatus() || !strings.Contains(res.BodyString(), `"not_found"`) {
		t.Fatalf("unknown identity over the wire: %d %s", res.StatusCode, res.BodyString())
	}
}

func TestBranches(t *testing.T) {
	p, g, _ := seededProvider(t)
	bm := gp.BranchManager(p)
	tg := webTarget()
	tg.Repo.DefaultBranch = "main"

	got, err := bm.GetBranch(bg, gp.GetBranchRequest{Target: tg, Name: "feat/PACA-12-login"})
	if err != nil || got.SHA != "abc123" || got.URL != "https://github.com/acme/web/tree/feat/PACA-12-login" {
		t.Fatalf("GetBranch: %+v %v", got, err)
	}
	if _, err := bm.GetBranch(bg, gp.GetBranchRequest{Target: tg, Name: "nope"}); codeOf(t, err) != gp.CodeNotFound {
		t.Fatalf("missing branch: %v", err)
	}
	if g.called("GET /repos/acme/web/branches/feat/PACA-12-login") != 1 {
		t.Errorf("a slash in a branch name must stay a path separator: %v", g.calls)
	}

	made, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: tg, Name: "paca/PACA-1"})
	if err != nil || made.SHA != "sha-main" || made.Name != "paca/PACA-1" {
		t.Fatalf("CreateBranch from the default: %+v %v", made, err)
	}
	if g.branches["paca/PACA-1"] != "sha-main" {
		t.Errorf("the ref was not created at the source head: %v", g.branches)
	}
	fromOther, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: tg, Name: "paca/PACA-2", From: "feat/PACA-12-login"})
	if err != nil || fromOther.SHA != "abc123" {
		t.Fatalf("CreateBranch from another branch: %+v %v", fromOther, err)
	}
	if _, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: tg, Name: "paca/PACA-1"}); codeOf(t, err) != gp.CodeConflict {
		t.Fatalf("a taken name must be a conflict: %v", err)
	}
	if _, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: tg, Name: "paca/x", From: "gone"}); codeOf(t, err) != gp.CodeNotFound {
		t.Fatalf("a missing source must be not_found: %v", err)
	}
	noDefault := webTarget()
	if _, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: noDefault, Name: "paca/x"}); codeOf(t, err) != gp.CodeValidation {
		t.Fatalf("no source and no default: %v", err)
	}
	for _, bad := range []string{"", "has space", "a..b", "-flag", "x.lock", "a//b", "tilde~", "/lead", "trail/", "a@{b"} {
		if _, err := bm.CreateBranch(bg, gp.CreateBranchRequest{Target: tg, Name: bad}); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("%q must be refused as validation: %v", bad, err)
		}
		if _, err := bm.GetBranch(bg, gp.GetBranchRequest{Target: tg, Name: bad}); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("get %q must be refused as validation: %v", bad, err)
		}
	}
}

func TestMintCredential_ReportsTheRequestedLifetimeWhenShorter(t *testing.T) {
	p, _, _ := seededProvider(t)
	until := func(c gp.Credential) time.Duration { return time.Until(c.ExpiresAt) }

	short, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: gp.AccessRead, TTLSeconds: 300})
	if err != nil || until(short) > 5*time.Minute || until(short) < 4*time.Minute {
		t.Fatalf("a requested lifetime caps ExpiresAt: %v %v", until(short), err)
	}
	unspecified, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: gp.AccessRead})
	if err != nil || until(unspecified) < 30*time.Minute {
		t.Fatalf("without a request the token's own expiry is reported: %v %v", until(unspecified), err)
	}
	long, err := p.MintCredential(bg, gp.MintRequest{Target: webTarget(), Access: gp.AccessRead, TTLSeconds: 7 * 24 * 3600})
	if err != nil || until(long) > 61*time.Minute {
		t.Fatalf("a longer request cannot extend a token: %v %v", until(long), err)
	}
}

// An App only receives the events its manifest subscribes to, so every
// delivery the adapter understands must be listed there.
func TestManifest_SubscribesToEveryEventTheAdapterHandles(t *testing.T) {
	have := map[string]bool{}
	for _, e := range manifestEvents {
		have[e] = true
	}
	for _, e := range []string{"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "check_run", "push"} {
		if !have[e] {
			t.Errorf("the manifest does not subscribe to %q", e)
		}
	}
}

func TestIsLocalURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:8080/x": true, "http://127.0.0.1/x": true, "http://[::1]/x": true,
		"http://10.1.2.3/x": true, "http://192.168.0.5/x": true, "http://172.16.0.1/x": true,
		"http://169.254.169.254/x": true, "http://100.64.1.1/x": true, "http://[fd00::1]/x": true,
		"http://paca.lan/x": true, "https://paca.example.com/x": false, "http://172.32.0.1/x": false,
		"http://8.8.8.8/x": false, "http://100.128.0.1/x": false,
	} {
		if got := isLocalURL(url); got != want {
			t.Errorf("isLocalURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestClaimSetupCodeIsSingleUse(t *testing.T) {
	st := newMemStore()
	_ = st.saveSetup(setupSession{State: "s"})
	if ok, err := st.claimSetupCode("s"); !ok || err != nil {
		t.Fatalf("first claim = %v %v", ok, err)
	}
	if ok, _ := st.claimSetupCode("s"); ok {
		t.Error("second claim succeeded")
	}
}
