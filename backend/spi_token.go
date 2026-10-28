package main

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// Personal access token identities. They exist for people who cannot or do not
// want to create a GitHub App: the user pastes a token and a webhook secret, the
// provider checks the token against GitHub and keeps it encrypted. Differences
// from an App identity:
//
//   - the credential is static: git and the API use the token itself, so it
//     cannot be narrowed to one repository or to read-only;
//   - there is no installation: the token sees whatever its user can see, and no
//     access grant is needed;
//   - events arrive through a webhook registered on each linked repository
//     (EnsureWebhook), signed with the secret the user chose;
//   - the identity is a person's account, so GitHub shows its actions as that
//     person. A dedicated machine user is the recommended owner of the token.

const (
	kindApp   = "app"
	kindToken = "token"

	fieldToken         = "token"
	fieldWebhookSecret = "webhook_secret"

	// minWebhookSecretLen keeps a guessable secret from guarding the webhook
	// endpoint: anyone who knows the secret can forge a delivery.
	minWebhookSecretLen = 16

	tokenCredUsername = "x-access-token"
)

// ghIdentity is whatever an IdentityRef.ID names: an App or a token.
type ghIdentity struct {
	app   *appRecord
	token *tokenRecord
}

// loadIdentity finds the App or the token behind an identity id.
func (p *ghProvider) loadIdentity(id string) (ghIdentity, error) {
	app, err := p.loadApp(id)
	if err == nil {
		return ghIdentity{app: &app}, nil
	}
	if gp.AsError(err).Code != gp.CodeNotFound {
		return ghIdentity{}, err
	}
	tok, terr := p.store.loadToken(id)
	switch terr {
	case nil:
		if tok.Login == "" || tok.WebhookSecret == "" {
			if err := p.completeMigratedToken(&tok); err != nil {
				return ghIdentity{}, err
			}
		}
		return ghIdentity{token: &tok}, nil
	case errNoSuchToken:
		return ghIdentity{}, err
	}
	return ghIdentity{}, gp.Errorf(gp.CodeInternal, "could not load the GitHub identity")
}

// completeMigratedToken fills in what a token carried over from the previous
// release lacks: the owner's login (asked of GitHub) and a webhook secret
// (generated; the old per-repository secrets belonged to hooks that pointed at
// the old plugin). It runs on first use and stores the result.
func (p *ghProvider) completeMigratedToken(t *tokenRecord) error {
	if t.Login == "" {
		var u ghAuthUser
		if err := (&ghAPI{token: t.Token}).get("/user", &u); err != nil {
			return err
		}
		if u.Login == "" {
			return gp.Errorf(gp.CodeInternal, "GitHub returned an incomplete user")
		}
		t.Login, t.Name = u.Login, u.Name
	}
	if t.WebhookSecret == "" {
		secret, err := randomHex(32)
		if err != nil {
			return gp.Errorf(gp.CodeInternal, "could not generate a webhook secret")
		}
		t.WebhookSecret = secret
	}
	if err := p.store.updateToken(*t); err != nil {
		return gp.Errorf(gp.CodeInternal, "could not store the token")
	}
	// updateToken only fills blanks, so a concurrent first use may have stored a
	// different secret first: read back what won, or hooks signed with ours fail.
	won, err := p.store.loadToken(t.ID)
	if err != nil {
		return gp.Errorf(gp.CodeInternal, "could not store the token")
	}
	*t = won
	return nil
}

// loadTokenIdentity is loadIdentity for operations that only a token supports.
func (p *ghProvider) loadTokenIdentity(id string) (tokenRecord, error) {
	ident, err := p.loadIdentity(id)
	if err != nil {
		return tokenRecord{}, err
	}
	if ident.token == nil {
		return tokenRecord{}, gp.Errorf(gp.CodeUnsupported, "this identity is a GitHub App: its webhook is managed by the App")
	}
	return *ident.token, nil
}

// asBotRecord lets the webhook parser treat a token like an App: it addresses
// the identity by the token owner's login and compares users by that login.
func (t tokenRecord) asBotRecord() appRecord {
	return appRecord{ID: t.ID, Slug: t.Login, IsUser: true}
}

func (p *ghProvider) beginTokenSetup(req gp.BeginSetupRequest) (gp.SetupStep, error) {
	if req.WebhookURL == "" {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "webhook_url is required")
	}
	state, err := randomHex(24)
	if err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not generate state")
	}
	if err := p.store.saveSetup(setupSession{
		State: state, Scope: string(req.Scope), AgentID: req.AgentID, Kind: kindToken, ExpiresAt: p.now().Add(setupSessionLife),
	}); err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not start setup")
	}
	prompt := "Create a personal access token on GitHub, ideally on a dedicated machine-user account, and paste it below. " +
		"Fine-grained token: pick the repositories and grant Contents, Pull requests, Issues, Commit statuses and Webhooks (read and write) plus Metadata (read). " +
		"Classic token: the repo and admin:repo_hook scopes. " +
		"Paca registers the webhook on each repository you link, which is why the token needs the webhook permission."
	return gp.SetupStep{
		Kind:   gp.StepTokenForm,
		Prompt: prompt,
		State:  state,
		Form: []gp.FormField{
			{Name: fieldToken, Label: "Personal access token", Secret: true, Required: true,
				Help: "Used for every API call and for git clone, fetch and push. It acts as its owner on GitHub."},
		},
	}, nil
}

type ghAuthUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Type  string `json:"type"`
}

func (p *ghProvider) completeToken(s setupSession, params map[string]string) (gp.Identity, error) {
	token := strings.TrimSpace(params[fieldToken])
	secret := strings.TrimSpace(params[fieldWebhookSecret])
	if token == "" {
		return gp.Identity{}, gp.Errorf(gp.CodeValidation, "the access token is required")
	}
	switch {
	case secret == "":
		var err error
		if secret, err = randomHex(32); err != nil {
			return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not generate a webhook secret")
		}
	case len(secret) < minWebhookSecretLen:
		return gp.Identity{}, gp.Errorf(gp.CodeValidation, "the webhook secret must be at least %d characters", minWebhookSecretLen)
	}
	// Prove the token before spending the state: a typo can be retried.
	var u ghAuthUser
	status, text, headers, err := (&ghAPI{token: token}).doRaw("GET", "/user", nil, "")
	switch {
	case err != nil:
		switch gp.AsError(err).Code {
		case gp.CodeForbidden, gp.CodeNotFound:
			return gp.Identity{}, gp.Errorf(gp.CodeValidation, "GitHub rejected the token: check that it is correct and has not expired")
		}
		return gp.Identity{}, err
	case status >= 400:
		return gp.Identity{}, gp.Errorf(gp.CodeValidation, "GitHub rejected the token")
	}
	if err := decodeInto(text, &u); err != nil {
		return gp.Identity{}, err
	}
	if u.Login == "" {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "GitHub returned an incomplete user")
	}

	claimed, err := p.store.claimSetupCode(s.State)
	if err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not record setup progress")
	}
	if !claimed {
		return gp.Identity{}, gp.Errorf(gp.CodeForbidden, "this setup was already completed")
	}
	rec := tokenRecord{Login: u.Login, Name: u.Name, Token: token, WebhookSecret: secret}
	id, err := p.store.createToken(rec)
	if err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not store the token")
	}
	rec.ID = id
	s.CodeUsed, s.Completed = true, true
	if err := p.store.updateSetup(s); err != nil {
		// The state is spent and the caller sees a failure: do not leave a token
		// nobody was told about.
		_ = p.store.deleteToken(id)
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not record setup progress")
	}
	ident := identityOfToken(rec)
	if exp := tokenExpiry(headers); !exp.IsZero() {
		ident.ExpiresAt = exp
	}
	return ident, nil
}

func identityOfToken(t tokenRecord) gp.Identity {
	name := t.Login
	if t.Name != "" {
		name = t.Name + " (" + t.Login + ")"
	}
	return gp.Identity{
		Ref:         gp.IdentityRef{ID: t.ID, Handle: t.Login},
		Host:        githubHost,
		Kind:        gp.IdentityToken,
		DisplayName: name,
	}
}

// tokenExpiry reads the expiry GitHub reports for tokens that have one
// ("2026-11-30 10:00:00 UTC"); zero when there is none.
func tokenExpiry(h map[string]string) time.Time {
	v := strings.TrimSpace(headerOf(h, "github-authentication-token-expiration"))
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02 15:04:05 MST", v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// listTokenRepositories pages through the repositories the token can reach.
func (p *ghProvider) listTokenRepositories(tok tokenRecord, req gp.ListReposRequest) (gp.RepositoryPage, error) {
	per := req.PageSize
	if per <= 0 || per > 100 {
		per = 30
	}
	page := 1
	if req.Cursor != "" {
		n, err := strconv.Atoi(req.Cursor)
		if err != nil || n < 1 {
			return gp.RepositoryPage{}, gp.Errorf(gp.CodeValidation, "invalid cursor")
		}
		page = n
	}
	q := strings.ToLower(strings.TrimSpace(req.Query))
	out := gp.RepositoryPage{Items: []gp.Repository{}}
	// GitHub cannot filter this listing, so with a query the pages are scanned
	// (bounded) until a page's worth of matches is found: a match on a later page
	// is not hidden behind an empty first page.
	for scanned := 0; scanned < maxTokenListPages; scanned++ {
		var batch []ghRepoJSON
		path := "/user/repos?affiliation=owner,collaborator,organization_member&sort=full_name&per_page=" + strconv.Itoa(per) + "&page=" + strconv.Itoa(page)
		if err := (&ghAPI{token: tok.Token}).get(path, &batch); err != nil {
			return gp.RepositoryPage{}, err
		}
		for _, r := range batch {
			if q != "" && !strings.Contains(strings.ToLower(r.FullName), q) {
				continue
			}
			out.Items = append(out.Items, gp.Repository{Ref: repoRefOf(r, 0), Private: r.Private, Description: r.Description})
		}
		page++
		if len(batch) < per {
			return out, nil
		}
		if q == "" || len(out.Items) >= per {
			break
		}
	}
	out.NextCursor = strconv.Itoa(page)
	return out, nil
}

// maxTokenListPages bounds how many pages one filtered listing scans.
const maxTokenListPages = 10

// resolveWithToken looks a repository up as a token identity. A repository the
// token cannot see is not_found, like an App that is not installed there.
func (p *ghProvider) resolveWithToken(tok tokenRecord, owner, name string) (gp.ResolvedRepository, error) {
	var r ghRepoJSON
	if err := (&ghAPI{token: tok.Token}).get("/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), &r); err != nil {
		return gp.ResolvedRepository{}, err
	}
	return gp.ResolvedRepository{
		Repository: gp.Repository{Ref: repoRefOf(r, 0), Private: r.Private, Description: r.Description},
		Identity:   gp.IdentityRef{ID: tok.ID, Handle: tok.Login},
	}, nil
}

// ── repository webhooks ──────────────────────────────────────────────────────

type ghHook struct {
	ID     int64 `json:"id"`
	Active bool  `json:"active"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// EnsureWebhook implements gp.WebhookManager for token identities. It is
// idempotent: a hook already pointing at the URL is updated (its secret and
// events refreshed) instead of duplicated.
func (p *ghProvider) EnsureWebhook(_ context.Context, req gp.EnsureWebhookRequest) (gp.WebhookRef, error) {
	if strings.TrimSpace(req.URL) == "" {
		return gp.WebhookRef{}, gp.Errorf(gp.CodeValidation, "url is required")
	}
	tok, err := p.loadTokenIdentity(req.Target.Identity.ID)
	if err != nil {
		return gp.WebhookRef{}, err
	}
	repo, err := repoPath(req.Target.Repo.FullPath)
	if err != nil {
		return gp.WebhookRef{}, err
	}
	api := &ghAPI{token: tok.Token}
	body := map[string]any{
		"name": "web", "active": !isLocalURL(req.URL), "events": hookEvents,
		"config": map[string]any{"url": req.URL, "content_type": "json", "secret": tok.WebhookSecret, "insecure_ssl": "0"},
	}
	var hooks []ghHook
	if err := api.get(repo+"/hooks?per_page=100", &hooks); err != nil {
		return gp.WebhookRef{}, err
	}
	for _, h := range hooks {
		if h.Config.URL != req.URL {
			continue
		}
		var out ghHook
		if err := api.send("PATCH", repo+"/hooks/"+strconv.FormatInt(h.ID, 10), body, &out); err != nil {
			return gp.WebhookRef{}, err
		}
		return gp.WebhookRef{ID: strconv.FormatInt(h.ID, 10)}, nil
	}
	var out ghHook
	if err := api.send("POST", repo+"/hooks", body, &out); err != nil {
		return gp.WebhookRef{}, err
	}
	if out.ID == 0 {
		return gp.WebhookRef{}, gp.Errorf(gp.CodeInternal, "GitHub returned no webhook id")
	}
	return gp.WebhookRef{ID: strconv.FormatInt(out.ID, 10)}, nil
}

// RemoveWebhook implements gp.WebhookManager. A webhook that is already gone is fine.
func (p *ghProvider) RemoveWebhook(_ context.Context, req gp.RemoveWebhookRequest) (gp.Empty, error) {
	if _, err := strconv.ParseUint(req.Webhook.ID, 10, 63); err != nil {
		return gp.Empty{}, gp.Errorf(gp.CodeValidation, "webhook id is not a GitHub hook id")
	}
	tok, err := p.loadTokenIdentity(req.Target.Identity.ID)
	if err != nil {
		return gp.Empty{}, err
	}
	repo, err := repoPath(req.Target.Repo.FullPath)
	if err != nil {
		return gp.Empty{}, err
	}
	if err := (&ghAPI{token: tok.Token}).send("DELETE", repo+"/hooks/"+req.Webhook.ID, nil, nil); err != nil {
		if gp.AsError(err).Code == gp.CodeNotFound {
			return gp.Empty{}, nil
		}
		return gp.Empty{}, err
	}
	return gp.Empty{}, nil
}

// hookEvents are the events a repository webhook subscribes to: the same
// names the App manifest lists.
var hookEvents = func() []string {
	ev := append([]string(nil), manifestEvents...)
	sort.Strings(ev)
	return ev
}()

// ── commit statuses (tokens cannot create check runs) ────────────────────────

// setCommitStatus reports a run's state as a commit status: only a GitHub App
// can create check runs. The status ref is the commit, and a later call with the
// same title replaces the earlier status in GitHub's UI.
func (p *ghProvider) setCommitStatus(api *ghAPI, repo string, req gp.SetStatusRequest, summary string) (gp.StatusResult, error) {
	state := "pending"
	switch req.State {
	case gp.RunQueued, gp.RunInProgress:
	case gp.RunSuccess:
		state = "success"
	case gp.RunFailure:
		state = "failure"
	case gp.RunCancelled:
		state = "error"
	}
	sha := req.SHA
	if sha == "" {
		sha = req.StatusRef
	}
	if sha == "" && req.Subject.Kind == gp.SubjectChangeRequest && req.Subject.Number != "" {
		num, err := number(req.Subject.Number)
		if err != nil {
			return gp.StatusResult{}, err
		}
		pr, err := p.getPR(api, repo, num)
		if err != nil {
			return gp.StatusResult{}, err
		}
		sha = pr.Head.SHA
	}
	if sha == "" {
		return gp.StatusResult{}, gp.Errorf(gp.CodeValidation, "sha or a change request is required")
	}
	body := map[string]any{"state": state, "context": req.Title, "description": clipRunes(summary, 140)}
	if req.URL != "" {
		body["target_url"] = req.URL
	}
	if err := api.send("POST", repo+"/statuses/"+url.PathEscape(sha), body, nil); err != nil {
		return gp.StatusResult{}, err
	}
	return gp.StatusResult{StatusRef: sha}, nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
