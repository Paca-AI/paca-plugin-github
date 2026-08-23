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

Without PUBLIC_URL, linking repositories will fail because webhook creation requires a reachable callback URL.

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
