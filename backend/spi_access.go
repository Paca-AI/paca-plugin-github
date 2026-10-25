package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// Access grants: a GitHub App can only see the repositories of accounts it is
// installed on, and each project may use a different user or organization, so
// the install happens when a project links a repository there — not when the
// App is created. Core calls BeginAccessGrant with the owner it needs, the user
// installs the App on GitHub, GitHub returns to Paca with ?installation_id, and
// CompleteAccessGrant records the installation.

const accessScope = "access" // setupSession.Scope for an access grant

// refreshWindow is how long a miss-driven refresh of the App's installations is
// remembered, so a repository on an account the App is not installed on does not
// ask GitHub on every call.
const refreshWindow = time.Minute

// BeginAccessGrant implements gp.AccessGranter.
func (p *ghProvider) BeginAccessGrant(_ context.Context, req gp.BeginAccessRequest) (gp.SetupStep, error) {
	if req.CallbackURL == "" {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "callback_url is required")
	}
	app, err := p.loadApp(req.Identity.ID)
	if err != nil {
		return gp.SetupStep{}, err
	}
	owner := strings.TrimSpace(req.Owner)
	if owner != "" && !githubLoginRe.MatchString(owner) {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "owner is not a valid GitHub user or organization name")
	}

	// With a known owner, GitHub's install page can open on that account
	// directly; without one the user picks the account there.
	install := "https://github.com/apps/" + url.PathEscape(app.Slug) + "/installations/new"
	if owner != "" {
		var acct struct {
			ID int64 `json:"id"`
		}
		err := p.publicAPI(app).get("/users/"+url.PathEscape(owner), &acct)
		if err != nil && gp.AsError(err).Code == gp.CodeForbidden {
			// The token was refused (and could not be replaced): the endpoint is
			// public, so ask without credentials.
			err = (&ghAPI{}).get("/users/"+url.PathEscape(owner), &acct)
		}
		if err != nil {
			return gp.SetupStep{}, err // not_found: no such GitHub account
		}
		install += "/permissions?target_id=" + strconv.FormatInt(acct.ID, 10)
	}

	state, err := randomHex(24)
	if err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not generate state")
	}
	if err := p.store.saveSetup(setupSession{
		State: state, Scope: accessScope, AppID: app.ID, OwnerOrg: owner, ExpiresAt: p.now().Add(setupSessionLife),
	}); err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not start the access grant")
	}
	sep := "?"
	if strings.Contains(install, "?") {
		sep = "&"
	}
	return gp.SetupStep{Kind: gp.StepRedirect, URL: install + sep + "state=" + url.QueryEscape(state), State: state}, nil
}

// CompleteAccessGrant implements gp.AccessGranter. The state must be this
// identity's own, unused and unexpired; the installation id from the URL is
// never trusted on its own — it is fetched from GitHub as the App, which only
// answers for the App's own installations.
func (p *ghProvider) CompleteAccessGrant(_ context.Context, req gp.CompleteAccessRequest) (gp.AccessGrant, error) {
	if strings.TrimSpace(req.State) == "" {
		return gp.AccessGrant{}, gp.Errorf(gp.CodeValidation, "state is required")
	}
	app, err := p.loadApp(req.Identity.ID)
	if err != nil {
		return gp.AccessGrant{}, err
	}
	s, err := p.store.loadSetup(req.State)
	if err != nil || s.Scope != accessScope || s.AppID != app.ID || s.Completed || !p.now().Before(s.ExpiresAt) {
		return gp.AccessGrant{}, gp.Errorf(gp.CodeForbidden, "unknown or expired access grant")
	}
	if req.Params["setup_action"] == "request" && req.Params["installation_id"] == "" {
		// An organization owner has to approve the install first; nothing was granted yet.
		return gp.AccessGrant{}, gp.Errorf(gp.CodeConflict, "the installation is waiting for approval by an organization owner")
	}
	instID, err := strconv.ParseInt(strings.TrimSpace(req.Params["installation_id"]), 10, 64)
	if err != nil || instID <= 0 {
		return gp.AccessGrant{}, gp.Errorf(gp.CodeValidation, "installation_id is required")
	}
	api, err := p.appAPI(app)
	if err != nil {
		return gp.AccessGrant{}, err
	}
	var gi ghInstallation
	if err := api.get("/app/installations/"+strconv.FormatInt(instID, 10), &gi); err != nil {
		return gp.AccessGrant{}, err
	}
	if err := p.store.upsertInstallation(app.ID, installFrom(gi)); err != nil {
		return gp.AccessGrant{}, gp.Errorf(gp.CodeInternal, "could not store the installation")
	}
	s.Completed = true
	if err := p.store.updateSetup(s); err != nil {
		return gp.AccessGrant{}, gp.Errorf(gp.CodeInternal, "could not record the access grant")
	}
	return gp.AccessGrant{Owner: gi.Account.Login}, nil
}

func installFrom(gi ghInstallation) installRecord {
	return installRecord{
		InstallationID: gi.ID, AccountLogin: gi.Account.Login, AccountType: gi.Account.Type,
		Selection: gi.RepositorySelection, Suspended: gi.SuspendedAt != nil,
	}
}

// publicAPI is for GitHub's public endpoints (an account's id). An App JWT is
// refused there, so it uses an installation token when the App has one (a
// higher rate limit than anonymous calls) and no credentials otherwise.
func (p *ghProvider) publicAPI(app appRecord) *ghAPI {
	if list, err := p.store.installations(app.ID); err == nil {
		for _, in := range list {
			if in.Suspended {
				continue
			}
			if tok, _, err := p.mintToken(app, in, nil); err == nil {
				return &ghAPI{token: tok, refresh: p.freshToken(app, in)}
			}
		}
	}
	return &ghAPI{}
}

// refreshInstallations asks GitHub which accounts the App is installed on and
// records them. It makes an install done directly on GitHub (not through a
// Paca grant flow) take effect the first time something needs it. It reports
// whether it actually asked GitHub; a recent refresh is not repeated.
func (p *ghProvider) refreshInstallations(app appRecord) bool {
	key := "inst-refresh:" + app.ID
	if _, exp, ok := p.store.cacheGet(key); ok && exp.Sub(p.now()) > installationTokenReuseMargin {
		return false
	}
	p.store.cacheSet(key, "1", p.now().Add(installationTokenReuseMargin+refreshWindow))
	api, err := p.appAPI(app)
	if err != nil {
		return false
	}
	for page := 1; page <= 5; page++ {
		var list []ghInstallation
		if err := api.get("/app/installations?per_page=100&page="+strconv.Itoa(page), &list); err != nil {
			return true
		}
		for _, gi := range list {
			_ = p.store.upsertInstallation(app.ID, installFrom(gi))
		}
		if len(list) < 100 {
			break
		}
	}
	return true
}
