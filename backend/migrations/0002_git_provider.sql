-- 0002_git_provider.sql
-- The GitHub provider's own state: instance- or agent-level GitHub Apps
-- (created through the GitHub App manifest flow, key material encrypted), the
-- accounts they are installed on, and the setup sessions that tie the
-- manifest flow's redirects together. Drops the previous release's
-- project-bound tables (see the end).
-- Run with search_path = plugin_data_com_paca_github, public.

CREATE TABLE IF NOT EXISTS github_apps (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    github_app_id     BIGINT      NOT NULL,
    slug              TEXT        NOT NULL,
    name              TEXT        NOT NULL,
    owner_login       TEXT        NOT NULL DEFAULT '',
    html_url          TEXT        NOT NULL DEFAULT '',
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
    suspended_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (app_id, installation_id)
);

-- One row per manifest-flow attempt. state is the single-use CSRF nonce that
-- GitHub echoes back on both redirects.
CREATE TABLE IF NOT EXISTS github_manifest_sessions (
    state              TEXT        PRIMARY KEY,
    created_by         TEXT        NOT NULL,
    app_name           TEXT        NOT NULL DEFAULT '',
    owner_org          TEXT        NOT NULL DEFAULT '',
    -- Whom the identity is for: the shared one, or one agent's own.
    scope              TEXT        NOT NULL DEFAULT 'shared',
    agent_id           TEXT        NOT NULL DEFAULT '',
    -- Set once the manifest code has been exchanged; the second leg
    -- (installation) then runs against this app.
    app_id             UUID        REFERENCES github_apps(id) ON DELETE CASCADE,
    code_used_at       TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The plugin is a Git provider: repositories, pull requests, task links and
-- the per-project connection now live in Paca core (git_* tables). The
-- project-bound tables of the previous release are dropped; PAT connections
-- cannot back a provider identity, so no data is migrated.
DROP TABLE IF EXISTS github_task_pr_links;
DROP TABLE IF EXISTS github_task_branches;
DROP TABLE IF EXISTS github_pull_requests;
DROP TABLE IF EXISTS github_repositories;
DROP TABLE IF EXISTS github_integrations;
