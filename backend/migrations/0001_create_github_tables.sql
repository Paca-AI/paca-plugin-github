-- 0001_create_github_tables.sql
-- Creates the GitHub integration tables in the plugin schema.
-- Run with search_path = plugin_data_com_paca_github, public.
--
-- If the core migration (000006_migrate_github_to_plugin.sql) has already
-- moved tables from the public schema, the IF NOT EXISTS guards make this
-- migration a safe no-op.

CREATE TABLE IF NOT EXISTS github_integrations (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID        NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    access_token_enc TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_github_integrations_project_id
    ON github_integrations (project_id);

CREATE TABLE IF NOT EXISTS github_repositories (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    integration_id    UUID        NOT NULL REFERENCES github_integrations(id) ON DELETE CASCADE,
    owner             TEXT        NOT NULL,
    repo_name         TEXT        NOT NULL,
    full_name         TEXT        NOT NULL,
    webhook_id        BIGINT      NOT NULL DEFAULT 0,
    webhook_secret_enc TEXT       NOT NULL DEFAULT '',
    default_branch    TEXT        NOT NULL DEFAULT 'main',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, full_name)
);

CREATE INDEX IF NOT EXISTS idx_github_repositories_project_id
    ON github_repositories (project_id);
CREATE INDEX IF NOT EXISTS idx_github_repositories_full_name
    ON github_repositories (full_name);

CREATE TABLE IF NOT EXISTS github_pull_requests (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id     UUID        NOT NULL REFERENCES github_repositories(id) ON DELETE CASCADE,
    pr_number   INT         NOT NULL,
    github_pr_id BIGINT     NOT NULL,
    title       TEXT        NOT NULL DEFAULT '',
    state       TEXT        NOT NULL DEFAULT 'open',
    html_url    TEXT        NOT NULL DEFAULT '',
    head_branch TEXT        NOT NULL DEFAULT '',
    base_branch TEXT        NOT NULL DEFAULT '',
    author      TEXT        NOT NULL DEFAULT '',
    merged_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repo_id, pr_number)
);

CREATE INDEX IF NOT EXISTS idx_github_pull_requests_repo_id
    ON github_pull_requests (repo_id);

CREATE TABLE IF NOT EXISTS github_task_pr_links (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id        UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    pull_request_id UUID       NOT NULL REFERENCES github_pull_requests(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (task_id, pull_request_id)
);

CREATE INDEX IF NOT EXISTS idx_github_task_pr_links_task_id
    ON github_task_pr_links (task_id);

CREATE TABLE IF NOT EXISTS github_task_branches (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    repo_id     UUID        NOT NULL REFERENCES github_repositories(id) ON DELETE CASCADE,
    branch_name TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (task_id, repo_id, branch_name)
);

CREATE INDEX IF NOT EXISTS idx_github_task_branches_task_id
    ON github_task_branches (task_id);
