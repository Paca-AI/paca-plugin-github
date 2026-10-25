package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"regexp"
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

// isLocalURL reports whether rawURL points at this machine or a private
// network, where GitHub cannot deliver a webhook.
func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "", host == "localhost":
		return true
	case strings.HasSuffix(host, ".local"), strings.HasSuffix(host, ".internal"),
		strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".lan"):
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		cgnat.Contains(ip)
}

// cgnat is the carrier-grade NAT range (RFC 6598), which is not routable either.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func verifyHMAC(payload []byte, secret, signatureHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signatureHeader[len(prefix):]))
}
