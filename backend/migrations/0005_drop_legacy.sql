-- 0005_drop_legacy.sql
-- The plugin is now only a Git provider (spi_*.go). Everything the old
-- project-bound integration kept (PAT and app connections, linked
-- repositories, cached pull requests, task links, per-agent identities and
-- per-project installation grants) is superseded by core's git_* tables and is
-- dropped; PAT connections cannot be carried over, so no data is migrated.
-- Kept: github_apps, github_app_installations, github_manifest_sessions.
-- Run with search_path = plugin_data_com_paca_github, public.

DROP TABLE IF EXISTS github_task_pr_links;
DROP TABLE IF EXISTS github_task_branches;
DROP TABLE IF EXISTS github_pull_requests;
DROP TABLE IF EXISTS github_repositories;
DROP TABLE IF EXISTS github_agent_identities;
DROP TABLE IF EXISTS github_installation_projects;
DROP TABLE IF EXISTS github_integrations;

ALTER TABLE github_app_installations DROP COLUMN IF EXISTS allow_all_projects;
ALTER TABLE github_manifest_sessions DROP COLUMN IF EXISTS allow_all_projects;
ALTER TABLE github_manifest_sessions DROP COLUMN IF EXISTS return_url;
