-- 0002_git_provider.sql
-- The GitHub provider's own state: GitHub Apps (created through the GitHub App
-- manifest flow, key material encrypted), the accounts they are installed on,
-- personal access token connections (token and webhook secret encrypted), and
-- the setup sessions that tie a setup's redirects or token form together. The
-- previous release's project-bound tables are migrated and dropped (see the end).
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
    -- Which kind of identity the attempt creates: an App (manifest flow) or a token.
    kind               TEXT        NOT NULL DEFAULT 'app',
    agent_id           TEXT        NOT NULL DEFAULT '',
    -- Set once the manifest code has been exchanged; the second leg
    -- (installation) then runs against this app.
    app_id             UUID        REFERENCES github_apps(id) ON DELETE CASCADE,
    code_used_at       TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Personal access token identities, next to the Apps: a user pastes a token and
-- a webhook secret; the token is checked against GitHub and stored encrypted.
CREATE TABLE IF NOT EXISTS github_tokens (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    login              TEXT        NOT NULL,
    name               TEXT        NOT NULL DEFAULT '',
    token_enc          TEXT        NOT NULL,
    webhook_secret_enc TEXT        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Migrating the previous release ──────────────────────────────────────────
-- The plugin is a Git provider: repositories, pull requests, task links and the
-- per-project connection now live in Paca core (git_* tables). The previous
-- release kept one personal access token per project (github_integrations) and
-- the repositories linked with it. Each becomes a project-scoped token
-- connection in core with its repositories, branch links and pull request links,
-- and then the old tables go.
--
-- What cannot be known here is completed on the token's first use (see
-- completeMigratedToken in spi_token.go): the owner's login is asked of GitHub
-- and a webhook secret is generated. The webhooks the old release registered on
-- GitHub pointed at the old plugin and used per-repository secrets; they are
-- left on GitHub, and "Try again" on a repository registers a new one.
-- Core's tables are created before any plugin migration runs; if they are not
-- there (a plugin-only database) the old data is only dropped.

-- The ciphertexts are copied as they are: the same ENCRYPTION_KEY protects both.
INSERT INTO github_tokens (id, login, name, token_enc, webhook_secret_enc)
SELECT i.id, '', '', i.access_token_enc, ''
FROM github_integrations i
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
    IF to_regclass('public.git_identities') IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO public.git_identities (provider_id, host, provider_identity_id, kind, scope, project_id, handle, display_name)
    SELECT 'com.paca.github', 'github.com', i.id::text, 'token', 'project', i.project_id, '', 'GitHub access token'
    FROM github_integrations i
    WHERE NOT EXISTS (
        SELECT 1 FROM public.git_identities g
        WHERE g.provider_id = 'com.paca.github' AND g.provider_identity_id = i.id::text
    );

    -- Webhook deliveries for a connection arrive at its own endpoint.
    INSERT INTO public.git_webhook_endpoints (key, provider_id, identity_id)
    SELECT replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', ''), g.provider_id, g.id
    FROM public.git_identities g
    WHERE g.provider_id = 'com.paca.github' AND g.kind = 'token' AND g.scope = 'project'
      AND NOT EXISTS (SELECT 1 FROM public.git_webhook_endpoints e WHERE e.identity_id = g.id);

    INSERT INTO public.git_repositories (project_id, provider_id, host, external_id, full_path, default_branch, clone_url, identity_id)
    SELECT r.project_id, 'com.paca.github', 'github.com', '', r.full_name, r.default_branch,
           'https://github.com/' || r.full_name || '.git', g.id
    FROM github_repositories r
    JOIN public.git_identities g ON g.provider_id = 'com.paca.github' AND g.provider_identity_id = r.integration_id::text
    ON CONFLICT DO NOTHING;

    INSERT INTO public.git_change_requests (repository_id, number, title, state, head_ref, base_ref, author, html_url, merged_at)
    SELECT cr.id, p.pr_number::text, p.title,
           CASE WHEN p.state IN ('open', 'closed', 'merged') THEN p.state ELSE 'open' END,
           p.head_branch, p.base_branch, p.author, p.html_url, p.merged_at
    FROM github_pull_requests p
    JOIN github_repositories r ON r.id = p.repo_id
    JOIN public.git_repositories cr ON cr.project_id = r.project_id AND cr.provider_id = 'com.paca.github'
                                    AND cr.host = 'github.com' AND lower(cr.full_path) = lower(r.full_name)
    ON CONFLICT DO NOTHING;

    INSERT INTO public.git_task_links (task_id, change_request_id)
    SELECT l.task_id, cr.id
    FROM github_task_pr_links l
    JOIN github_pull_requests p ON p.id = l.pull_request_id
    JOIN github_repositories r ON r.id = p.repo_id
    JOIN public.git_repositories gr ON gr.project_id = r.project_id AND gr.provider_id = 'com.paca.github'
                                    AND gr.host = 'github.com' AND lower(gr.full_path) = lower(r.full_name)
    JOIN public.git_change_requests cr ON cr.repository_id = gr.id AND cr.number = p.pr_number::text
    ON CONFLICT DO NOTHING;

    INSERT INTO public.git_task_branches (task_id, repository_id, branch_name)
    SELECT b.task_id, gr.id, b.branch_name
    FROM github_task_branches b
    JOIN github_repositories r ON r.id = b.repo_id
    JOIN public.git_repositories gr ON gr.project_id = r.project_id AND gr.provider_id = 'com.paca.github'
                                    AND gr.host = 'github.com' AND lower(gr.full_path) = lower(r.full_name)
    ON CONFLICT DO NOTHING;
END $$;

DROP TABLE IF EXISTS github_task_pr_links;
DROP TABLE IF EXISTS github_task_branches;
DROP TABLE IF EXISTS github_pull_requests;
DROP TABLE IF EXISTS github_repositories;
DROP TABLE IF EXISTS github_integrations;
