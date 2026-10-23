package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// GitHub App management: the Manifest Flow that lets an administrator create
// a Paca GitHub App on their own account/org with a couple of clicks, the
// installation callback, and the (instance-level) catalogue of apps and
// installations that projects bind to.
//
//	POST /apps/manifest        → manifest + GitHub URL for the browser to POST
//	GET  /apps/callback/created   (GitHub redirect: manifest code → credentials)
//	GET  /apps/callback/installed (GitHub redirect: installation_id)

const (
	// callbackPageSlug is the slug of the plugin's app.page that GitHub
	// redirects back to (plugin.json navItems).
	callbackPageSlug = "github-app-callback"

	manifestSessionTTL = time.Hour

	// githubAppNameMaxLen is GitHub's limit on an App's name.
	githubAppNameMaxLen = 34
)

var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// manifestPermissions is requested up front as a superset of what the plugin
// uses today and what the planned PR-lifecycle tools need: widening an App's
// permissions later forces an org owner to re-approve it.
var manifestPermissions = map[string]string{
	"metadata":      "read",
	"contents":      "write",
	"pull_requests": "write",
	"issues":        "write",
	"checks":        "read",
	"statuses":      "read",
	"actions":       "read",
}

var manifestEvents = []string{
	"push",
	"pull_request",
	"pull_request_review",
	"pull_request_review_comment",
	"pull_request_review_thread",
	"issue_comment",
	"check_run",
	"check_suite",
}

// ─── DTOs ────────────────────────────────────────────────────────────────────

type manifestResponse struct {
	PostURL       string         `json:"post_url"`
	State         string         `json:"state"`
	Manifest      map[string]any `json:"manifest"`
	WebhookActive bool           `json:"webhook_active"`
}

type installationResponse struct {
	ID                  string   `json:"id"`
	InstallationID      int64    `json:"installation_id"`
	AccountLogin        string   `json:"account_login"`
	AccountType         string   `json:"account_type"`
	RepositorySelection string   `json:"repository_selection"`
	AllowAllProjects    bool     `json:"allow_all_projects"`
	ProjectIDs          []string `json:"project_ids"`
	Suspended           bool     `json:"suspended"`
	ManageURL           string   `json:"manage_url"`
}

type appResponse struct {
	ID            string                 `json:"id"`
	GitHubAppID   int64                  `json:"github_app_id"`
	Slug          string                 `json:"slug"`
	Name          string                 `json:"name"`
	OwnerLogin    string                 `json:"owner_login"`
	HTMLURL       string                 `json:"html_url"`
	WebhookActive bool                   `json:"webhook_active"`
	CreatedAt     string                 `json:"created_at"`
	Installations []installationResponse `json:"installations"`
}

// projectInstallationResponse is what a project manager sees when choosing an
// installation: no secrets and no information about other projects.
type projectInstallationResponse struct {
	ID                  string `json:"id"`
	AppName             string `json:"app_name"`
	AccountLogin        string `json:"account_login"`
	AccountType         string `json:"account_type"`
	RepositorySelection string `json:"repository_selection"`
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// publicBaseURL returns PUBLIC_URL without a trailing slash.
func (p *githubPlugin) publicBaseURL() (string, error) {
	publicURL, ok := p.cfg.Get("PUBLIC_URL")
	if !ok || strings.TrimSpace(publicURL) == "" {
		return "", errors.New("PUBLIC_URL is not configured")
	}
	return strings.TrimRight(strings.TrimSpace(publicURL), "/"), nil
}

func (p *githubPlugin) pluginBaseURL() (string, error) {
	base, err := p.publicBaseURL()
	if err != nil {
		return "", err
	}
	return base + "/api/v1/plugins/" + githubPluginID, nil
}

// isLocalURL reports whether rawURL points at a host GitHub cannot reach from
// the internet (loopback, private ranges, .local/.internal names), in which
// case webhook delivery is switched off rather than left failing.
func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "", host == "localhost", host == "::1", host == "0.0.0.0":
		return true
	case strings.HasSuffix(host, ".local"), strings.HasSuffix(host, ".internal"),
		strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".lan"):
		return true
	case strings.HasPrefix(host, "127."), strings.HasPrefix(host, "10."), strings.HasPrefix(host, "192.168."):
		return true
	case strings.HasPrefix(host, "172."):
		parts := strings.Split(host, ".")
		if len(parts) > 1 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n >= 16 && n <= 31 {
				return true
			}
		}
	}
	return false
}

// sameOrigin reports whether candidate shares scheme and host with base.
func sameOrigin(base, candidate string) bool {
	b, err1 := url.Parse(base)
	c, err2 := url.Parse(candidate)
	if err1 != nil || err2 != nil || c.Scheme == "" || c.Host == "" {
		return false
	}
	return strings.EqualFold(b.Scheme, c.Scheme) && strings.EqualFold(b.Host, c.Host)
}

// header reads a request header regardless of the key casing the host uses.
func header(req *plugin.Request, name string) string {
	for k, v := range req.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// ─── POST /apps/manifest ─────────────────────────────────────────────────────

type createManifestBody struct {
	Name             string `json:"name"`
	OwnerOrg         string `json:"owner_org"`
	AllowAllProjects bool   `json:"allow_all_projects"`
	Public           bool   `json:"public"`
	ReturnURL        string `json:"return_url"`
}

func (p *githubPlugin) createManifest(req *plugin.Request, res *plugin.Response) {
	b, err := plugin.JSONBody[createManifestBody](req)
	if err != nil {
		apiError(res, 400, "BAD_REQUEST", "invalid request body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" || len(name) > githubAppNameMaxLen {
		apiError(res, 400, "BAD_REQUEST", fmt.Sprintf("name is required and must be at most %d characters", githubAppNameMaxLen))
		return
	}
	org := strings.TrimSpace(b.OwnerOrg)
	if org != "" && !githubLoginRe.MatchString(org) {
		apiError(res, 400, "BAD_REQUEST", "owner_org is not a valid GitHub organization name")
		return
	}

	pluginBase, err := p.pluginBaseURL()
	if err != nil {
		apiError(res, 422, "GITHUB_WEBHOOK_URL_REQUIRED", "PUBLIC_URL is not configured")
		return
	}
	publicBase, _ := p.publicBaseURL()
	returnURL := strings.TrimSpace(b.ReturnURL)
	if returnURL != "" && !sameOrigin(publicBase, returnURL) {
		apiError(res, 400, "BAD_REQUEST", "return_url must be on this Paca instance")
		return
	}

	state, err := randomHex(24)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", "failed to generate state")
		return
	}
	appKey, err := randomHex(16)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", "failed to generate app key")
		return
	}

	now := time.Now().UTC()
	if _, err := p.db.Exec(`
		INSERT INTO github_manifest_sessions
			(state, app_key, created_by, app_name, owner_org, allow_all_projects, return_url, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, state, appKey, req.Caller.UserID, name, org, b.AllowAllProjects, returnURL,
		now.Add(manifestSessionTTL).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}

	webhookActive := !isLocalURL(publicBase)
	manifest := buildManifest(name, publicBase, pluginBase, appKey, webhookActive, b.Public)

	postURL := "https://github.com/settings/apps/new"
	if org != "" {
		postURL = "https://github.com/organizations/" + org + "/settings/apps/new"
	}
	postURL += "?state=" + url.QueryEscape(state)

	ok(res, manifestResponse{PostURL: postURL, State: state, Manifest: manifest, WebhookActive: webhookActive})
}

// buildManifest assembles the GitHub App manifest. The webhook URL carries the
// app_key so deliveries can be verified before the numeric app id is known.
func buildManifest(name, publicBase, pluginBase, appKey string, webhookActive, public bool) map[string]any {
	return map[string]any{
		"name": name,
		"url":  publicBase,
		"hook_attributes": map[string]any{
			"url":    pluginBase + "/webhooks/apps/" + appKey,
			"active": webhookActive,
		},
		// Both legs return to the same Paca web-app page (the plugin's app.page);
		// it tells them apart by the parameters GitHub appends (code vs
		// installation_id).
		"redirect_url":        publicBase + "/plugin-pages/" + githubPluginID + "/" + callbackPageSlug,
		"setup_url":           publicBase + "/plugin-pages/" + githubPluginID + "/" + callbackPageSlug,
		"setup_on_update":     true,
		"public":              public,
		"default_permissions": manifestPermissions,
		"default_events":      manifestEvents,
	}
}

// ─── POST /apps/callback/created ─────────────────────────────────────────────

// The two callbacks are not hit by GitHub directly. GitHub redirects the
// browser to a page of the Paca web app (the plugin's app.page, see
// frontend/src/GitHubAppCallbackPage.tsx), which restores the user's session
// if its short-lived cookie lapsed during the detour and then calls these
// routes with the parameters GitHub appended. So they are ordinary
// authenticated, permission-gated JSON routes, and the manifest session is
// additionally bound to the user who started it.

// Stable codes the callback page turns into messages.
const (
	setupInvalidState       = "GITHUB_SETUP_INVALID_STATE"
	setupMissingParam       = "GITHUB_SETUP_MISSING_PARAMETER"
	setupConversionFailed   = "GITHUB_SETUP_CONVERSION_FAILED"
	setupStoreFailed        = "GITHUB_SETUP_STORE_FAILED"
	setupInstallationFailed = "GITHUB_SETUP_INSTALLATION_FAILED"
)

// callbackResult tells the page where to send the browser next.
type callbackResult struct {
	// NextURL, when set, is the GitHub URL for the next leg of the flow.
	NextURL string `json:"next_url,omitempty"`
	// ReturnURL is the Paca page that started the flow.
	ReturnURL string `json:"return_url"`
}

// loadManifestSession returns the live (unexpired) session for state, bound to
// the user who started it: a state that leaks (a URL in a log, a screenshot)
// is useless to anyone else.
func (p *githubPlugin) loadManifestSession(state, userID string) (*scanner, error) {
	if state == "" {
		return nil, errors.New("missing state")
	}
	r, err := p.db.Query(
		`SELECT state, app_key, created_by, app_name, owner_org, allow_all_projects, return_url, app_id, code_used_at, completed_at, expires_at FROM github_manifest_sessions WHERE state = $1`,
		state)
	if err != nil {
		return nil, err
	}
	if len(r.Rows) == 0 {
		return nil, errors.New("unknown state")
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	if userID == "" || sc.str("created_by") != userID {
		return nil, errors.New("state belongs to another user")
	}
	exp, perr := time.Parse(time.RFC3339Nano, sc.str("expires_at"))
	if perr != nil || time.Now().After(exp) {
		return nil, errors.New("state expired")
	}
	return sc, nil
}

// returnTarget is where the browser goes when the flow ends.
func (p *githubPlugin) returnTarget(sess *scanner) string {
	if u := sess.str("return_url"); u != "" {
		return u
	}
	base, _ := p.publicBaseURL()
	return base + "/home"
}

type createdCallbackBody struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

func (p *githubPlugin) callbackCreated(req *plugin.Request, res *plugin.Response) {
	b, err := plugin.JSONBody[createdCallbackBody](req)
	if err != nil || b.State == "" || b.Code == "" {
		apiError(res, 400, setupMissingParam, "code and state are required")
		return
	}
	sess, err := p.loadManifestSession(b.State, req.Caller.UserID)
	if err != nil {
		apiError(res, 400, setupInvalidState, "The setup link is invalid, expired or belongs to another user")
		return
	}

	// Claim the code atomically so a replayed callback cannot create a second app.
	claimed, err := p.db.Exec(
		`UPDATE github_manifest_sessions SET code_used_at = $1 WHERE state = $2 AND code_used_at IS NULL`,
		nowStr(), b.State)
	if err != nil || claimed == 0 {
		apiError(res, 409, setupInvalidState, "This setup link was already used")
		return
	}

	conv, err := newGHClient("").convertManifestCode(context.Background(), b.Code)
	if err != nil {
		p.log.Error("github: manifest conversion failed: " + err.Error())
		apiError(res, 502, setupConversionFailed, "GitHub rejected the app setup")
		return
	}

	appRowID, err := p.storeApp(sess, conv, req.Caller.UserID)
	if err != nil {
		p.log.Error("github: storing app failed: " + err.Error())
		apiError(res, 500, setupStoreFailed, "The app was created on GitHub but could not be saved")
		return
	}
	if _, err := p.db.Exec(`UPDATE github_manifest_sessions SET app_id = $1 WHERE state = $2`, appRowID, b.State); err != nil {
		apiError(res, 500, setupStoreFailed, "The app was created on GitHub but could not be saved")
		return
	}

	// Leg two: send the user on to install the freshly created app.
	ok(res, callbackResult{NextURL: installURL(conv.Slug, b.State), ReturnURL: p.returnTarget(sess)})
}

func installURL(slug, state string) string {
	return "https://github.com/apps/" + slug + "/installations/new?state=" + url.QueryEscape(state)
}

func (p *githubPlugin) storeApp(sess *scanner, conv *ghManifestConversion, userID string) (string, error) {
	if conv.ID == 0 || conv.PEM == "" {
		return "", errors.New("conversion response is missing the app id or private key")
	}
	// Refuse to store a key we could not later sign with.
	if _, err := parseRSAPrivateKey(conv.PEM); err != nil {
		return "", err
	}
	pemEnc, err := p.encrypt(conv.PEM)
	if err != nil {
		return "", err
	}
	secretEnc, err := p.encrypt(conv.ClientSecret)
	if err != nil {
		return "", err
	}
	hookEnc, err := p.encrypt(conv.WebhookSecret)
	if err != nil {
		return "", err
	}
	publicBase, _ := p.publicBaseURL()
	now := nowStr()
	ins, err := p.db.Query(`
		INSERT INTO github_apps
			(app_key, github_app_id, slug, name, owner_login, html_url, client_id, client_secret_enc,
			 webhook_secret_enc, private_key_enc, webhook_active, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		RETURNING id`,
		sess.str("app_key"), conv.ID, conv.Slug, conv.Name, conv.Owner.Login, conv.HTMLURL, conv.ClientID,
		secretEnc, hookEnc, pemEnc, !isLocalURL(publicBase), userID, now)
	if err != nil {
		return "", err
	}
	if len(ins.Rows) == 0 {
		return "", errors.New("insert returned no rows")
	}
	return newRowScanner(ins.Columns, ins.Rows[0]).str("id"), nil
}

// ─── POST /apps/callback/installed ───────────────────────────────────────────

type installedCallbackBody struct {
	InstallationID string `json:"installation_id"`
	State          string `json:"state"`
}

func (p *githubPlugin) callbackInstalled(req *plugin.Request, res *plugin.Response) {
	b, err := plugin.JSONBody[installedCallbackBody](req)
	installationID, perr := strconv.ParseInt(strings.TrimSpace(b.InstallationID), 10, 64)
	if err != nil || b.State == "" || perr != nil || installationID <= 0 {
		apiError(res, 400, setupMissingParam, "installation_id and state are required")
		return
	}
	sess, err := p.loadManifestSession(b.State, req.Caller.UserID)
	if err != nil || sess.str("app_id") == "" {
		apiError(res, 400, setupInvalidState, "The setup link is invalid, expired or belongs to another user")
		return
	}
	claimed, err := p.db.Exec(
		`UPDATE github_manifest_sessions SET completed_at = $1 WHERE state = $2 AND completed_at IS NULL`,
		nowStr(), b.State)
	if err != nil || claimed == 0 {
		apiError(res, 409, setupInvalidState, "This setup link was already used")
		return
	}

	appRowID := sess.str("app_id")
	jwt, err := p.appJWT(appRowID)
	if err != nil {
		apiError(res, 500, setupInstallationFailed, "The installation could not be verified")
		return
	}
	inst, err := newGHClient(jwt).getInstallation(context.Background(), installationID)
	if err != nil {
		p.log.Error("github: fetching installation failed: " + err.Error())
		apiError(res, 502, setupInstallationFailed, "GitHub did not confirm the installation")
		return
	}
	if _, err := p.upsertInstallation(appRowID, inst, sess.str("allow_all_projects") == "true"); err != nil {
		p.log.Error("github: storing installation failed: " + err.Error())
		apiError(res, 500, setupStoreFailed, "The installation could not be saved")
		return
	}
	ok(res, callbackResult{ReturnURL: p.returnTarget(sess)})
}

// upsertInstallation records an installation. allowAll only applies when the
// installation is first created; later syncs never widen access.
func (p *githubPlugin) upsertInstallation(appRowID string, inst *ghInstallation, allowAll bool) (string, error) {
	var suspended any
	if inst.SuspendedAt != nil {
		suspended = inst.SuspendedAt.UTC().Format(time.RFC3339Nano)
	}
	now := nowStr()
	r, err := p.db.Query(`
		INSERT INTO github_app_installations
			(app_id, installation_id, account_login, account_type, repository_selection,
			 allow_all_projects, suspended_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (app_id, installation_id) DO UPDATE SET
			account_login = EXCLUDED.account_login,
			account_type = EXCLUDED.account_type,
			repository_selection = EXCLUDED.repository_selection,
			suspended_at = EXCLUDED.suspended_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id`,
		appRowID, inst.ID, inst.Account.Login, inst.Account.Type, inst.RepositorySelection, allowAll, suspended, now)
	if err != nil {
		return "", err
	}
	if len(r.Rows) == 0 {
		return "", errors.New("upsert returned no rows")
	}
	return newRowScanner(r.Columns, r.Rows[0]).str("id"), nil
}

// ─── GET /apps ───────────────────────────────────────────────────────────────

func (p *githubPlugin) listApps(_ *plugin.Request, res *plugin.Response) {
	apps, err := p.db.Query(`
		SELECT id, github_app_id, slug, name, owner_login, html_url, webhook_active, created_at
		FROM github_apps ORDER BY created_at ASC`)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	insts, err := p.db.Query(`
		SELECT id, app_id, installation_id, account_login, account_type, repository_selection,
		       allow_all_projects, suspended_at
		FROM github_app_installations ORDER BY created_at ASC`)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	links, err := p.db.Query(`SELECT installation_id, project_id FROM github_installation_projects`)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	projectsByInst := map[string][]string{}
	for _, row := range links.Rows {
		sc := newRowScanner(links.Columns, row)
		k := sc.str("installation_id")
		projectsByInst[k] = append(projectsByInst[k], sc.str("project_id"))
	}

	items := make([]appResponse, 0, len(apps.Rows))
	index := map[string]int{}
	for _, row := range apps.Rows {
		sc := newRowScanner(apps.Columns, row)
		a := appResponse{
			ID:            sc.str("id"),
			GitHubAppID:   sc.int64Val("github_app_id"),
			Slug:          sc.str("slug"),
			Name:          sc.str("name"),
			OwnerLogin:    sc.str("owner_login"),
			HTMLURL:       sc.str("html_url"),
			WebhookActive: sc.str("webhook_active") == "true",
			CreatedAt:     sc.str("created_at"),
			Installations: []installationResponse{},
		}
		index[a.ID] = len(items)
		items = append(items, a)
	}
	for _, row := range insts.Rows {
		sc := newRowScanner(insts.Columns, row)
		i, found := index[sc.str("app_id")]
		if !found {
			continue
		}
		id := sc.str("id")
		pids := projectsByInst[id]
		if pids == nil {
			pids = []string{}
		}
		items[i].Installations = append(items[i].Installations, installationResponse{
			ID:                  id,
			InstallationID:      sc.int64Val("installation_id"),
			AccountLogin:        sc.str("account_login"),
			AccountType:         sc.str("account_type"),
			RepositorySelection: sc.str("repository_selection"),
			AllowAllProjects:    sc.str("allow_all_projects") == "true",
			ProjectIDs:          pids,
			Suspended:           sc.strPtr("suspended_at") != nil,
			ManageURL:           installationManageURL(sc.str("account_type"), sc.str("account_login"), sc.int64Val("installation_id")),
		})
	}
	ok(res, items)
}

func installationManageURL(accountType, login string, installationID int64) string {
	if strings.EqualFold(accountType, "Organization") {
		return fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", login, installationID)
	}
	return fmt.Sprintf("https://github.com/settings/installations/%d", installationID)
}

// ─── DELETE /apps/:appId ─────────────────────────────────────────────────────

// deleteApp forgets an app locally. Projects bound to its installations fall
// back to "not connected"; the App itself must be deleted on GitHub (there is
// no API for it), which the UI links to.
func (p *githubPlugin) deleteApp(req *plugin.Request, res *plugin.Response) {
	appID := req.PathParam("appId")
	n, err := p.db.Exec(`DELETE FROM github_apps WHERE id = $1`, appID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if n == 0 {
		apiError(res, 404, "GITHUB_APP_NOT_FOUND", "GitHub App not found")
		return
	}
	noContent(res)
}

// ─── POST /apps/:appId/install ───────────────────────────────────────────────

// startInstall returns the GitHub URL for installing an existing app on
// another account/org, with a fresh state so the callback is attributable.
func (p *githubPlugin) startInstall(req *plugin.Request, res *plugin.Response) {
	appID := req.PathParam("appId")
	b, _ := plugin.JSONBody[createManifestBody](req)

	publicBase, err := p.publicBaseURL()
	if err != nil {
		apiError(res, 422, "GITHUB_WEBHOOK_URL_REQUIRED", "PUBLIC_URL is not configured")
		return
	}
	returnURL := strings.TrimSpace(b.ReturnURL)
	if returnURL != "" && !sameOrigin(publicBase, returnURL) {
		apiError(res, 400, "BAD_REQUEST", "return_url must be on this Paca instance")
		return
	}
	app, err := p.db.Query(`SELECT slug, app_key, name FROM github_apps WHERE id = $1`, appID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if len(app.Rows) == 0 {
		apiError(res, 404, "GITHUB_APP_NOT_FOUND", "GitHub App not found")
		return
	}
	asc := newRowScanner(app.Columns, app.Rows[0])
	state, err := randomHex(24)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", "failed to generate state")
		return
	}
	now := time.Now().UTC()
	if _, err := p.db.Exec(`
		INSERT INTO github_manifest_sessions
			(state, app_key, created_by, app_name, allow_all_projects, return_url, app_id, code_used_at, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)
	`, state, asc.str("app_key"), req.Caller.UserID, asc.str("name"), b.AllowAllProjects, returnURL, appID,
		now.Format(time.RFC3339Nano), now.Add(manifestSessionTTL).Format(time.RFC3339Nano)); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	ok(res, map[string]string{"url": installURL(asc.str("slug"), state)})
}

// ─── POST /apps/:appId/installations/:installationId/access ──────────────────

type installationAccessBody struct {
	AllowAllProjects bool     `json:"allow_all_projects"`
	ProjectIDs       []string `json:"project_ids"`
}

func (p *githubPlugin) setInstallationAccess(req *plugin.Request, res *plugin.Response) {
	appID := req.PathParam("appId")
	instID := req.PathParam("installationId")
	b, err := plugin.JSONBody[installationAccessBody](req)
	if err != nil {
		apiError(res, 400, "BAD_REQUEST", "invalid request body")
		return
	}
	exists, err := p.db.Query(`SELECT id FROM github_app_installations WHERE id = $1 AND app_id = $2`, instID, appID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if len(exists.Rows) == 0 {
		apiError(res, 404, "GITHUB_INSTALLATION_NOT_FOUND", "GitHub App installation not found")
		return
	}
	if _, err := p.db.Exec(
		`UPDATE github_app_installations SET allow_all_projects = $1, updated_at = $2 WHERE id = $3`,
		b.AllowAllProjects, nowStr(), instID); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if _, err := p.db.Exec(`DELETE FROM github_installation_projects WHERE installation_id = $1`, instID); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	for _, pid := range b.ProjectIDs {
		if _, err := p.db.Exec(
			`INSERT INTO github_installation_projects (installation_id, project_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			instID, pid); err != nil {
			apiError(res, 400, "BAD_REQUEST", "unknown project_id: "+pid)
			return
		}
	}
	noContent(res)
}
