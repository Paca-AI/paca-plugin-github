-- 0002_github_apps.sql
-- Adds instance-level GitHub Apps (created through the GitHub App Manifest
-- flow), their installations, and the per-project binding to an installation.
-- Run with search_path = plugin_data_com_paca_github, public.

CREATE TABLE IF NOT EXISTS github_apps (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Random token embedded in the app's webhook URL. It is generated before
    -- GitHub assigns the numeric app id, so it is what routes a delivery to
    -- the right webhook secret.
    app_key           TEXT        NOT NULL UNIQUE,
    github_app_id     BIGINT      NOT NULL,
    slug              TEXT        NOT NULL,
    name              TEXT        NOT NULL,
    owner_login       TEXT        NOT NULL DEFAULT '',
    html_url          TEXT        NOT NULL DEFAULT '',
    client_id         TEXT        NOT NULL DEFAULT '',
    client_secret_enc TEXT        NOT NULL DEFAULT '',
    webhook_secret_enc TEXT       NOT NULL DEFAULT '',
    private_key_enc   TEXT        NOT NULL,
    webhook_active    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by        TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS github_app_installations (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id             UUID        NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
    installation_id    BIGINT      NOT NULL,
    account_login      TEXT        NOT NULL DEFAULT '',
    account_type       TEXT        NOT NULL DEFAULT '',
    repository_selection TEXT      NOT NULL DEFAULT 'selected',
    -- When false, only the projects listed in github_installation_projects may
    -- bind this installation.
    allow_all_projects BOOLEAN     NOT NULL DEFAULT FALSE,
    suspended_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (app_id, installation_id)
);

CREATE TABLE IF NOT EXISTS github_installation_projects (
    installation_id UUID NOT NULL REFERENCES github_app_installations(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (installation_id, project_id)
);

-- One row per manifest-flow attempt. state is the single-use CSRF nonce that
-- GitHub echoes back on both redirects.
CREATE TABLE IF NOT EXISTS github_manifest_sessions (
    state              TEXT        PRIMARY KEY,
    app_key            TEXT        NOT NULL,
    created_by         TEXT        NOT NULL,
    app_name           TEXT        NOT NULL DEFAULT '',
    owner_org          TEXT        NOT NULL DEFAULT '',
    allow_all_projects BOOLEAN     NOT NULL DEFAULT FALSE,
    return_url         TEXT        NOT NULL DEFAULT '',
    -- Set once the manifest code has been exchanged; the second leg
    -- (installation) then runs against this app.
    app_id             UUID        REFERENCES github_apps(id) ON DELETE CASCADE,
    code_used_at       TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A project is connected either by PAT (access_token_enc) or by binding an
-- app installation.
ALTER TABLE github_integrations ALTER COLUMN access_token_enc DROP NOT NULL;
ALTER TABLE github_integrations ADD COLUMN IF NOT EXISTS auth_type TEXT NOT NULL DEFAULT 'pat';
ALTER TABLE github_integrations
    ADD COLUMN IF NOT EXISTS installation_id UUID REFERENCES github_app_installations(id) ON DELETE SET NULL;
