-- 0004_git_provider_sessions.sql
-- The Git provider SPI drives the GitHub App manifest flow for core, which
-- also needs to know whom the identity is for. Run with
-- search_path = plugin_data_com_paca_github, public.

ALTER TABLE github_manifest_sessions ADD COLUMN IF NOT EXISTS scope    TEXT NOT NULL DEFAULT 'shared';
ALTER TABLE github_manifest_sessions ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';
