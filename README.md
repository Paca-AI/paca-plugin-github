# com.paca.github

First-party Paca plugin that is the GitHub provider for Paca's platform-neutral Git integration (`gitprovider.v1`, see `docs/plugins/git-provider-spi.md`).

Core owns repositories, project links, identities, change requests and audit. This plugin only adapts GitHub to the provider SPI:

- Connection setup, either through the GitHub App manifest flow (an App per workspace, project or agent) or by pasting a personal access token (the webhook secret is generated). A workspace or project may hold several connections of either kind.
- Apps: per-owner installation grants, requested when a repository is linked, and short-lived installation tokens for read/write repository credentials (never stored in remotes).
- Tokens: static credentials handed to git per command by core, a webhook registered on each linked repository with a generated secret (`webhook_management`; the token needs the webhook permission), and commit statuses instead of check runs. A token acts as its owner on GitHub, so a dedicated machine user is recommended.
- Repository listing, webhook verification and event normalisation.

## Structure

```text
github/
└── backend/   - Go WASM plugin (runs inside the API host)
```

The plugin has no HTTP routes of its own: core calls the internal-only `POST /_git/:op` operations. It has no frontend and no MCP tools; the Git UI and agent tools live in core and `apps/mcp`.

### Backend

- `spi_*.go` implement the provider operations; the conformance suite (`spi_conformance_test.go`) runs them against a fake GitHub.
- Owns the schema `plugin_data_com_paca_github` (`github_apps`, `github_app_installations`, `github_tokens`, `github_manifest_sessions`); App private keys, tokens and webhook secrets are stored encrypted with AES-256-GCM using the `ENCRYPTION_KEY` config value.
- `spi_token.go` holds everything specific to token connections; the kind a setup creates is chosen by `Params["kind"]` (`app` by default, `token`). The provider reports per-kind behaviour in `capabilities.kinds`.

### Upgrading from 0.4.x

Migration `0002_git_provider.sql` moves the previous release's data into core and drops its tables (`github_integrations`, `github_repositories`, `github_pull_requests`, `github_task_branches`, `github_task_pr_links`):

- Each project's access token becomes a **project-scoped token connection** (the ciphertext is copied as is, so the same `ENCRYPTION_KEY` must be in place), with its linked repositories, branch links and pull request links.
- The token's owner login and a webhook secret are not known to the migration; they are filled in the first time the token is used (GitHub is asked who owns it, a secret is generated). Until then the connection shows no `@handle`.
- The webhooks the old release registered on GitHub pointed at the old plugin and are left there. Open the project's Git settings and press *Try again* on each repository to register a new one (the token needs the webhook permission), and delete the old hooks on GitHub.

### Build

```bash
cd backend
go vet ./... && go test -race ./...
tinygo build -target=wasip1 -buildmode=c-shared -o backend.wasm .
```
