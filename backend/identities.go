package main

import (
	"strings"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// Per-agent GitHub identity: inside an App-connected project, an account (in
// practice an AI agent) can be mapped to its own installation so everything it
// does on GitHub — branches, PRs, reviews, comments — is attributed to that
// app's bot instead of the project's default identity.

type identityResponse struct {
	MemberID       string `json:"member_id"`
	InstallationID string `json:"installation_id"`
	AppName        string `json:"app_name"`
	AccountLogin   string `json:"account_login"`
}

// ─── GET /integration/agent-identities ───────────────────────────────────────

func (p *githubPlugin) listAgentIdentities(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	r, err := p.db.Query(`
		SELECT m.id AS member_id, ai.installation_id, i.account_login, a.name AS app_name
		FROM github_agent_identities ai
		JOIN project_members m ON m.account_id = ai.account_id AND m.project_id = ai.project_id AND m.deleted_at IS NULL
		JOIN github_app_installations i ON i.id = ai.installation_id
		JOIN github_apps a ON a.id = i.app_id
		WHERE ai.project_id = $1
		ORDER BY a.name ASC, i.account_login ASC`, projectID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	items := make([]identityResponse, 0, len(r.Rows))
	for _, row := range r.Rows {
		sc := newRowScanner(r.Columns, row)
		items = append(items, identityResponse{
			MemberID:       sc.str("member_id"),
			InstallationID: sc.str("installation_id"),
			AppName:        sc.str("app_name"),
			AccountLogin:   sc.str("account_login"),
		})
	}
	ok(res, items)
}

// ─── POST /integration/agent-identities ──────────────────────────────────────

type setIdentityBody struct {
	MemberID       string `json:"member_id"`
	InstallationID string `json:"installation_id"`
}

func (p *githubPlugin) setAgentIdentity(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	b, err := plugin.JSONBody[setIdentityBody](req)
	if err != nil || strings.TrimSpace(b.MemberID) == "" || strings.TrimSpace(b.InstallationID) == "" {
		apiError(res, 400, "BAD_REQUEST", "member_id and installation_id are required")
		return
	}

	pa, err := p.loadProjectAuth(projectID)
	if err != nil {
		writeAppError(res, err)
		return
	}
	if pa.authType != authTypeApp {
		apiError(res, 409, "GITHUB_APP_REQUIRED", "Per-agent identities need the project to be connected through a GitHub App")
		return
	}

	accountID, found, err := p.memberAccountID(projectID, b.MemberID)
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if !found {
		apiError(res, 404, "GITHUB_MEMBER_NOT_FOUND", "Project member not found")
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
	// Prove the identity works before an agent depends on it.
	if _, _, err := p.installationToken(b.InstallationID); err != nil {
		writeAppError(res, err)
		return
	}

	now := nowStr()
	if _, err := p.db.Exec(`
		INSERT INTO github_agent_identities (project_id, account_id, installation_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (project_id, account_id) DO UPDATE SET
			installation_id = EXCLUDED.installation_id, updated_at = EXCLUDED.updated_at
	`, projectID, accountID, b.InstallationID, now); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	p.listAgentIdentities(req, res)
}

// ─── DELETE /integration/agent-identities/:memberId ──────────────────────────

func (p *githubPlugin) deleteAgentIdentity(req *plugin.Request, res *plugin.Response) {
	projectID := req.Caller.ProjectID
	accountID, found, err := p.memberAccountID(projectID, req.PathParam("memberId"))
	if err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if !found {
		apiError(res, 404, "GITHUB_MEMBER_NOT_FOUND", "Project member not found")
		return
	}
	if _, err := p.db.Exec(
		`DELETE FROM github_agent_identities WHERE project_id = $1 AND account_id = $2`,
		projectID, accountID); err != nil {
		apiError(res, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	noContent(res)
}

// memberAccountID resolves a project membership id to its account id,
// scoped to the project so a member of another project can never be named.
func (p *githubPlugin) memberAccountID(projectID, memberID string) (string, bool, error) {
	r, err := p.db.Query(
		`SELECT account_id FROM project_members WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`,
		memberID, projectID)
	if err != nil {
		return "", false, err
	}
	if len(r.Rows) == 0 {
		return "", false, nil
	}
	id := newRowScanner(r.Columns, r.Rows[0]).str("account_id")
	return id, id != "", nil
}
