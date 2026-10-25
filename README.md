# com.paca.github

First-party Paca plugin that is the GitHub provider for Paca's platform-neutral Git integration (`gitprovider.v1`, see `docs/plugins/git-provider-spi.md`).

Core owns repositories, project links, identities, change requests and audit. This plugin only adapts GitHub to the provider SPI:

- Identity setup through the GitHub App manifest flow (one App per Paca instance or agent).
- Per-owner installation grants, requested when a repository is linked.
- Short-lived installation tokens for read/write repository credentials (never stored in remotes).
- Repository listing, webhook verification and event normalisation.

## Structure

```text
github/
└── backend/   - Go WASM plugin (runs inside the API host)
```

The plugin has no HTTP routes of its own: core calls the internal-only `POST /_git/:op` operations. It has no frontend and no MCP tools; the Git UI and agent tools live in core and `apps/mcp`.

### Backend

- `spi_*.go` implement the provider operations; the conformance suite (`spi_conformance_test.go`) runs them against a fake GitHub.
- Owns the schema `plugin_data_com_paca_github` (`github_apps`, `github_app_installations`, `github_manifest_sessions`); App private keys, client secrets and webhook secrets are stored encrypted with AES-256-GCM using the `ENCRYPTION_KEY` config value.
- Personal access tokens are not supported: every connection is a GitHub App installation.

### Build

```bash
cd backend
go vet ./... && go test -race ./...
tinygo build -target=wasip1 -buildmode=c-shared -o backend.wasm .
```
