package main

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestSignAppJWT_VerifiesAndCarriesClaims(t *testing.T) {
	key := newTestKey(t)
	now := time.Unix(1_700_000_000, 0)
	jwt, err := signAppJWT(12345, pkcs1PEM(key), now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 segments, got %d", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "12345" || claims.Iat != now.Unix()-60 || claims.Exp != now.Unix()+540 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestParseRSAPrivateKey_AcceptsPKCS8AndRejectsGarbage(t *testing.T) {
	key := newTestKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := parseRSAPrivateKey(p8); err != nil {
		t.Fatalf("pkcs8: %v", err)
	}
	if _, err := parseRSAPrivateKey("not pem"); err == nil {
		t.Fatal("expected error for non-PEM input")
	}
}

func TestIsLocalURL(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8080":    true,
		"http://127.0.0.1":         true,
		"http://192.168.1.10":      true,
		"http://10.0.0.5":          true,
		"http://172.20.0.2":        true,
		"http://172.32.0.2":        false,
		"https://paca.example.com": false,
		"https://paca.internal":    true,
		"https://1.2.3.4":          false,
	}
	for u, want := range cases {
		if got := isLocalURL(u); got != want {
			t.Errorf("isLocalURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	base := "https://paca.example.com"
	if !sameOrigin(base, "https://paca.example.com/projects/1/settings") {
		t.Error("same host should match")
	}
	for _, bad := range []string{"https://evil.example.com/", "http://paca.example.com/", "/relative", "javascript:alert(1)", ""} {
		if sameOrigin(base, bad) {
			t.Errorf("sameOrigin accepted %q", bad)
		}
	}
}

func TestBuildManifest(t *testing.T) {
	m := buildManifest("Paca", "https://paca.example.com", "https://paca.example.com/api/v1/plugins/com.paca.github", "abc", true, false)
	hook := m["hook_attributes"].(map[string]any)
	if hook["url"] != "https://paca.example.com/api/v1/plugins/com.paca.github/webhooks/apps/abc" || hook["active"] != true {
		t.Fatalf("hook_attributes: %v", hook)
	}
	// Both legs return to a page of the Paca web app, not to a plugin API route.
	const page = "https://paca.example.com/plugin-pages/com.paca.github/github-app-callback"
	if m["redirect_url"] != page || m["setup_url"] != page {
		t.Fatalf("redirect_url=%v setup_url=%v", m["redirect_url"], m["setup_url"])
	}
	if m["public"] != false {
		t.Fatal("app must be private by default")
	}
}

// ── handlers ──────────────────────────────────────────────────────────────────

func setupAppsPlugin(t *testing.T) *plugintest.Context {
	t.Helper()
	tc := setupPlugin(t)
	tc.Config.Set("ENCRYPTION_KEY", testEncryptionKey)
	tc.Config.Set("PUBLIC_URL", "https://paca.example.com")
	tc.DB.SeedRows("github_manifest_sessions",
		[]string{"state", "app_key", "created_by", "app_name", "owner_org", "allow_all_projects",
			"return_url", "app_id", "code_used_at", "completed_at", "expires_at", "created_at"}, nil)
	tc.DB.SeedRows("github_apps",
		[]string{"id", "app_key", "github_app_id", "slug", "name", "private_key_enc", "webhook_secret_enc"}, nil)
	return tc
}

func adminReq() plugintest.Request {
	return plugintest.Request{Caller: plugin.CallerIdentity{UserID: "admin-1"}, PathParams: map[string]string{}}
}

func TestCreateManifest_StoresSessionAndReturnsGitHubURL(t *testing.T) {
	tc := setupAppsPlugin(t)
	res := tc.Call("POST", "/apps/manifest", adminReq().WithJSONBody(map[string]any{
		"name": "Paca Acme", "owner_org": "acme-inc", "return_url": "https://paca.example.com/projects/1",
	}))
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, res.BodyString())
	}
	var out struct {
		Data manifestResponse `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Data.PostURL, "https://github.com/organizations/acme-inc/settings/apps/new?state=") {
		t.Fatalf("post_url: %s", out.Data.PostURL)
	}
	if !out.Data.WebhookActive {
		t.Error("public https URL should enable the webhook")
	}
	if rows := tc.DB.AllRows("github_manifest_sessions"); len(rows) != 1 {
		t.Fatalf("want 1 session row, got %d", len(rows))
	}
}

func TestCreateManifest_Validation(t *testing.T) {
	tc := setupAppsPlugin(t)
	bad := []map[string]any{
		{"name": ""},
		{"name": strings.Repeat("x", githubAppNameMaxLen+1)},
		{"name": "ok", "owner_org": "bad/org"},
		{"name": "ok", "return_url": "https://evil.example.com/"},
	}
	for _, body := range bad {
		res := tc.Call("POST", "/apps/manifest", adminReq().WithJSONBody(body))
		if res.StatusCode != 400 {
			t.Errorf("body %v: want 400, got %d", body, res.StatusCode)
		}
	}
	if rows := tc.DB.AllRows("github_manifest_sessions"); len(rows) != 0 {
		t.Fatalf("rejected requests must not create sessions, got %d", len(rows))
	}
}

const sessionCols = "state,app_key,created_by,app_name,owner_org,allow_all_projects,return_url,app_id,code_used_at,completed_at,expires_at,created_at"

func seedSession(tc *plugintest.Context, state, createdBy, appID string, expires time.Time) {
	var app any
	if appID != "" {
		app = appID
	}
	tc.DB.SeedRows("github_manifest_sessions", strings.Split(sessionCols, ","),
		[][]any{{state, "k", createdBy, "Paca", "", false, "https://paca.example.com/projects/1",
			app, nil, nil, expires.UTC().Format(time.RFC3339Nano), "now"}})
}

func decodeError(t *testing.T, res *plugin.Response) string {
	t.Helper()
	var out struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("body %q: %v", res.BodyString(), err)
	}
	return out.ErrorCode
}

func TestCallbackCreated_RejectsUnknownState(t *testing.T) {
	tc := setupAppsPlugin(t)
	res := tc.Call("POST", "/apps/callback/created", adminReq().WithJSONBody(map[string]string{"state": "nope", "code": "c"}))
	if res.StatusCode != 400 || decodeError(t, res) != setupInvalidState {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.BodyString())
	}
}

func TestCallbackCreated_RequiresCodeAndState(t *testing.T) {
	tc := setupAppsPlugin(t)
	res := tc.Call("POST", "/apps/callback/created", adminReq().WithJSONBody(map[string]string{"state": "s"}))
	if res.StatusCode != 400 || decodeError(t, res) != setupMissingParam {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.BodyString())
	}
}

// A state that leaks must be useless to anyone but the user who started the flow.
func TestCallbacks_StateIsBoundToItsUser(t *testing.T) {
	tc := setupAppsPlugin(t)
	seedSession(tc, "live", "admin-1", "app-row-1", time.Now().Add(time.Hour))
	other := plugintest.Request{Caller: plugin.CallerIdentity{UserID: "someone-else"}}

	created := tc.Call("POST", "/apps/callback/created", other.WithJSONBody(map[string]string{"state": "live", "code": "c"}))
	installed := tc.Call("POST", "/apps/callback/installed", other.WithJSONBody(map[string]string{"state": "live", "installation_id": "1"}))
	for name, res := range map[string]*plugin.Response{"created": created, "installed": installed} {
		if res.StatusCode != 400 || decodeError(t, res) != setupInvalidState {
			t.Errorf("%s: status=%d body=%s", name, res.StatusCode, res.BodyString())
		}
	}
}

func TestCallbacks_NoCallerIsRejected(t *testing.T) {
	tc := setupAppsPlugin(t)
	seedSession(tc, "live", "admin-1", "app-row-1", time.Now().Add(time.Hour))
	res := tc.Call("POST", "/apps/callback/installed", plugintest.Request{}.WithJSONBody(map[string]string{"state": "live", "installation_id": "1"}))
	if res.StatusCode != 400 || decodeError(t, res) != setupInvalidState {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.BodyString())
	}
}

func TestCallbackInstalled_ExpiredStateIsRejected(t *testing.T) {
	tc := setupAppsPlugin(t)
	seedSession(tc, "old", "admin-1", "app-row-1", time.Now().Add(-time.Minute))
	res := tc.Call("POST", "/apps/callback/installed", adminReq().WithJSONBody(map[string]string{"state": "old", "installation_id": "1"}))
	if res.StatusCode != 400 || decodeError(t, res) != setupInvalidState {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.BodyString())
	}
}

func TestCallbackInstalled_RejectsBadInstallationID(t *testing.T) {
	tc := setupAppsPlugin(t)
	seedSession(tc, "live", "admin-1", "app-row-1", time.Now().Add(time.Hour))
	for _, id := range []string{"", "abc", "0", "-3"} {
		res := tc.Call("POST", "/apps/callback/installed", adminReq().WithJSONBody(map[string]string{"state": "live", "installation_id": id}))
		if res.StatusCode != 400 || decodeError(t, res) != setupMissingParam {
			t.Errorf("id %q: status=%d body=%s", id, res.StatusCode, res.BodyString())
		}
	}
}

// Without an app yet (the created leg never ran) the installed leg has nothing to attach to.
func TestCallbackInstalled_RequiresCreatedLegFirst(t *testing.T) {
	tc := setupAppsPlugin(t)
	seedSession(tc, "live", "admin-1", "", time.Now().Add(time.Hour))
	res := tc.Call("POST", "/apps/callback/installed", adminReq().WithJSONBody(map[string]string{"state": "live", "installation_id": "7"}))
	if res.StatusCode != 400 || decodeError(t, res) != setupInvalidState {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.BodyString())
	}
}

func appWebhookSetup(t *testing.T) (*plugintest.Context, string) {
	tc := setupAppsPlugin(t)
	p := &githubPlugin{cfg: tc.PluginContext().Config()}
	enc, err := p.encrypt("hook-secret")
	if err != nil {
		t.Fatal(err)
	}
	tc.DB.SeedRows("github_apps",
		[]string{"id", "app_key", "github_app_id", "slug", "name", "private_key_enc", "webhook_secret_enc"},
		[][]any{{"app-row-1", "key1", float64(1), "paca", "Paca", "x", enc}})
	return tc, "key1"
}

func signBody(body []byte, secret string) string {
	return "sha256=" + hmacHex(body, secret)
}

func TestAppWebhook_RejectsBadSignature(t *testing.T) {
	tc, key := appWebhookSetup(t)
	req := adminReq()
	req.PathParams = map[string]string{"appKey": key}
	req.Body = []byte(`{"zen":"x"}`)
	req.Headers = map[string]string{"x-github-event": "ping", "x-hub-signature-256": "sha256=deadbeef"}
	if res := tc.Call("POST", "/webhooks/apps/:appKey", req); res.StatusCode != 401 {
		t.Fatalf("want 401, got %d", res.StatusCode)
	}
}

func TestAppWebhook_AcceptsValidPing(t *testing.T) {
	tc, key := appWebhookSetup(t)
	body := []byte(`{"zen":"x"}`)
	req := adminReq()
	req.PathParams = map[string]string{"appKey": key}
	req.Body = body
	req.Headers = map[string]string{"x-github-event": "ping", "x-hub-signature-256": signBody(body, "hook-secret")}
	if res := tc.Call("POST", "/webhooks/apps/:appKey", req); res.StatusCode != 204 {
		t.Fatalf("want 204, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestAppWebhook_UnknownAppIs404(t *testing.T) {
	tc, _ := appWebhookSetup(t)
	req := adminReq()
	req.PathParams = map[string]string{"appKey": "other"}
	req.Body = []byte(`{}`)
	if res := tc.Call("POST", "/webhooks/apps/:appKey", req); res.StatusCode != 404 {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
}

// ── installation tokens ───────────────────────────────────────────────────────

func TestInstallationToken_MintsCachesAndReuses(t *testing.T) {
	tc := setupAppsPlugin(t)
	p := &githubPlugin{db: tc.PluginContext().DB(), cfg: tc.PluginContext().Config(), cache: tc.PluginContext().Cache(), log: tc.PluginContext().Log()}
	key := newTestKey(t)
	pemEnc, err := p.encrypt(pkcs1PEM(key))
	if err != nil {
		t.Fatal(err)
	}
	tc.DB.SeedRows("github_apps",
		[]string{"id", "app_key", "github_app_id", "slug", "name", "private_key_enc", "webhook_secret_enc"},
		[][]any{{"app-row-1", "k", float64(42), "paca", "Paca", pemEnc, ""}})
	tc.DB.SeedRows("github_app_installations",
		[]string{"id", "app_id", "installation_id", "suspended_at"},
		[][]any{{"inst-row-1", "app-row-1", float64(777), nil}})

	calls := 0
	orig := fetch
	defer func() { fetch = orig }()
	fetch = func(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
		calls++
		if method != "POST" || rawURL != "https://api.github.com/app/installations/777/access_tokens" {
			t.Fatalf("unexpected call %s %s", method, rawURL)
		}
		if !strings.HasPrefix(headers["Authorization"], "Bearer ") || strings.Count(headers["Authorization"], ".") != 2 {
			t.Fatalf("expected a JWT bearer, got %q", headers["Authorization"])
		}
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		return &plugin.FetchResponse{Status: 201, Body: `{"token":"ghs_abc","expires_at":"` + exp + `"}`}, nil
	}

	for i := 0; i < 3; i++ {
		tok, exp, err := p.installationToken("inst-row-1")
		if err != nil {
			t.Fatal(err)
		}
		if tok != "ghs_abc" || time.Until(exp) < 50*time.Minute {
			t.Fatalf("token=%q exp=%v", tok, exp)
		}
	}
	if calls != 1 {
		t.Fatalf("token should be minted once and then served from cache, minted %d times", calls)
	}
	if v, ok := tc.Cache.Get(installationTokenCacheKey("inst-row-1")); !ok || strings.Contains(v, "ghs_abc") {
		t.Fatal("cached token must be present and encrypted")
	}
}

func TestInstallationToken_SuspendedIsRefused(t *testing.T) {
	tc := setupAppsPlugin(t)
	p := &githubPlugin{db: tc.PluginContext().DB(), cfg: tc.PluginContext().Config(), cache: tc.PluginContext().Cache(), log: tc.PluginContext().Log()}
	tc.DB.SeedRows("github_app_installations",
		[]string{"id", "app_id", "installation_id", "suspended_at"},
		[][]any{{"inst-row-1", "app-row-1", float64(777), "2026-01-01T00:00:00Z"}})
	if _, _, err := p.installationToken("inst-row-1"); err == nil {
		t.Fatal("expected suspended installation to be refused")
	}
}

func hmacHex(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
