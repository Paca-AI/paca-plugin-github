package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

const (
	patSecret    = "ghp_fake_personal_access_token"
	patHookKey   = "a-webhook-secret-of-16+chars"
	patHookURL   = "https://paca.example.com/api/v1/git/webhooks/key1"
	patLogin     = "paca-machine"
	patIdentity  = "token-row-1"
	patRepoSHA   = "feedbeef"
	patCheckName = "Paca review"
)

// fakeUserGitHub is a GitHub that knows one personal access token and one
// repository, enough to drive the token identity paths.
type fakeUserGitHub struct {
	t     *testing.T
	mu    sync.Mutex
	calls []string
	hooks map[int64]map[string]any
	next  int64
	// statuses are the bodies posted to /statuses/<sha>.
	statuses []map[string]any
	expiry   string
}

func newFakeUserGitHub(t *testing.T) *fakeUserGitHub {
	t.Helper()
	g := &fakeUserGitHub{t: t, hooks: map[int64]map[string]any{}, next: 700}
	old := fetch
	fetch = g.fetch
	t.Cleanup(func() { fetch = old })
	return g
}

func (g *fakeUserGitHub) called(prefix string) int {
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

func (g *fakeUserGitHub) fetch(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	u, _ := url.Parse(rawURL)
	if u.Host != "api.github.com" {
		g.t.Errorf("request outside the allow-list: %s", rawURL)
		return nil, fmt.Errorf("blocked")
	}
	path := u.Path
	g.calls = append(g.calls, method+" "+path)
	if headers["Authorization"] != "Bearer "+patSecret {
		return resp(401, map[string]string{"message": "Bad credentials"})
	}
	var payload map[string]any
	if body != "" {
		_ = json.Unmarshal([]byte(body), &payload)
	}
	switch {
	case method == "GET" && path == "/user":
		r, _ := resp(200, map[string]any{"login": patLogin, "name": "Paca Machine", "type": "User"})
		if g.expiry != "" {
			r.Headers["github-authentication-token-expiration"] = g.expiry
		}
		return r, nil
	case method == "GET" && path == "/user/repos":
		return resp(200, []map[string]any{{"id": 1, "full_name": "acme/web", "default_branch": "main", "private": true}})
	case method == "GET" && path == "/repos/acme/web":
		return resp(200, map[string]any{"id": 1, "full_name": "acme/web", "default_branch": "main", "private": true})
	case method == "GET" && path == "/repos/acme/web/hooks":
		var out []map[string]any
		for id, h := range g.hooks {
			out = append(out, map[string]any{"id": id, "active": h["active"], "config": h["config"]})
		}
		return resp(200, out)
	case method == "POST" && path == "/repos/acme/web/hooks":
		g.next++
		g.hooks[g.next] = payload
		return resp(201, map[string]any{"id": g.next})
	case strings.HasPrefix(path, "/repos/acme/web/hooks/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/repos/acme/web/hooks/"), 10, 64)
		if _, ok := g.hooks[id]; !ok {
			return notFound()
		}
		if method == "DELETE" {
			delete(g.hooks, id)
			return text(204, "")
		}
		g.hooks[id] = payload
		return resp(200, map[string]any{"id": id})
	case method == "POST" && path == "/repos/acme/web/statuses/"+patRepoSHA:
		g.statuses = append(g.statuses, payload)
		return resp(201, map[string]any{"id": 1})
	}
	return notFound()
}

func tokenProvider(t *testing.T) (*ghProvider, *fakeUserGitHub, *memStore) {
	t.Helper()
	g := newFakeUserGitHub(t)
	st := newMemStore()
	st.tokens[patIdentity] = tokenRecord{ID: patIdentity, Login: patLogin, Token: patSecret, WebhookSecret: patHookKey}
	return newGHProvider(st), g, st
}

func tokenTarget() gp.Target {
	return gp.Target{Identity: gp.IdentityRef{ID: patIdentity, Handle: patLogin}, Repo: gp.RepoRef{Host: githubHost, FullPath: "acme/web"}}
}

func beginToken(t *testing.T, p *ghProvider) gp.SetupStep {
	t.Helper()
	step, err := p.BeginIdentitySetup(bg, gp.BeginSetupRequest{
		Scope: gp.SetupShared, Host: githubHost, WebhookURL: patHookURL, CallbackURL: "https://paca.example.com/git/setup/callback",
		Params: map[string]string{"kind": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return step
}

func TestTokenSetup_FormThenValidatedCompletion(t *testing.T) {
	p, g, st := tokenProvider(t)
	step := beginToken(t, p)
	if step.Kind != gp.StepTokenForm || step.State == "" || len(step.Form) != 1 || step.Form[0].Name != fieldToken {
		t.Fatalf("step = %+v", step)
	}
	if strings.Contains(step.Prompt, patHookURL) {
		t.Error("Paca registers the webhook itself: the prompt must not send the user to do it by hand")
	}
	if s, _ := st.loadSetup(step.State); s.Kind != kindToken {
		t.Fatalf("the session must remember it is a token setup: %+v", s)
	}

	complete := func(token, secret string) (gp.Identity, error) {
		return p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{Scope: gp.SetupShared, Host: githubHost, State: step.State,
			Params: map[string]string{"token": token, "webhook_secret": secret}})
	}
	if _, err := complete(patSecret, "short"); !hasCode(err, gp.CodeValidation) {
		t.Errorf("short webhook secret: %v", err)
	}
	if _, err := complete("", patHookKey); !hasCode(err, gp.CodeValidation) {
		t.Errorf("missing token: %v", err)
	}
	// A mistyped token is a validation error and does not spend the state.
	if _, err := complete("ghp_wrong", patHookKey); !hasCode(err, gp.CodeValidation) {
		t.Fatalf("rejected token: %v", err)
	}
	if len(st.tokens) != 1 {
		t.Fatal("nothing may be stored for a token GitHub rejected")
	}
	g.expiry = "2030-01-02 03:04:05 UTC"
	ident, err := complete(patSecret, patHookKey)
	if err != nil {
		t.Fatalf("retry with the right token: %v", err)
	}
	if ident.Kind != gp.IdentityToken || ident.Ref.Handle != patLogin || ident.Host != githubHost || ident.Ref.ID == "" {
		t.Fatalf("identity = %+v", ident)
	}
	if ident.ExpiresAt.Year() != 2030 {
		t.Errorf("expiry = %v, want the one GitHub reports", ident.ExpiresAt)
	}
	stored, err := st.loadToken(ident.Ref.ID)
	if err != nil || stored.Token != patSecret || stored.WebhookSecret != patHookKey || stored.Login != patLogin {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	if strings.Contains(fmt.Sprint(ident), patSecret) {
		t.Error("the identity must not carry the token")
	}
	// Finished: the same state cannot create a second identity.
	if _, err := complete(patSecret, patHookKey); !hasCode(err, gp.CodeForbidden) {
		t.Errorf("reusing the state: %v", err)
	}
}

func TestTokenIdentity_ResolveCredentialAndList(t *testing.T) {
	p, _, _ := tokenProvider(t)
	res, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{Host: githubHost, PathOrURL: "https://github.com/acme/web",
		Candidates: []gp.IdentityRef{{ID: "no-such-identity"}, {ID: patIdentity}}})
	if err != nil || res.Identity.ID != patIdentity || res.Repository.Ref.FullPath != "acme/web" || res.Repository.Ref.ExternalID != "1" {
		t.Fatalf("resolve = %+v %v", res, err)
	}
	if _, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{Host: githubHost, PathOrURL: "acme/hidden", Candidates: []gp.IdentityRef{{ID: patIdentity}}}); !hasCode(err, gp.CodeNotFound) {
		t.Errorf("a repository the token cannot see: %v", err)
	}

	for _, access := range []gp.Access{gp.AccessRead, gp.AccessWrite} {
		c, err := p.MintCredential(bg, gp.MintRequest{Target: tokenTarget(), Access: access, TTLSeconds: 60})
		if err != nil || c.Secret != patSecret || c.Username == "" || !c.ExpiresAt.IsZero() {
			t.Errorf("%s credential = %+v %v: static, no expiry", access, c, err)
		}
	}
	if _, err := p.MintCredential(bg, gp.MintRequest{Target: tokenTarget(), Access: "admin"}); !hasCode(err, gp.CodeValidation) {
		t.Errorf("bad access: %v", err)
	}

	page, err := p.ListRepositories(bg, gp.ListReposRequest{Identity: gp.IdentityRef{ID: patIdentity}, PageSize: 30})
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref.FullPath != "acme/web" || page.NextCursor != "" {
		t.Fatalf("list = %+v %v", page, err)
	}
}

func TestTokenIdentity_WebhookManagement(t *testing.T) {
	p, g, _ := tokenProvider(t)
	req := gp.EnsureWebhookRequest{Target: tokenTarget(), URL: patHookURL}
	a, err := p.EnsureWebhook(bg, req)
	if err != nil || a.ID == "" {
		t.Fatalf("ensure: %+v %v", a, err)
	}
	h := g.hooks[mustInt(t, a.ID)]
	cfg, _ := h["config"].(map[string]any)
	if cfg["url"] != patHookURL || cfg["secret"] != patHookKey || cfg["content_type"] != "json" {
		t.Fatalf("hook config = %+v", cfg)
	}
	b, err := p.EnsureWebhook(bg, req)
	if err != nil || b.ID != a.ID || len(g.hooks) != 1 {
		t.Fatalf("ensure again must update the same hook: %+v %v hooks=%d", b, err, len(g.hooks))
	}
	if _, err := p.RemoveWebhook(bg, gp.RemoveWebhookRequest{Target: tokenTarget(), Webhook: a}); err != nil || len(g.hooks) != 0 {
		t.Fatalf("remove: %v hooks=%d", err, len(g.hooks))
	}
	if _, err := p.RemoveWebhook(bg, gp.RemoveWebhookRequest{Target: tokenTarget(), Webhook: a}); err != nil {
		t.Errorf("removing it again: %v", err)
	}
	if _, err := p.RemoveWebhook(bg, gp.RemoveWebhookRequest{Target: tokenTarget(), Webhook: gp.WebhookRef{ID: "../x"}}); !hasCode(err, gp.CodeValidation) {
		t.Errorf("a hook id that is not a number: %v", err)
	}

	// An App's webhook is app-level: there is nothing to register per repository.
	pa, _, _ := seededProvider(t)
	if _, err := pa.EnsureWebhook(bg, gp.EnsureWebhookRequest{Target: webTarget(), URL: patHookURL}); !hasCode(err, gp.CodeUnsupported) {
		t.Errorf("app identity: %v", err)
	}
}

func tokenDelivery(event, id string, payload map[string]any, secret string) gp.WebhookRequest {
	body, _ := json.Marshal(payload)
	return gp.WebhookRequest{
		Identity: gp.IdentityRef{ID: patIdentity, Handle: patLogin}, Method: "POST", Body: body,
		Headers: map[string]string{"x-github-event": event, "x-github-delivery": id, "x-hub-signature-256": sign(body, secret)},
	}
}

func TestTokenIdentity_WebhookIsVerifiedWithItsOwnSecretAndAddressedByLogin(t *testing.T) {
	p, _, _ := tokenProvider(t)
	comment := map[string]any{"action": "created", "repository": repoPayload, "sender": userSender,
		"issue":   map[string]any{"number": 1, "title": "Add login", "user": userSender, "pull_request": map[string]any{"url": "x"}},
		"comment": map[string]any{"id": 11, "body": "@" + patLogin + " please fix the test"}}

	evs, err := p.VerifyAndParseWebhook(bg, tokenDelivery("issue_comment", "d1", comment, patHookKey))
	if err != nil || len(evs) != 1 || len(evs[0].Addressed) != 1 || evs[0].Addressed[0].IdentityID != patIdentity || evs[0].Addressed[0].Via != gp.ViaMention {
		t.Fatalf("mention = %+v %v", evs, err)
	}
	// The App's secret does not verify a token identity's deliveries.
	if _, err := p.VerifyAndParseWebhook(bg, tokenDelivery("issue_comment", "d2", comment, fakeWebhookSecret)); !hasCode(err, gp.CodeInvalidSignature) {
		t.Errorf("wrong secret: %v", err)
	}

	// A review requested from the token's user is addressed to it; the user's
	// login has no "[bot]" suffix, unlike an App's account.
	rr := func(login string) gp.WebhookRequest {
		return tokenDelivery("pull_request", "d3-"+login, map[string]any{"action": "review_requested", "repository": repoPayload, "sender": userSender,
			"requested_reviewer": map[string]any{"login": login, "type": "User"},
			"pull_request": map[string]any{"number": 4, "title": "Add x", "user": userSender,
				"head": map[string]any{"ref": "x", "sha": "s1", "repo": map[string]string{"full_name": "acme/web"}}, "base": map[string]any{"ref": "main"}}}, patHookKey)
	}
	if evs, err := p.VerifyAndParseWebhook(bg, rr(patLogin)); err != nil || len(evs) != 1 || evs[0].Addressed[0].Via != gp.ViaReviewRequest {
		t.Errorf("review requested from the token's user: %+v %v", evs, err)
	}
	if evs, err := p.VerifyAndParseWebhook(bg, rr("someone-else")); err != nil || len(evs) != 0 {
		t.Errorf("review requested from somebody else: %+v %v", evs, err)
	}
}

func TestTokenIdentity_StatusIsACommitStatus(t *testing.T) {
	p, g, _ := tokenProvider(t)
	res, err := p.SetStatus(bg, gp.SetStatusRequest{Target: tokenTarget(), Title: patCheckName, State: gp.RunInProgress, SHA: patRepoSHA, Summary: "working", URL: "https://paca.example.com/r/1"})
	if err != nil || res.StatusRef != patRepoSHA {
		t.Fatalf("status = %+v %v", res, err)
	}
	if g.called("POST /repos/acme/web/check-runs") != 0 {
		t.Fatal("a token cannot create check runs")
	}
	// A later call updates through the ref, which is the commit.
	if _, err := p.SetStatus(bg, gp.SetStatusRequest{Target: tokenTarget(), Title: patCheckName, State: gp.RunFailure, StatusRef: res.StatusRef, Summary: strings.Repeat("é", 300)}); err != nil {
		t.Fatal(err)
	}
	if len(g.statuses) != 2 || g.statuses[0]["state"] != "pending" || g.statuses[0]["context"] != patCheckName || g.statuses[0]["target_url"] != "https://paca.example.com/r/1" {
		t.Fatalf("statuses = %+v", g.statuses)
	}
	last := g.statuses[1]
	if last["state"] != "failure" || len([]rune(last["description"].(string))) > 140 {
		t.Errorf("failure status = %+v: GitHub caps descriptions at 140 characters", last)
	}
}

func TestTokenIdentity_DeleteForgetsTheToken(t *testing.T) {
	p, _, st := tokenProvider(t)
	if _, err := p.DeleteIdentity(bg, gp.DeleteIdentityRequest{Identity: gp.IdentityRef{ID: patIdentity}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.tokens[patIdentity]; ok {
		t.Fatal("the token should be gone")
	}
	if _, err := p.DeleteIdentity(bg, gp.DeleteIdentityRequest{Identity: gp.IdentityRef{ID: patIdentity}}); err != nil {
		t.Errorf("deleting it again: %v", err)
	}
	if _, err := p.MintCredential(bg, gp.MintRequest{Target: tokenTarget(), Access: gp.AccessRead}); !hasCode(err, gp.CodeNotFound) {
		t.Errorf("credential for a deleted identity: %v", err)
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// hasCode reports whether err is a provider error with the given code.
func hasCode(err error, code gp.ErrorCode) bool {
	return err != nil && gp.AsError(err).Code == code
}

// A token migrated from the previous release has no login and no webhook secret:
// the first use asks GitHub who owns it and generates the secret, once.
func TestTokenIdentity_MigratedTokenIsCompletedOnFirstUse(t *testing.T) {
	p, g, st := tokenProvider(t)
	st.tokens["legacy"] = tokenRecord{ID: "legacy", Token: patSecret}
	tg := gp.Target{Identity: gp.IdentityRef{ID: "legacy"}, Repo: gp.RepoRef{Host: githubHost, FullPath: "acme/web"}}

	if _, err := p.EnsureWebhook(bg, gp.EnsureWebhookRequest{Target: tg, URL: patHookURL}); err != nil {
		t.Fatal(err)
	}
	got := st.tokens["legacy"]
	if got.Login != patLogin || len(got.WebhookSecret) < minWebhookSecretLen {
		t.Fatalf("token after first use = %+v", got)
	}
	for _, h := range g.hooks {
		if cfg, _ := h["config"].(map[string]any); cfg["secret"] != got.WebhookSecret {
			t.Errorf("the hook must be signed with the stored secret: %+v", cfg)
		}
	}
	secret := got.WebhookSecret
	if _, err := p.EnsureWebhook(bg, gp.EnsureWebhookRequest{Target: tg, URL: patHookURL}); err != nil {
		t.Fatal(err)
	}
	if st.tokens["legacy"].WebhookSecret != secret || g.called("GET /user") != 1 {
		t.Error("completion must happen once")
	}
}

// Leaving the webhook secret empty is fine: the provider generates one.
func TestTokenSetup_GeneratesTheWebhookSecretWhenLeftEmpty(t *testing.T) {
	p, _, st := tokenProvider(t)
	step := beginToken(t, p)
	ident, err := p.CompleteIdentitySetup(bg, gp.CompleteSetupRequest{Scope: gp.SetupShared, Host: githubHost, State: step.State,
		Params: map[string]string{"token": patSecret}})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.tokens[ident.Ref.ID].WebhookSecret; len(got) < minWebhookSecretLen {
		t.Fatalf("generated secret = %q", got)
	}
}
