// Package main implements the com.paca.github backend WASM plugin: the GitHub
// provider of Paca's Git provider SPI (spi_*.go). Core drives it through the
// internal-only POST /_git/:op routes; the plugin has no routes of its own, no
// per-project data and no frontend. Connections are GitHub App installations
// only; personal access tokens are not supported.
package main

import (
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/gitprovider/serve"
)

// githubPlugin implements plugin.Plugin.
type githubPlugin struct {
	db    *plugin.DB
	log   *plugin.Logger
	cfg   *plugin.Config
	cache *plugin.Cache
}

// Init mounts the provider operations.
func (p *githubPlugin) Init(ctx *plugin.Context) error {
	p.db = ctx.DB()
	p.log = ctx.Log()
	p.cfg = ctx.Config()
	p.cache = ctx.Cache()
	serve.Register(ctx, newGHProvider(newDBStore(p)))
	return nil
}

// Shutdown is a no-op for this plugin.
func (p *githubPlugin) Shutdown() {}
