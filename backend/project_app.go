package main

import (
	"encoding/json"
	"strings"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// ─── GET /projects/:projectId/integration/installations ──────────────────────

// listProjectInstallations returns the app installations this project may
// bind: those open to every project plus those that name it on their
// allow-list. Suspended installations are left out.
func (p *githubPlugin) listProjectInstallations(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	r, err := p.db.Query(`
		SELECT i.id, i.account_login, i.account_type, i.repository_selection, a.name AS app_name
		FROM github_app_installations i
		JOIN github_apps a ON a.id = i.app_id
		WHERE i.suspended_at IS NULL
		  AND (i.allow_all_projects = TRUE
		       OR EXISTS (SELECT 1 FROM github_installation_projects ip
		                  WHERE ip.installation_id = i.id AND ip.project_id = $1))
		ORDER BY a.name ASC, i.account_login ASC`, projectID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	items := make([]projectInstallationResponse, 0, len(r.Rows))
	for _, row := range r.Rows {
		sc := newRowScanner(r.Columns, row)
		items = append(items, projectInstallationResponse{
			ID:                  sc.str("id"),
			AppName:             sc.str("app_name"),
			AccountLogin:        sc.str("account_login"),
			AccountType:         sc.str("account_type"),
			RepositorySelection: sc.str("repository_selection"),
		})
	}
	ok(res, items)
}

// ─── POST /projects/:projectId/integration/app ───────────────────────────────

type bindAppBody struct {
	InstallationID string `json:"installation_id"`
}

func (p *githubPlugin) bindInstallation(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	b, err := plugin.JSONBody[bindAppBody](req)
	if err != nil || b.InstallationID == "" {
		apiError(res, 400, "BAD_REQUEST", "installation_id is required")
		return
	}

	allowed, err := p.projectMayUseInstallation(projectID, b.InstallationID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if !allowed {
		apiError(res, 403, "GITHUB_INSTALLATION_NOT_ALLOWED", "This installation is not available to the project")
		return
	}

	// Changing what a connection authenticates as would orphan repositories
	// linked through the old one (their per-repo webhooks belong to the PAT).
	existing, err := p.db.Query(
		`SELECT id, auth_type, installation_id FROM github_integrations WHERE project_id = $1`, projectID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if len(existing.Rows) > 0 {
		sc := newRowScanner(existing.Columns, existing.Rows[0])
		unchanged := sc.str("auth_type") == authTypeApp && sc.str("installation_id") == b.InstallationID
		if !unchanged {
			linked, qErr := p.db.Query(`SELECT id FROM github_repositories WHERE project_id = $1`, projectID)
			if qErr != nil {
				apiError(res, 500, "INTERNAL_ERROR", qErr.Error())
				return
			}
			if len(linked.Rows) > 0 {
				apiError(res, 409, "GITHUB_UNLINK_REPOS_FIRST",
					"Unlink this project's repositories before changing how it connects to GitHub")
				return
			}
		}
	}

	// Prove the installation works (key decrypts, GitHub accepts the JWT)
	// before reporting the project as connected.
	if _, _, err := p.installationToken(b.InstallationID); err != nil {
		writeAppError(res, err)
		return
	}

	now := nowStr()
	if _, err := p.db.Exec(`
		INSERT INTO github_integrations (project_id, auth_type, installation_id, access_token_enc, created_at, updated_at)
		VALUES ($1, 'app', $2, NULL, $3, $3)
		ON CONFLICT (project_id) DO UPDATE SET
			auth_type = 'app', installation_id = EXCLUDED.installation_id,
			access_token_enc = NULL, updated_at = EXCLUDED.updated_at
	`, projectID, b.InstallationID, now); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	p.getIntegration(req, res)
}

func (p *githubPlugin) projectMayUseInstallation(projectID, installationRowID string) (bool, error) {
	r, err := p.db.Query(
		`SELECT allow_all_projects, suspended_at FROM github_app_installations WHERE id = $1`,
		installationRowID)
	if err != nil {
		return false, err
	}
	if len(r.Rows) == 0 {
		return false, nil
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	if sc.strPtr("suspended_at") != nil {
		return false, nil
	}
	if sc.str("allow_all_projects") == "true" {
		return true, nil
	}
	l, err := p.db.Query(
		`SELECT project_id FROM github_installation_projects WHERE installation_id = $1 AND project_id = $2`,
		installationRowID, projectID)
	if err != nil {
		return false, err
	}
	return len(l.Rows) > 0, nil
}

// projectAuth describes how a project is connected, for handlers whose
// behaviour differs between PAT and App connections.
type projectAuth struct {
	integrationID  string
	authType       string
	installationID string
}

func (p *githubPlugin) loadProjectAuth(projectID string) (*projectAuth, error) {
	r, err := p.db.Query(
		`SELECT id, auth_type, installation_id FROM github_integrations WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	if len(r.Rows) == 0 {
		return nil, &appError{code: "GITHUB_INTEGRATION_NOT_FOUND", status: 404, msg: "GitHub integration not found"}
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	a := &projectAuth{integrationID: sc.str("id"), authType: sc.str("auth_type"), installationID: sc.str("installation_id")}
	if a.authType == "" {
		a.authType = authTypePAT
	}
	return a, nil
}

// appWebhookActive reports whether the app behind installationRowID delivers
// webhooks (false when PUBLIC_URL was not reachable from GitHub at creation).
func (p *githubPlugin) appWebhookActive(installationRowID string) bool {
	r, err := p.db.Query(`
		SELECT a.webhook_active FROM github_app_installations i
		JOIN github_apps a ON a.id = i.app_id WHERE i.id = $1`, installationRowID)
	if err != nil || len(r.Rows) == 0 {
		return false
	}
	return newRowScanner(r.Columns, r.Rows[0]).str("webhook_active") == "true"
}

// ─── POST /webhooks/apps/:appKey ─────────────────────────────────────────────

// receiveAppWebhook handles every delivery for one GitHub App. Unlike the
// per-project PAT webhook, one App delivers for all installed repos, so the
// payload is verified once against the App's secret and then fanned out to
// every project that linked the repository through that installation.
func (p *githubPlugin) receiveAppWebhook(req *plugin.Request, res *plugin.Response) {
	appKey := req.PathParam("appKey")
	app, err := p.db.Query(`SELECT id, webhook_secret_enc FROM github_apps WHERE app_key = $1`, appKey)
	if err != nil {
		p.log.Error("github: app webhook lookup failed: " + err.Error())
		res.NoContent()
		return
	}
	if len(app.Rows) == 0 {
		apiError(res, 404, "GITHUB_APP_NOT_FOUND", "unknown app")
		return
	}
	asc := newRowScanner(app.Columns, app.Rows[0])
	secret, err := p.decrypt(asc.str("webhook_secret_enc"))
	if err != nil || secret == "" {
		p.log.Error("github: app has no usable webhook secret, refusing delivery")
		res.NoContent()
		return
	}
	if !verifyHMAC(req.Body, secret, header(req, "X-Hub-Signature-256")) {
		apiError(res, 401, "INVALID_SIGNATURE", "invalid webhook signature")
		return
	}

	event := header(req, "X-GitHub-Event")
	if err := p.handleAppEvent(asc.str("id"), event, req.Body); err != nil {
		p.log.Error("github: app webhook handler error: " + err.Error() + ", event=" + event)
	}
	// Always 2xx after authentication so GitHub does not retry on app errors.
	res.NoContent()
}

type appEventPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID                  int64      `json:"id"`
		RepositorySelection string     `json:"repository_selection"`
		SuspendedAt         *time.Time `json:"suspended_at"`
		Account             struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (p *githubPlugin) handleAppEvent(appRowID, event string, payload []byte) error {
	if event == "ping" {
		return nil
	}
	var ev appEventPayload
	if err := json.Unmarshal(payload, &ev); err != nil {
		return err
	}
	if ev.Installation.ID == 0 {
		return nil
	}

	switch event {
	case "installation":
		return p.syncInstallationEvent(appRowID, &ev)
	case "installation_repositories":
		return nil
	case "pull_request", "push":
		return p.fanOutRepoEvent(appRowID, event, &ev, payload)
	default:
		p.log.Info("github: unhandled app event type, event=" + event)
		return nil
	}
}

func (p *githubPlugin) syncInstallationEvent(appRowID string, ev *appEventPayload) error {
	switch ev.Action {
	case "created", "unsuspend", "suspend", "new_permissions_accepted":
		inst := &ghInstallation{
			ID:                  ev.Installation.ID,
			RepositorySelection: ev.Installation.RepositorySelection,
			SuspendedAt:         ev.Installation.SuspendedAt,
		}
		inst.Account.Login = ev.Installation.Account.Login
		inst.Account.Type = ev.Installation.Account.Type
		if ev.Action == "suspend" && inst.SuspendedAt == nil {
			now := time.Now().UTC()
			inst.SuspendedAt = &now
		}
		// A "created" event for an installation made outside the Paca flow
		// starts closed: an admin must opt it in before projects can use it.
		_, err := p.upsertInstallation(appRowID, inst, false)
		return err
	case "deleted":
		_, err := p.db.Exec(
			`DELETE FROM github_app_installations WHERE app_id = $1 AND installation_id = $2`,
			appRowID, ev.Installation.ID)
		return err
	}
	return nil
}

func (p *githubPlugin) fanOutRepoEvent(appRowID, event string, ev *appEventPayload, payload []byte) error {
	fullName := ev.Repository.FullName
	if fullName == "" {
		return nil
	}
	targets, err := p.db.Query(`
		SELECT r.id AS repo_id, r.project_id
		FROM github_repositories r
		JOIN github_integrations gi ON gi.id = r.integration_id
		JOIN github_app_installations i ON i.id = gi.installation_id
		WHERE i.app_id = $1 AND i.installation_id = $2 AND LOWER(r.full_name) = LOWER($3)`,
		appRowID, ev.Installation.ID, strings.TrimSpace(fullName))
	if err != nil {
		return err
	}
	var firstErr error
	for _, row := range targets.Rows {
		sc := newRowScanner(targets.Columns, row)
		var herr error
		switch event {
		case "pull_request":
			herr = p.handlePREvent(sc.str("repo_id"), sc.str("project_id"), payload)
		case "push":
			herr = p.handlePushEvent(sc.str("repo_id"), sc.str("project_id"), payload)
		}
		if herr != nil && firstErr == nil {
			firstErr = herr
		}
	}
	return firstErr
}
