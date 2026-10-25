package main

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

const (
	githubHost       = "github.com"
	githubCloneBase  = "https://github.com/"
	botCredUsername  = "x-access-token"
	setupSessionLife = time.Hour
)

// ghProvider is the GitHub implementation of the Git provider SPI
// (gitprovider.v1): identities are GitHub Apps created through the manifest
// flow, credentials are installation tokens, webhooks are the app-level
// webhook core terminates. See docs/plugins/git-provider-spi.md in the Paca
// repository for the contract this implements.
type ghProvider struct {
	store spiStore
	now   func() time.Time
}

func newGHProvider(store spiStore) *ghProvider {
	return &ghProvider{store: store, now: time.Now}
}

// Describe implements gp.Provider. Capabilities are declared honestly:
//   - review_request / assignment are not declared: an ordinary GitHub App bot
//     cannot be a requested reviewer or an assignee, so those events never
//     address the bot (users mention it or add a label instead);
//   - code_search, rerun_checks, reactions and webhook_management are not
//     implemented (the app-level webhook is managed by core, not per repo).
func (p *ghProvider) Describe() gp.Info {
	return gp.Info{
		SPIVersion:      gp.SPIVersion,
		ID:              githubPluginID,
		DisplayName:     "GitHub",
		Icon:            "github",
		Hosts:           []string{githubHost},
		SelfHostedHosts: false,
		Terms:           gp.Terms{ChangeRequest: "Pull request", ChangeRequestPlural: "Pull requests", Repository: "Repository"},
		Capabilities: gp.Capabilities{
			IdentityKinds:      []gp.IdentityKind{gp.IdentityApp},
			CredentialModel:    gp.CredentialEphemeral,
			WebhookScope:       gp.WebhookApp,
			StatusKind:         gp.StatusCheckRun,
			Triggers:           []gp.TriggerKind{gp.TriggerMention, gp.TriggerReviewRequest, gp.TriggerAssignment, gp.TriggerCIFailure, gp.TriggerOwnCRFeedback, gp.TriggerAutoReview},
			MultipleIdentities: true,

			ResolveThreads:   true,
			FileRead:         true,
			Labels:           true,
			ReviewerRequests: true,
			Assignees:        true,
			Merge:            true,
			IdentityDeletion: true,
			Branches:         true,
			AccessGrants:     true,

			BatchReview:         true,
			HiddenMarkers:       true,
			ForkChangeRequests:  true,
			DraftChangeRequests: true,
			NestedPaths:         false,
			CIConfigGlobs:       []string{".github/workflows/**", ".github/actions/**"},
		},
	}
}

// ── installations and tokens ─────────────────────────────────────────────────

const installationTokenReuseMargin = 5 * time.Minute

func notFoundIdentity() error {
	return gp.Errorf(gp.CodeNotFound, "the GitHub identity does not exist")
}

func (p *ghProvider) loadApp(identityID string) (appRecord, error) {
	if strings.TrimSpace(identityID) == "" {
		return appRecord{}, notFoundIdentity()
	}
	app, err := p.store.loadApp(identityID)
	if err != nil {
		if err == errNoSuchApp {
			return appRecord{}, notFoundIdentity()
		}
		return appRecord{}, gp.Errorf(gp.CodeInternal, "could not load the GitHub identity")
	}
	return app, nil
}

// appAPI authenticates as the App itself (a short-lived JWT).
func (p *ghProvider) appAPI(app appRecord) (*ghAPI, error) {
	jwt, err := signAppJWT(app.GitHubAppID, app.PrivateKeyPEM, p.now())
	if err != nil {
		return nil, gp.Errorf(gp.CodeInternal, "could not sign the GitHub App token")
	}
	return &ghAPI{token: jwt}, nil
}

// installationFor finds the live installation covering owner.
func (p *ghProvider) installationFor(app appRecord, owner string) (installRecord, error) {
	return p.findInstallation(app, owner, false)
}

func (p *ghProvider) findInstallation(app appRecord, owner string, refreshed bool) (installRecord, error) {
	list, err := p.store.installations(app.ID)
	if err != nil {
		return installRecord{}, gp.Errorf(gp.CodeInternal, "could not load installations")
	}
	for _, in := range list {
		if strings.EqualFold(in.AccountLogin, owner) {
			if in.Suspended {
				return installRecord{}, gp.Errorf(gp.CodeForbidden, "the GitHub App installation is suspended")
			}
			return in, nil
		}
	}
	// Not installed as far as Paca knows. The user may have installed the App on
	// GitHub directly, so ask GitHub once (rate-limited) before giving up.
	if !refreshed && p.refreshInstallations(app) {
		return p.findInstallation(app, owner, true)
	}
	return installRecord{}, gp.Errorf(gp.CodeNotFound, "the identity is not installed on this account")
}

// mintToken returns an installation token; scope, when set, narrows it to one
// repository and the given permissions. Unscoped tokens are reused from the
// cache until shortly before expiry; scoped ones are never cached.
func (p *ghProvider) mintToken(app appRecord, in installRecord, scope map[string]any) (string, time.Time, error) {
	key := "inst:" + in.RowID
	if scope == nil {
		if tok, exp, ok := p.store.cacheGet(key); ok && exp.Sub(p.now()) > installationTokenReuseMargin {
			return tok, exp, nil
		}
	}
	api, err := p.appAPI(app)
	if err != nil {
		return "", time.Time{}, err
	}
	body := map[string]any{}
	for k, v := range scope {
		body[k] = v
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := api.send("POST", "/app/installations/"+strconv.FormatInt(in.InstallationID, 10)+"/access_tokens", body, &out); err != nil {
		if gp.AsError(err).Code == gp.CodeNotFound {
			// GitHub has no such installation: the App was uninstalled. Forget the
			// dead record so the account reads as "not installed" again.
			_ = p.store.deleteInstallation(app.ID, in.InstallationID)
			p.store.cacheDelete(key)
			return "", time.Time{}, gp.Errorf(gp.CodeNotFound, "the identity is no longer installed on this account")
		}
		return "", time.Time{}, err
	}
	if out.Token == "" {
		return "", time.Time{}, gp.Errorf(gp.CodeInternal, "GitHub returned no installation token")
	}
	if scope == nil {
		p.store.cacheSet(key, out.Token, out.ExpiresAt)
	}
	return out.Token, out.ExpiresAt, nil
}

// freshToken returns a function that discards the cached installation token and
// mints a new one, for an API view whose token GitHub rejected.
func (p *ghProvider) freshToken(app appRecord, in installRecord) func() (string, error) {
	return func() (string, error) {
		p.store.cacheDelete("inst:" + in.RowID)
		tok, _, err := p.mintToken(app, in, nil)
		return tok, err
	}
}

// apiFor returns an API view acting as identityID on the account owning repo.
func (p *ghProvider) apiFor(t gp.Target) (*ghAPI, string, error) {
	owner, _, ok := splitFullPath(t.Repo.FullPath)
	if !ok {
		return nil, "", gp.Errorf(gp.CodeValidation, "repository must be owner/name")
	}
	repo, err := repoPath(t.Repo.FullPath)
	if err != nil {
		return nil, "", err
	}
	app, err := p.loadApp(t.Identity.ID)
	if err != nil {
		return nil, "", err
	}
	in, err := p.installationFor(app, owner)
	if err != nil {
		return nil, "", err
	}
	tok, _, err := p.mintToken(app, in, nil)
	if err != nil {
		return nil, "", err
	}
	return &ghAPI{token: tok, refresh: p.freshToken(app, in)}, repo, nil
}

// ── repositories ─────────────────────────────────────────────────────────────

type ghRepoJSON struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
}

func repoRefOf(r ghRepoJSON, installationID int64) gp.RepoRef {
	ref, _ := json.Marshal(map[string]int64{"installation_id": installationID})
	return gp.RepoRef{
		Host: githubHost, FullPath: r.FullName, ExternalID: strconv.FormatInt(r.ID, 10),
		DefaultBranch: r.DefaultBranch, CloneURL: githubCloneBase + r.FullName + ".git", ProviderRef: ref,
	}
}

// ListRepositories implements gp.Provider. The cursor is "<installation>:<page>"
// over the identity's installations in a stable order, so paging is exact.
func (p *ghProvider) ListRepositories(_ context.Context, req gp.ListReposRequest) (gp.RepositoryPage, error) {
	app, err := p.loadApp(req.Identity.ID)
	if err != nil {
		return gp.RepositoryPage{}, err
	}
	list, err := p.store.installations(app.ID)
	if err != nil {
		return gp.RepositoryPage{}, gp.Errorf(gp.CodeInternal, "could not load installations")
	}
	if len(list) == 0 && p.refreshInstallations(app) {
		list, _ = p.store.installations(app.ID) // installed on GitHub, not yet known here
	}
	live := list[:0:0]
	for _, in := range list {
		if !in.Suspended {
			live = append(live, in)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		return strings.ToLower(live[i].AccountLogin) < strings.ToLower(live[j].AccountLogin)
	})

	per := req.PageSize
	if per <= 0 || per > 100 {
		per = 30
	}
	idx, page := 0, 1
	if req.Cursor != "" {
		a, b, ok := strings.Cut(req.Cursor, ":")
		i, e1 := strconv.Atoi(a)
		n, e2 := strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || i < 0 || n < 1 {
			return gp.RepositoryPage{}, gp.Errorf(gp.CodeValidation, "invalid cursor")
		}
		idx, page = i, n
	}
	if idx >= len(live) {
		return gp.RepositoryPage{Items: []gp.Repository{}}, nil
	}
	in := live[idx]
	tok, _, err := p.mintToken(app, in, nil)
	if err != nil {
		return gp.RepositoryPage{}, err
	}
	var batch struct {
		Repositories []ghRepoJSON `json:"repositories"`
	}
	if err := (&ghAPI{token: tok, refresh: p.freshToken(app, in)}).get("/installation/repositories?per_page="+strconv.Itoa(per)+"&page="+strconv.Itoa(page), &batch); err != nil {
		return gp.RepositoryPage{}, err
	}
	out := gp.RepositoryPage{Items: []gp.Repository{}}
	q := strings.ToLower(strings.TrimSpace(req.Query))
	for _, r := range batch.Repositories {
		if q != "" && !strings.Contains(strings.ToLower(r.FullName), q) {
			continue
		}
		out.Items = append(out.Items, gp.Repository{Ref: repoRefOf(r, in.InstallationID), Private: r.Private, Description: r.Description})
	}
	switch {
	case len(batch.Repositories) >= per:
		out.NextCursor = strconv.Itoa(idx) + ":" + strconv.Itoa(page+1)
	case idx+1 < len(live):
		out.NextCursor = strconv.Itoa(idx+1) + ":1"
	}
	return out, nil
}

// parseRepoInput accepts owner/name, https/ssh URLs and ".git" suffixes. A URL
// for another host is rejected rather than guessed at.
func parseRepoInput(in string) (owner, name string, err error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", "", gp.Errorf(gp.CodeValidation, "a repository is required")
	}
	switch {
	case strings.HasPrefix(s, "git@"):
		host, path, ok := strings.Cut(strings.TrimPrefix(s, "git@"), ":")
		if !ok || !strings.EqualFold(host, githubHost) {
			return "", "", gp.Errorf(gp.CodeValidation, "only %s repositories are supported", githubHost)
		}
		s = path
	case strings.Contains(s, "://"):
		u, perr := url.Parse(s)
		if perr != nil || !strings.EqualFold(u.Hostname(), githubHost) {
			return "", "", gp.Errorf(gp.CodeValidation, "only %s repositories are supported", githubHost)
		}
		s = u.Path
	case strings.HasPrefix(strings.ToLower(s), githubHost+"/"):
		s = s[len(githubHost)+1:]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", gp.Errorf(gp.CodeValidation, "repository must be owner/name or a GitHub URL")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// ResolveRepository implements gp.Provider: the first candidate identity that
// can read the repository wins.
func (p *ghProvider) ResolveRepository(_ context.Context, req gp.ResolveRepoRequest) (gp.ResolvedRepository, error) {
	if req.Host != "" && !strings.EqualFold(req.Host, githubHost) {
		return gp.ResolvedRepository{}, gp.Errorf(gp.CodeValidation, "unknown host %q", req.Host)
	}
	owner, name, err := parseRepoInput(req.PathOrURL)
	if err != nil {
		return gp.ResolvedRepository{}, err
	}
	if len(req.Candidates) == 0 {
		return gp.ResolvedRepository{}, gp.Errorf(gp.CodeForbidden, "no GitHub identity is available to look this repository up")
	}
	var last error
	for _, c := range req.Candidates {
		app, err := p.loadApp(c.ID)
		if err != nil {
			last = err
			continue
		}
		in, err := p.installationFor(app, owner)
		if err != nil {
			last = err
			continue
		}
		tok, _, err := p.mintToken(app, in, nil)
		if err != nil {
			return gp.ResolvedRepository{}, err
		}
		var r ghRepoJSON
		if err := (&ghAPI{token: tok, refresh: p.freshToken(app, in)}).get("/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), &r); err != nil {
			if gp.AsError(err).Code == gp.CodeNotFound {
				last = err
				continue
			}
			return gp.ResolvedRepository{}, err
		}
		// A public repository answers any token, so being able to read it proves
		// nothing. When the installation covers only selected repositories, ask
		// GitHub whether this one is among them: a token scoped to it is refused
		// (422) otherwise. Without this, linking succeeds and every credential
		// for the repository fails later.
		if !strings.EqualFold(in.Selection, "all") {
			_, _, err := p.mintToken(app, in, map[string]any{
				"repositories": []string{name},
				"permissions":  map[string]string{"metadata": "read"},
			})
			if err != nil {
				if gp.AsError(err).Code == gp.CodeValidation {
					last = gp.Errorf(gp.CodeNotFound, "the identity is installed on %s but not on this repository", owner)
					continue
				}
				return gp.ResolvedRepository{}, err
			}
		}
		return gp.ResolvedRepository{
			Repository: gp.Repository{Ref: repoRefOf(r, in.InstallationID), Private: r.Private, Description: r.Description},
			Identity:   gp.IdentityRef{ID: app.ID, Handle: app.Slug},
		}, nil
	}
	if last != nil && gp.AsError(last).Code == gp.CodeForbidden {
		return gp.ResolvedRepository{}, last
	}
	return gp.ResolvedRepository{}, gp.Errorf(gp.CodeNotFound, "no identity can see %s/%s", owner, name)
}

// ── credentials and policy inputs ────────────────────────────────────────────

// MintCredential implements gp.Provider. The token is an installation token
// narrowed to the one repository and to what git needs: contents read, or
// contents write (a push). It never carries pull_requests or workflows
// permissions, so a leaked git credential cannot rewrite CI or open PRs.
func (p *ghProvider) MintCredential(_ context.Context, req gp.MintRequest) (gp.Credential, error) {
	perm := "read"
	switch req.Access {
	case gp.AccessRead:
	case gp.AccessWrite:
		perm = "write"
	default:
		return gp.Credential{}, gp.Errorf(gp.CodeValidation, "access must be read or write")
	}
	owner, name, ok := splitFullPath(req.Target.Repo.FullPath)
	if !ok {
		return gp.Credential{}, gp.Errorf(gp.CodeValidation, "repository must be owner/name")
	}
	app, err := p.loadApp(req.Target.Identity.ID)
	if err != nil {
		return gp.Credential{}, err
	}
	in, err := p.installationFor(app, owner)
	if err != nil {
		return gp.Credential{}, err
	}
	tok, exp, err := p.mintToken(app, in, map[string]any{
		"repositories": []string{name},
		"permissions":  map[string]string{"contents": perm, "metadata": "read"},
	})
	if err != nil {
		// GitHub answers 422 when the repository is outside a "selected" installation.
		if gp.AsError(err).Code == gp.CodeValidation {
			return gp.Credential{}, gp.Errorf(gp.CodeNotFound, "the repository is not accessible to this identity")
		}
		return gp.Credential{}, err
	}
	// GitHub installation tokens last an hour and cannot be shortened. When the
	// caller names a lifetime, ExpiresAt reports that instead: the token is
	// good until then, and a caller that stops using it at ExpiresAt is never
	// holding a credential longer than it asked for.
	if req.TTLSeconds > 0 {
		if until := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second); until.Before(exp) {
			exp = until
		}
	}
	return gp.Credential{Username: botCredUsername, Secret: tok, ExpiresAt: exp}, nil
}

// GetActorPermission implements gp.Provider. A login GitHub does not know has
// no access; that is an answer, not an error.
func (p *ghProvider) GetActorPermission(_ context.Context, req gp.ActorPermissionRequest) (gp.Permission, error) {
	login := strings.TrimSpace(req.Login)
	if login == "" {
		return gp.PermissionNone, gp.Errorf(gp.CodeValidation, "login is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.PermissionNone, err
	}
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	if err := api.get(repo+"/collaborators/"+url.PathEscape(login)+"/permission", &out); err != nil {
		if gp.AsError(err).Code == gp.CodeNotFound {
			return gp.PermissionNone, nil
		}
		return gp.PermissionNone, err
	}
	// role_name distinguishes maintain/triage, which "permission" folds into write/read.
	for _, v := range []string{out.RoleName, out.Permission} {
		switch v {
		case "admin":
			return gp.PermissionAdmin, nil
		case "maintain":
			return gp.PermissionMaintain, nil
		case "write":
			return gp.PermissionWrite, nil
		case "triage":
			return gp.PermissionTriage, nil
		case "read":
			return gp.PermissionRead, nil
		case "none":
			return gp.PermissionNone, nil
		}
	}
	return gp.PermissionNone, nil
}

// GetBranchProtection implements gp.Provider using the branch endpoint, which
// needs only contents:read (the dedicated protection endpoint needs the
// administration permission the app does not request).
func (p *ghProvider) GetBranchProtection(_ context.Context, req gp.BranchProtectionRequest) (gp.BranchProtection, error) {
	if strings.TrimSpace(req.Branch) == "" {
		return gp.BranchProtection{}, gp.Errorf(gp.CodeValidation, "branch is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.BranchProtection{}, err
	}
	var out struct {
		Protected  bool `json:"protected"`
		Protection struct {
			RequiredStatusChecks *json.RawMessage `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := api.get(repo+"/branches/"+url.PathEscape(req.Branch), &out); err != nil {
		if gp.AsError(err).Code == gp.CodeNotFound {
			return gp.BranchProtection{}, nil // a branch that does not exist is not protected
		}
		return gp.BranchProtection{}, err
	}
	return gp.BranchProtection{Protected: out.Protected, RequiresStatusChecks: out.Protection.RequiredStatusChecks != nil}, nil
}
