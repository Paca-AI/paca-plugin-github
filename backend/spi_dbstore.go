package main

import (
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"strconv"
	"strings"
	"time"
)

// dbStore is the production spiStore: the plugin's own tables, secrets
// encrypted with the instance key, installation tokens cached encrypted.
type dbStore struct {
	p  *githubPlugin
	db sqlExecutor
}

// sqlExecutor is the part of *plugin.DB the store uses; a test can stand in for it.
type sqlExecutor interface {
	Query(sql string, params ...any) (*plugin.DBQueryResult, error)
	Exec(sql string, params ...any) (int64, error)
}

func newDBStore(p *githubPlugin) dbStore { return dbStore{p: p, db: p.db} }

func (s dbStore) loadApp(id string) (appRecord, error) {
	r, err := s.db.Query(`SELECT id, github_app_id, slug, name, owner_login, html_url, webhook_secret_enc, private_key_enc, webhook_active FROM github_apps WHERE id = $1`, id)
	if err != nil {
		return appRecord{}, err
	}
	if len(r.Rows) == 0 {
		return appRecord{}, errNoSuchApp
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	pem, err := s.p.decrypt(sc.str("private_key_enc"))
	if err != nil {
		return appRecord{}, err
	}
	secret := ""
	if enc := sc.str("webhook_secret_enc"); enc != "" {
		if secret, err = s.p.decrypt(enc); err != nil {
			return appRecord{}, err
		}
	}
	return appRecord{
		ID: sc.str("id"), GitHubAppID: sc.int64Val("github_app_id"), Slug: sc.str("slug"), Name: sc.str("name"),
		OwnerLogin: sc.str("owner_login"), HTMLURL: sc.str("html_url"), WebhookSecret: secret, PrivateKeyPEM: pem,
		WebhookActive: sc.str("webhook_active") != "false",
	}, nil
}

func (s dbStore) loadToken(id string) (tokenRecord, error) {
	r, err := s.db.Query(`SELECT id, login, name, token_enc, webhook_secret_enc FROM github_tokens WHERE id = $1`, id)
	if err != nil {
		return tokenRecord{}, err
	}
	if len(r.Rows) == 0 {
		return tokenRecord{}, errNoSuchToken
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	tok, err := s.p.decrypt(sc.str("token_enc"))
	if err != nil {
		return tokenRecord{}, err
	}
	// A token migrated from the previous release has no webhook secret yet.
	secret := ""
	if enc := sc.str("webhook_secret_enc"); enc != "" {
		if secret, err = s.p.decrypt(enc); err != nil {
			return tokenRecord{}, err
		}
	}
	return tokenRecord{ID: sc.str("id"), Login: sc.str("login"), Name: sc.str("name"), Token: tok, WebhookSecret: secret}, nil
}

func (s dbStore) createToken(t tokenRecord) (string, error) {
	tok, err := s.p.encrypt(t.Token)
	if err != nil {
		return "", err
	}
	hook, err := s.p.encrypt(t.WebhookSecret)
	if err != nil {
		return "", err
	}
	now := nowStr()
	r, err := s.db.Query(`
		INSERT INTO github_tokens (login, name, token_enc, webhook_secret_enc, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5) RETURNING id`, t.Login, t.Name, tok, hook, now)
	if err != nil {
		return "", err
	}
	if len(r.Rows) == 0 {
		return "", errNoSuchToken
	}
	return newRowScanner(r.Columns, r.Rows[0]).str("id"), nil
}

// updateToken fills a token's blanks (login, name, webhook secret) and never
// overwrites a value that is already there, so two first uses cannot disagree.
func (s dbStore) updateToken(t tokenRecord) error {
	hook := ""
	if t.WebhookSecret != "" {
		var err error
		if hook, err = s.p.encrypt(t.WebhookSecret); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`UPDATE github_tokens SET
		login = CASE WHEN login = '' THEN $2 ELSE login END,
		name = CASE WHEN login = '' THEN $3 ELSE name END,
		webhook_secret_enc = CASE WHEN webhook_secret_enc = '' THEN $4 ELSE webhook_secret_enc END,
		updated_at = $5 WHERE id = $1`,
		t.ID, t.Login, t.Name, hook, nowStr())
	return err
}

func (s dbStore) deleteToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM github_tokens WHERE id = $1`, id)
	return err
}

func (s dbStore) installations(appID string) ([]installRecord, error) {
	r, err := s.db.Query(`SELECT id, installation_id, account_login, account_type, repository_selection, suspended_at FROM github_app_installations WHERE app_id = $1`, appID)
	if err != nil {
		return nil, err
	}
	out := make([]installRecord, 0, len(r.Rows))
	for _, row := range r.Rows {
		sc := newRowScanner(r.Columns, row)
		out = append(out, installRecord{
			RowID: sc.str("id"), InstallationID: sc.int64Val("installation_id"), AccountLogin: sc.str("account_login"),
			AccountType: sc.str("account_type"), Selection: sc.str("repository_selection"), Suspended: sc.strPtr("suspended_at") != nil,
		})
	}
	return out, nil
}

func (s dbStore) createApp(a appRecord) (string, error) {
	pem, err := s.p.encrypt(a.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	hook, err := s.p.encrypt(a.WebhookSecret)
	if err != nil {
		return "", err
	}
	now := nowStr()
	r, err := s.db.Query(`
		INSERT INTO github_apps (github_app_id, slug, name, owner_login, html_url, webhook_secret_enc, private_key_enc, webhook_active, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'git-provider',$9,$9) RETURNING id`,
		a.GitHubAppID, a.Slug, a.Name, a.OwnerLogin, a.HTMLURL, hook, pem, a.WebhookActive, now)
	if err != nil {
		return "", err
	}
	if len(r.Rows) == 0 {
		return "", errNoSuchApp
	}
	return newRowScanner(r.Columns, r.Rows[0]).str("id"), nil
}

func (s dbStore) upsertInstallation(appID string, i installRecord) error {
	var suspended any
	if i.Suspended {
		suspended = nowStr()
	}
	now := nowStr()
	_, err := s.db.Exec(`
		INSERT INTO github_app_installations (app_id, installation_id, account_login, account_type, repository_selection, suspended_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (app_id, installation_id) DO UPDATE SET
			account_login = EXCLUDED.account_login, account_type = EXCLUDED.account_type,
			repository_selection = EXCLUDED.repository_selection, suspended_at = EXCLUDED.suspended_at, updated_at = EXCLUDED.updated_at`,
		appID, i.InstallationID, i.AccountLogin, i.AccountType, i.Selection, suspended, now)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM github_app_installations WHERE app_id = $1 AND lower(account_login) = lower($2) AND installation_id <> $3`,
		appID, i.AccountLogin, i.InstallationID)
	return err
}

func (s dbStore) deleteApp(id string) error {
	_, err := s.db.Exec(`DELETE FROM github_apps WHERE id = $1`, id)
	return err
}

func (s dbStore) deleteInstallation(appID string, installationID int64) error {
	_, err := s.db.Exec(`DELETE FROM github_app_installations WHERE app_id = $1 AND installation_id = $2`, appID, installationID)
	return err
}

func (s dbStore) saveSetup(x setupSession) error {
	// app_id is set for an access grant (it belongs to an existing App) and
	// stays NULL for an identity setup until the manifest code is exchanged.
	var appID any
	if x.AppID != "" {
		appID = x.AppID
	}
	_, err := s.db.Exec(`
		INSERT INTO github_manifest_sessions (state, created_by, app_name, owner_org, scope, kind, agent_id, app_id, expires_at, created_at)
		VALUES ($1,'git-provider',$2,$3,$4,$5,$6,$7,$8,$9)`,
		x.State, x.AppName, x.OwnerOrg, x.Scope, x.Kind, x.AgentID, appID, x.ExpiresAt.UTC().Format(time.RFC3339Nano), nowStr())
	return err
}

func (s dbStore) loadSetup(state string) (setupSession, error) {
	r, err := s.db.Query(`SELECT state, app_name, owner_org, scope, kind, agent_id, app_id, code_used_at, completed_at, expires_at FROM github_manifest_sessions WHERE state = $1`, state)
	if err != nil {
		return setupSession{}, err
	}
	if len(r.Rows) == 0 {
		return setupSession{}, errNoSuchSetup
	}
	sc := newRowScanner(r.Columns, r.Rows[0])
	exp, _ := time.Parse(time.RFC3339Nano, sc.str("expires_at"))
	return setupSession{
		State: sc.str("state"), Scope: sc.str("scope"), Kind: sc.str("kind"), AgentID: sc.str("agent_id"),
		AppName: sc.str("app_name"), OwnerOrg: sc.str("owner_org"), AppID: sc.str("app_id"),
		CodeUsed: sc.strPtr("code_used_at") != nil, Completed: sc.strPtr("completed_at") != nil, ExpiresAt: exp,
	}, nil
}

func (s dbStore) updateSetup(x setupSession) error {
	var appID, codeUsed, completed any
	if x.AppID != "" {
		appID = x.AppID
	}
	if x.CodeUsed {
		codeUsed = nowStr()
	}
	if x.Completed {
		completed = nowStr()
	}
	_, err := s.db.Exec(`UPDATE github_manifest_sessions SET app_id = $2, code_used_at = COALESCE(code_used_at, $3), completed_at = COALESCE(completed_at, $4) WHERE state = $1`,
		x.State, appID, codeUsed, completed)
	return err
}

func (s dbStore) claimSetupCode(state string) (bool, error) {
	n, err := s.db.Exec(`UPDATE github_manifest_sessions SET code_used_at = $2 WHERE state = $1 AND code_used_at IS NULL`, state, nowStr())
	return n == 1, err
}

func (s dbStore) cacheGet(key string) (string, time.Time, bool) {
	if s.p.cache == nil {
		return "", time.Time{}, false
	}
	enc, hit := s.p.cache.Get(key)
	if !hit || enc == "" {
		return "", time.Time{}, false
	}
	raw, err := s.p.decrypt(enc)
	if err != nil {
		return "", time.Time{}, false
	}
	tok, ts, ok := strings.Cut(raw, "|")
	unix, perr := strconv.ParseInt(ts, 10, 64)
	if !ok || perr != nil {
		return "", time.Time{}, false
	}
	return tok, time.Unix(unix, 0), true
}

func (s dbStore) cacheSet(key, token string, expires time.Time) {
	if s.p.cache == nil {
		return
	}
	ttl := time.Until(expires) - installationTokenReuseMargin
	if ttl <= 0 {
		return
	}
	if enc, err := s.p.encrypt(token + "|" + strconv.FormatInt(expires.Unix(), 10)); err == nil {
		s.p.cache.Set(key, enc, ttl)
	}
}

func (s dbStore) cacheDelete(key string) {
	if s.p.cache != nil {
		s.p.cache.Delete(key)
	}
}

var _ spiStore = dbStore{}
