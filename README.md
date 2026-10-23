# com.paca.github

First-party Paca plugin that integrates GitHub repositories, pull requests, and branches with Paca projects and tasks.

---

## Architecture

The plugin follows the standard three-part plugin structure:

```text
github/
├── backend/   - Go WASM plugin (runs inside the API host)
├── frontend/  - React micro-frontend (Module Federation remote)
└── mcp/       - MCP tools bundle for AI/tooling integrations
```

### Backend (backend/)

- Written in Go, compiled to wasip1/wasm for production.
- Registered as com.paca.github in the plugin registry.
- Owns its database schema (plugin_data_com_paca_github) and runs its own migration on startup.
- Stores GitHub PATs and webhook secrets encrypted with AES-256-GCM.
- Creates and verifies GitHub webhooks for linked repositories.
- Handles task/project cleanup on task.deleted and project.deleted events.

### Frontend (frontend/)

- Vite + React + TanStack Query.
- Exposed as a Module Federation remote (com_paca_github).
- Mounted at:
  - project.settings.tab (GitHubSettingsTab)
  - task.detail.section (GitHubTaskSection)
- Communicates with backend via plugin API routes under /api/v1/plugins/com.paca.github/projects/:projectId/...

### MCP (mcp/)

- Built with @paca-ai/plugin-sdk-mcp.
- Published as an ESM bundle (mcp.js).
- Exposes tools for integration management, repository linking, PR linking, and branch operations.

---

## Required Configuration

The backend uses the following config keys:

- ENCRYPTION_KEY: 64 hex chars (32-byte key) used for AES-256-GCM encryption of tokens/secrets.
- PUBLIC_URL: public base URL of the Paca server, used to build the webhook callback URL.

Without PUBLIC_URL, PAT-connected projects cannot link repositories (webhook creation needs a reachable callback URL), and GitHub App creation is refused. If PUBLIC_URL is a private/loopback address, a GitHub App is still created but with its webhook disabled.

## GitHub App connection (self-hosted, Manifest Flow)

Besides a personal access token, a project can connect through a **GitHub App** created from inside Paca:

1. A user with the instance-wide `github:manage_apps` action opens Project Settings → GitHub → *Create GitHub App*, picks a name and where to create it (personal account or organization).
2. Paca sends the pre-filled manifest to GitHub; the user confirms once.
3. GitHub redirects the browser back to a **page of the Paca web app** (`/plugin-pages/com.paca.github/github-app-callback`, a plugin `app.page`; needs a Paca release with `app.page` support). That page lives under the authenticated layout, so an expired access token is refreshed (or the user signs in and is returned to it) before it calls the plugin's authenticated `POST /apps/callback/created`, which exchanges the one-time code for the app's credentials and stores them encrypted (`github_apps`). The manifest `state` is single-use, expires after an hour and is bound to the user who started the flow.
4. The user installs the app on the account/organization; GitHub returns to the same page, which calls `POST /apps/callback/installed`.
5. Project managers (`github:manage`) pick an installation that was made available to their project. Installation tokens (1 hour) are minted from the app's private key and cached encrypted; clone-info returns the token with its expiry.

An app delivers a single webhook for every installed repository (`POST /webhooks/apps/:appKey`, verified with that app's secret and fanned out to every project that linked the repo), so linking a repo through an app needs no `admin:repo_hook` permission and creates no per-repo webhook.

### Per-agent identities

In an App-connected project, a project manager can map a member (typically an AI agent) to a different installation under Settings → GitHub → *Agent identities*. Calls made by that account — branches, pull requests, comments, reviews, clone tokens — then use that installation's token, so GitHub attributes them to that app's bot. This is what allows a reviewer agent to formally approve a developer agent's PR. Accounts without a mapping use the project's default installation. Mappings are keyed by account, so they apply to API-key callers (agents) as they do to people. If a mapped installation is suspended or withdrawn from the project, calls by that account fail with `GITHUB_AGENT_IDENTITY_UNAVAILABLE` rather than silently acting as the default identity. Automation actions (merge/comment nodes) have no calling account and use the project default.

Not yet implemented: review-thread/merge tools, new PR/review event triggers, GitHub Enterprise Server (`host`), importing an existing app.

---

## API Endpoints

All routes are prefixed with /projects/:projectId by the host.
The caller must be an authenticated member of the project unless noted otherwise.

| Method | Path | Description |
|--------|------|-------------|
| GET | /github | Get integration status for the current project |
| POST | /github/token | Set or replace GitHub PAT |
| DELETE | /github/token | Delete GitHub PAT and integration |
| GET | /github/repositories | List repositories accessible by the token |
| GET | /github/linked-repositories | List repositories linked to the project |
| POST | /github/linked-repositories | Link a repository and create webhook |
| DELETE | /github/linked-repositories/:repoId | Unlink a repository |
| GET | /tasks/:taskId/github/pull-requests | List pull requests linked to a task |
| POST | /tasks/:taskId/github/pull-requests | Link a pull request to a task |
| DELETE | /tasks/:taskId/github/pull-requests/:prId | Unlink a pull request from a task |
| POST | /tasks/:taskId/github/branches | Create branch and link it to a task |
| GET | /tasks/:taskId/github/branches | List branches linked to a task |
| POST | /webhook (public) | Receive GitHub webhook events |

### Example requests

Set token:

```json
{ "token": "ghp_xxx" }
```

Link repository:

```json
{ "owner": "Paca-AI", "repo_name": "paca" }
```

Link PR to task:

```json
{ "repo_id": "550e8400-e29b-41d4-a716-446655440000", "pr_number": 42 }
```

Create branch for task:

```json
{
  "repo_id": "550e8400-e29b-41d4-a716-446655440000",
  "branch_name": "feat/PROJ-42-github-integration",
  "source_branch": "main"
}
```

---

## Webhook Behavior

On linked repositories, GitHub webhooks are configured for:

- push
- pull_request
- check_run

Current behavior includes:

- Verifying X-Hub-Signature-256 with the stored webhook secret.
- Caching/updating pull request state in plugin tables.
- Auto-linking PRs when opened/reopened from a branch already linked to a task.
- Auto-linking created branches when branch names include task references like PROJ-42.
- Emitting plugin events:
  - github.pr_linked
  - github.pr_updated
  - github.branch_linked

---

## Database Schema

Tables live in plugin_data_com_paca_github and are created by backend/migrations/0001_create_github_tables.sql.

```text
github_integrations
  id UUID PK
  project_id UUID UNIQUE -> projects(id) ON DELETE CASCADE
  access_token_enc TEXT
  created_at TIMESTAMPTZ
  updated_at TIMESTAMPTZ

github_repositories
  id UUID PK
  project_id UUID -> projects(id) ON DELETE CASCADE
  integration_id UUID -> github_integrations(id) ON DELETE CASCADE
  owner TEXT
  repo_name TEXT
  full_name TEXT
  webhook_id BIGINT
  webhook_secret_enc TEXT
  default_branch TEXT
  created_at TIMESTAMPTZ
  updated_at TIMESTAMPTZ

github_pull_requests
  id UUID PK
  project_id UUID -> projects(id) ON DELETE CASCADE
  repo_id UUID -> github_repositories(id) ON DELETE CASCADE
  pr_number INT
  github_pr_id BIGINT
  title TEXT
  state TEXT
  html_url TEXT
  head_branch TEXT
  base_branch TEXT
  author TEXT
  merged_at TIMESTAMPTZ
  created_at TIMESTAMPTZ
  updated_at TIMESTAMPTZ

github_task_pr_links
  id UUID PK
  task_id UUID -> tasks(id) ON DELETE CASCADE
  pull_request_id UUID -> github_pull_requests(id) ON DELETE CASCADE
  created_at TIMESTAMPTZ

github_task_branches
  id UUID PK
  task_id UUID -> tasks(id) ON DELETE CASCADE
  repo_id UUID -> github_repositories(id) ON DELETE CASCADE
  branch_name TEXT
  created_at TIMESTAMPTZ
```

---

## Development

### Backend

```bash
cd backend

# Run tests
go test -v ./...

# Build WASM binary (requires TinyGo — see https://tinygo.org/getting-started/install/)
tinygo build -target=wasip1 -buildmode=c-shared -o github.wasm .
```

### Frontend

```bash
cd frontend

# Install dependencies
bun install

# Development build (watch)
bun run dev

# Production build
bun run build
```

### MCP

```bash
cd mcp

# Install dependencies
bun install

# Typecheck
bun run typecheck

# Production build (outputs mcp.js)
bun run build
```

---

## Release (CD)

The release workflow is at .github/workflows/release.yml.

It runs on:

- tag push matching v*
- manual workflow_dispatch

The workflow builds backend/frontend/mcp assets, packages migrations and plugin.json, generates SHA256 checksums, and uploads all artifacts to a GitHub Release.
