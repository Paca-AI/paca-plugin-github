package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// Identity setup is the GitHub App manifest flow: Begin → a form post to GitHub
// with the manifest; GitHub redirects back to the Paca page core names in
// CallbackURL with ?code&state; Complete(code) exchanges the code for the App's
// credentials. The App is not installed here: installation is per account and
// happens when a project links a repository there (spi_access.go).
//
// The webhook of every App points at the URL core gives in WebhookURL, so
// deliveries reach core (not this plugin) and are handed back for verification.

// BeginIdentitySetup implements gp.Provider.
func (p *ghProvider) BeginIdentitySetup(_ context.Context, req gp.BeginSetupRequest) (gp.SetupStep, error) {
	if !strings.EqualFold(req.Host, githubHost) {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "unknown host %q: only %s is supported", req.Host, githubHost)
	}
	if req.Scope != gp.SetupShared && req.Scope != gp.SetupAgent {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "scope must be shared or agent")
	}
	if req.CallbackURL == "" || req.WebhookURL == "" {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "callback_url and webhook_url are required")
	}
	name := strings.TrimSpace(req.Params["name"])
	if name == "" {
		name = "Paca"
		if req.Scope == gp.SetupAgent && strings.TrimSpace(req.AgentName) != "" {
			name = "Paca " + strings.TrimSpace(req.AgentName)
		}
	}
	if len(name) > githubAppNameMaxLen {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "the app name must be at most %d characters", githubAppNameMaxLen)
	}
	org := strings.TrimSpace(req.Params["owner_org"])
	if org != "" && !githubLoginRe.MatchString(org) {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "owner_org is not a valid GitHub organization name")
	}
	home, err := originOf(req.CallbackURL)
	if err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeValidation, "callback_url is not a valid URL")
	}

	state, err := randomHex(24)
	if err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not generate state")
	}
	if err := p.store.saveSetup(setupSession{
		State: state, Scope: string(req.Scope), AgentID: req.AgentID,
		AppName: name, OwnerOrg: org, ExpiresAt: p.now().Add(setupSessionLife),
	}); err != nil {
		return gp.SetupStep{}, gp.Errorf(gp.CodeInternal, "could not start setup")
	}

	manifest := buildSPIManifest(name, home, req.WebhookURL, req.CallbackURL, !isLocalURL(req.WebhookURL), req.Params["public"] == "true")
	mj, _ := json.Marshal(manifest)
	post := "https://github.com/settings/apps/new"
	if org != "" {
		post = "https://github.com/organizations/" + org + "/settings/apps/new"
	}
	return gp.SetupStep{
		Kind:   gp.StepFormPost,
		URL:    post + "?state=" + url.QueryEscape(state),
		Fields: map[string]string{"manifest": string(mj)},
		State:  state,
	}, nil
}

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errBadURL
	}
	return u.Scheme + "://" + u.Host, nil
}

type badURL struct{}

func (badURL) Error() string { return "invalid url" }

var errBadURL = badURL{}

// buildSPIManifest is the App manifest for the SPI flow. The webhook goes to
// core; both redirects return to the same Paca page.
func buildSPIManifest(name, home, hookURL, callbackURL string, hookActive, public bool) map[string]any {
	return map[string]any{
		"name":                name,
		"url":                 home,
		"hook_attributes":     map[string]any{"url": hookURL, "active": hookActive},
		"redirect_url":        callbackURL,
		"setup_url":           callbackURL,
		"setup_on_update":     true,
		"public":              public,
		"default_permissions": manifestPermissions,
		"default_events":      manifestEvents,
	}
}

// CompleteIdentitySetup implements gp.Provider.
func (p *ghProvider) CompleteIdentitySetup(_ context.Context, req gp.CompleteSetupRequest) (gp.Identity, error) {
	code := strings.TrimSpace(req.Params["code"])
	if strings.TrimSpace(req.State) == "" || code == "" {
		return gp.Identity{}, gp.Errorf(gp.CodeValidation, "state and code are required")
	}
	s, err := p.store.loadSetup(req.State)
	if err != nil || s.Scope == accessScope || s.Completed || !p.now().Before(s.ExpiresAt) {
		return gp.Identity{}, gp.Errorf(gp.CodeForbidden, "unknown or expired setup state")
	}
	return p.completeCreated(s, code)
}

func identityOf(app appRecord) gp.Identity {
	pr, _ := json.Marshal(map[string]any{"github_app_id": app.GitHubAppID, "slug": app.Slug})
	return gp.Identity{
		Ref:         gp.IdentityRef{ID: app.ID, Handle: app.Slug, ProviderRef: pr},
		Host:        githubHost,
		Kind:        gp.IdentityApp,
		DisplayName: app.Name,
	}
}

// completeCreated is leg 1: manifest code → the App's credentials.
func (p *ghProvider) completeCreated(s setupSession, code string) (gp.Identity, error) {
	// Claim the code before exchanging it: GitHub's codes are single-use and
	// the response is the only time the private key is ever shown. The claim is
	// atomic, so concurrent completions cannot both get past it.
	claimed, err := p.store.claimSetupCode(s.State)
	if err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not record setup progress")
	}
	if !claimed {
		return gp.Identity{}, gp.Errorf(gp.CodeForbidden, "this setup code was already used")
	}
	s.CodeUsed = true
	conv, err := (&ghAPI{}).convertManifest(code)
	if err != nil {
		return gp.Identity{}, err
	}
	app := appRecord{
		GitHubAppID: conv.ID, Slug: conv.Slug, Name: conv.Name, OwnerLogin: conv.Owner.Login, HTMLURL: conv.HTMLURL,
		WebhookSecret: conv.WebhookSecret, PrivateKeyPEM: conv.PEM, WebhookActive: true,
	}
	id, err := p.store.createApp(app)
	if err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not store the GitHub App")
	}
	app.ID = id
	s.AppID = id
	if err := p.store.updateSetup(s); err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not record setup progress")
	}
	// The App is created; it is installed later, per account, when a project
	// links a repository there (see spi_access.go).
	ident := identityOf(app)
	s.Completed = true
	if err := p.store.updateSetup(s); err != nil {
		return gp.Identity{}, gp.Errorf(gp.CodeInternal, "could not record setup progress")
	}
	return ident, nil
}

// convertManifest exchanges the manifest code (unauthenticated: the code is the credential).
func (a *ghAPI) convertManifest(code string) (*ghManifestConversion, error) {
	var out ghManifestConversion
	if err := a.send("POST", "/app-manifests/"+url.PathEscape(code)+"/conversions", map[string]any{}, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 || out.PEM == "" {
		return nil, gp.Errorf(gp.CodeInternal, "GitHub returned an incomplete App")
	}
	return &out, nil
}

// DeleteIdentity implements gp.IdentityDeleter: forget the App locally. The
// App itself stays on GitHub (there is no API to delete it); the operator
// removes it there.
func (p *ghProvider) DeleteIdentity(_ context.Context, req gp.DeleteIdentityRequest) (gp.Empty, error) {
	if _, err := p.loadApp(req.Identity.ID); err != nil {
		if gp.AsError(err).Code == gp.CodeNotFound {
			return gp.Empty{}, nil // already gone
		}
		return gp.Empty{}, err
	}
	if err := p.store.deleteApp(req.Identity.ID); err != nil {
		return gp.Empty{}, gp.Errorf(gp.CodeInternal, "could not delete the identity")
	}
	return gp.Empty{}, nil
}
