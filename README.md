# com.paca.github

First-party Paca plugin that integrates GitHub repositories, pull requests, and branches with projects and tasks.

This plugin lets a project connect a GitHub token, link repositories, attach pull requests to tasks, create task branches, and receive GitHub webhooks for automatic sync.

---

## Architecture

The plugin follows the standard three-part structure:

```
paca-plugin-github/
├── backend/   — Go WASM plugin (API host runtime)
├── frontend/  — React micro-frontend (Module Federation remote)
└── mcp/       — MCP tools (plugin-sdk-mcp)
```

### Backend (`backend/`)

- Written in Go and compiled to `wasip1/wasm` for production.
- Registered as `com.paca.github`.
- Stores integration/repository/task-link data in plugin-owned tables (see migration `backend/migrations/0001_create_github_tables.sql`).
- Encrypts GitHub token and webhook secret using plugin config (`ENCRYPTION_KEY`).
- Creates/deletes GitHub webhooks for linked repositories.
- Subscribes to events:
  - `task.deleted` to clean up task branch and PR links.
  - `project.deleted` to clean up integration data.

### Frontend (`frontend/`)

- Vite + React + TanStack Query.
- Exposes task/project GitHub UI through extension points declared in `plugin.json`.
- Communicates with backend through plugin API routes under:
  - `/api/v1/plugins/com.paca.github/projects/:projectId/...`

### MCP (`mcp/`)

- Built with `@paca-ai/plugin-sdk-mcp`.
- Exposes GitHub integration tools for agents and automation workflows.

---

## Backend API Endpoints

All routes are project-scoped by the host unless marked as public.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/github` | Get current GitHub integration status |
| `POST` | `/github/token` | Set or replace GitHub personal access token |
| `DELETE` | `/github/token` | Remove token and integration |
| `GET` | `/github/repositories` | List GitHub repositories accessible by token |
| `GET` | `/github/linked-repositories` | List repositories linked to this project |
| `POST` | `/github/linked-repositories` | Link a repository and create webhook |
| `DELETE` | `/github/linked-repositories/:repoId` | Unlink repository and delete webhook |
| `GET` | `/tasks/:taskId/github/pull-requests` | List pull requests linked to a task |
| `POST` | `/tasks/:taskId/github/pull-requests` | Link a pull request to a task |
| `DELETE` | `/tasks/:taskId/github/pull-requests/:prId` | Unlink a pull request from a task |
| `POST` | `/tasks/:taskId/github/branches` | Create and link a branch for a task |
| `GET` | `/tasks/:taskId/github/branches` | List branches linked to a task |
| `POST` | `/webhook` | Public GitHub webhook endpoint |

Webhook behavior:
- `pull_request` updates PR cache and can auto-link PRs when branch-task links exist.
- `push` can auto-link branches to tasks when branch names include task references like `PROJ-42`.

---

## Frontend Extension Points

| Extension Point | Component | Label | Order |
|-----------------|-----------|-------|-------|
| `project.settings.tab` | `GitHubSettingsTab` | `GitHub` | `100` |
| `task.detail.section` | `GitHubTaskSection` | - | `5` |

Remote entry URL:

`/plugins/com.paca.github/assets/remoteEntry.js`

---

## MCP Tools

The MCP package provides the following tools:

- `github_get_integration`
- `github_set_token`
- `github_delete_token`
- `github_list_repositories`
- `github_list_linked_repos`
- `github_link_repository`
- `github_unlink_repository`
- `github_list_task_prs`
- `github_link_pr_to_task`
- `github_unlink_pr_from_task`
- `github_create_branch`
- `github_list_task_branches`

---

## Development

### Backend

```bash
cd backend

# Run tests
go test ./...

# Build WASM (requires Go 1.24+)
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o github.wasm .
```

### Frontend

```bash
cd frontend

# Install dependencies
bun install

# Typecheck
bun run typecheck

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

# Production build
bun run build
```

---

## CI/CD

This repository includes GitHub Actions workflows under `.github/workflows`:

- `backend-pr-ci.yml`: runs backend lint, build, and tests on backend-related PR changes.
- `frontend-pr-ci.yml`: runs frontend typecheck and build on frontend-related PR changes.
- `mcp-pr-ci.yml`: runs MCP typecheck and build on MCP-related PR changes.
- `release.yml`: builds backend/frontend/mcp artifacts on tag pushes (`v*`) and publishes release assets.

Release artifacts include:

- `github-backend-wasm.tar.gz`
- `github-frontend-dist.tar.gz`
- `github-mcp-dist.tar.gz`
- `github-migrations.tar.gz`
- `github-plugin-manifest.tar.gz`
- `checksums.txt`

---

## Required Backend Config

The backend uses the following allowed config keys defined in `plugin.json`:

- `ENCRYPTION_KEY` for token/secret encryption.
- `PUBLIC_URL` for generating public webhook callback URLs.