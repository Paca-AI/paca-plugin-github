package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

const (
	githubPluginID = "com.paca.github"
	ghBaseURL      = "https://api.github.com"
	ghAPIVersion   = "2022-11-28"

	// githubAppNameMaxLen is GitHub's limit on an App's name.
	githubAppNameMaxLen = 34
)

// fetch is the outbound HTTP call; a variable so tests can stub GitHub.
var fetch = plugin.Fetch

var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// manifestPermissions is requested up front as a superset of what the provider
// uses: widening an App's permissions later forces an org owner to re-approve it.
var manifestPermissions = map[string]string{
	"metadata":      "read",
	"contents":      "write",
	"pull_requests": "write",
	"issues":        "write",
	"checks":        "write", // the SPI reports agent progress as check runs
	"statuses":      "read",
	"actions":       "read",
}

var manifestEvents = []string{
	"push",
	"pull_request",
	"pull_request_review",
	"pull_request_review_comment",
	"pull_request_review_thread",
	"issue_comment",
	"issues",
	"check_run",
	"check_suite",
}

// ghInstallation is a minimal GET /app/installations/{id} response.
type ghInstallation struct {
	ID                  int64      `json:"id"`
	RepositorySelection string     `json:"repository_selection"`
	SuspendedAt         *time.Time `json:"suspended_at"`
	Account             struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// ghManifestConversion is the response of POST /app-manifests/{code}/conversions.
// It is the only time GitHub ever returns the App's private key and webhook secret.
type ghManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// nowStr returns the current UTC time as an RFC3339Nano string.
func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "", host == "localhost", host == "::1", host == "0.0.0.0":
		return true
	case strings.HasSuffix(host, ".local"), strings.HasSuffix(host, ".internal"),
		strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".lan"):
		return true
	case strings.HasPrefix(host, "127."), strings.HasPrefix(host, "10."), strings.HasPrefix(host, "192.168."):
		return true
	case strings.HasPrefix(host, "172."):
		parts := strings.Split(host, ".")
		if len(parts) > 1 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n >= 16 && n <= 31 {
				return true
			}
		}
	}
	return false
}

func verifyHMAC(payload []byte, secret, signatureHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signatureHeader[len(prefix):]))
}
