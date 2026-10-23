-- 0003_agent_identities.sql
-- Maps an account (typically an AI agent) to the GitHub App installation it
-- acts as inside one project. Accounts without a mapping use the project's
-- default installation.
-- Run with search_path = plugin_data_com_paca_github, public.

CREATE TABLE IF NOT EXISTS github_agent_identities (
    project_id      UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    account_id      UUID        NOT NULL,
    installation_id UUID        NOT NULL REFERENCES github_app_installations(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_github_agent_identities_installation_id
    ON github_agent_identities (installation_id);
