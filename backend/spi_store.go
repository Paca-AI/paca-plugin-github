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
	// deleteInstallation forgets an installation GitHub no longer has.
	deleteInstallation(appID string, installationID int64) error

	saveSetup(s setupSession) error
	loadSetup(state string) (setupSession, error)
	updateSetup(s setupSession) error

	cacheGet(key string) (token string, expires time.Time, ok bool)
	cacheSet(key, token string, expires time.Time)
	cacheDelete(key string)
}

var errNoSuchApp = errors.New("github app not found")
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
	AppKey    string
	Scope     string // shared | agent
	AgentID   string
	AppName   string
	OwnerOrg  string
	AppID     string // set once the manifest code has been exchanged
	CodeUsed  bool
	Completed bool
	ExpiresAt time.Time
}
