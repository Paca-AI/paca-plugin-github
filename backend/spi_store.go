package main

import (
	"errors"
	"time"
)

// spiStore is what the Git provider needs persisted. The production
// implementation (dbStore) is backed by the plugin's tables; tests use an
// in-memory one. Secrets are decrypted by the store, never by callers.
type spiStore interface {
	loadApp(id string) (appRecord, error)
	installations(appID string) ([]installRecord, error)
	createApp(a appRecord) (id string, err error)
	// upsertInstallation records an installation. An account has at most one
	// installation of an App, and GitHub issues a new id when the App is
	// uninstalled and installed again, so any other recorded installation of the
	// same App on the same account is dead and is removed.
	upsertInstallation(appID string, i installRecord) error
	deleteApp(id string) error

	// Personal access token identities.
	loadToken(id string) (tokenRecord, error)
	createToken(t tokenRecord) (id string, err error)
	// updateToken rewrites a token's login, name and webhook secret (the fields a
	// migrated token fills in on first use).
	updateToken(t tokenRecord) error
	deleteToken(id string) error
	// deleteInstallation forgets an installation GitHub no longer has.
	deleteInstallation(appID string, installationID int64) error

	saveSetup(s setupSession) error
	loadSetup(state string) (setupSession, error)
	updateSetup(s setupSession) error
	// claimSetupCode marks the setup's manifest code as used and reports
	// whether this call was the one that did, so two concurrent completions
	// cannot both exchange the same code.
	claimSetupCode(state string) (bool, error)

	cacheGet(key string) (token string, expires time.Time, ok bool)
	cacheSet(key, token string, expires time.Time)
	cacheDelete(key string)
}

var errNoSuchApp = errors.New("github app not found")
var errNoSuchToken = errors.New("github token not found")
var errNoSuchSetup = errors.New("setup session not found")

// appRecord is a GitHub App the instance created through the manifest flow.
type appRecord struct {
	ID            string // row id; the SPI IdentityRef.ID
	GitHubAppID   int64
	Slug          string
	Name          string
	OwnerLogin    string
	HTMLURL       string
	WebhookSecret string
	PrivateKeyPEM string
	WebhookActive bool
	// IsUser marks a stand-in built from a token identity: its Slug is the
	// token owner's login, which is a user account rather than "<slug>[bot]".
	IsUser bool
}

// tokenRecord is a personal access token identity: the token acts as the user
// it belongs to, and the webhook secret is the one the user chose for the
// repository webhooks Paca registers with it.
type tokenRecord struct {
	ID            string // row id; the SPI IdentityRef.ID
	Login         string
	Name          string
	Token         string
	WebhookSecret string
}

// installRecord is an installation of an App on a GitHub account.
type installRecord struct {
	RowID          string
	InstallationID int64
	AccountLogin   string
	AccountType    string
	Selection      string
	Suspended      bool
}

// setupSession is one manifest-flow attempt, keyed by its single-use state.
type setupSession struct {
	State     string
	Scope     string // shared | agent
	Kind      string // "" or app: the manifest flow; token: a pasted access token
	AgentID   string
	AppName   string
	OwnerOrg  string
	AppID     string // set once the manifest code has been exchanged
	CodeUsed  bool
	Completed bool
	ExpiresAt time.Time
}
