package main

import (
	"strings"
	"testing"
	"time"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

// seedTwoInstallations connects testProjectID to inst-default and makes
// inst-reviewer available to it, with a stubbed GitHub that hands out a token
// named after the installation it was minted for.
func seedTwoInstallations(t *testing.T, mapped bool, reviewerAllowed bool) (*plugintest.Context, *githubPlugin) {
	t.Helper()
	tc := setupAppsPlugin(t)
	p := &githubPlugin{db: tc.PluginContext().DB(), cfg: tc.PluginContext().Config(), cache: tc.PluginContext().Cache(), log: tc.PluginContext().Log()}
	pemEnc, err := p.encrypt(pkcs1PEM(newTestKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	tc.DB.SeedRows("github_apps",
		[]string{"id", "app_key", "github_app_id", "slug", "name", "private_key_enc", "webhook_secret_enc"},
		[][]any{{"app-1", "k", float64(1), "paca", "Paca", pemEnc, ""}})
	tc.DB.SeedRows("github_app_installations",
		[]string{"id", "app_id", "installation_id", "allow_all_projects", "suspended_at"},
		[][]any{
			{"inst-default", "app-1", float64(100), true, nil},
			{"inst-reviewer", "app-1", float64(200), reviewerAllowed, nil},
		})
	tc.DB.SeedRows("github_installation_projects", []string{"installation_id", "project_id"}, nil)
	tc.DB.SeedRows("github_integrations",
		[]string{"id", "project_id", "auth_type", "installation_id", "access_token_enc"},
		[][]any{{"integ-1", testProjectID, "app", "inst-default", nil}})
	var rows [][]any
	if mapped {
		rows = [][]any{{testProjectID, "agent-account", "inst-reviewer"}}
	}
	tc.DB.SeedRows("github_agent_identities", []string{"project_id", "account_id", "installation_id"}, rows)

	orig := fetch
	t.Cleanup(func() { fetch = orig })
	fetch = func(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
		name := "default"
		if strings.Contains(rawURL, "/200/") {
			name = "reviewer"
		}
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		return &plugin.FetchResponse{Status: 201, Body: `{"token":"ghs_` + name + `","expires_at":"` + exp + `"}`}, nil
	}
	return tc, p
}

func TestResolveCredentialFor_MappedAccountUsesItsOwnInstallation(t *testing.T) {
	_, p := seedTwoInstallations(t, true, true)
	tok, _, err := p.resolveCredentialFor(testProjectID, "agent-account")
	if err != nil || tok != "ghs_reviewer" {
		t.Fatalf("mapped account: token=%q err=%v", tok, err)
	}
}

func TestResolveCredentialFor_UnmappedAccountUsesProjectDefault(t *testing.T) {
	_, p := seedTwoInstallations(t, true, true)
	for _, account := range []string{"someone-else", ""} {
		tok, _, err := p.resolveCredentialFor(testProjectID, account)
		if err != nil || tok != "ghs_default" {
			t.Fatalf("account %q: token=%q err=%v", account, tok, err)
		}
	}
}

func TestResolveCredentialFor_WithdrawnMappingFailsClosed(t *testing.T) {
	_, p := seedTwoInstallations(t, true, false) // reviewer installation no longer open to the project
	_, _, err := p.resolveCredentialFor(testProjectID, "agent-account")
	ae, ok := err.(*appError)
	if !ok || ae.code != "GITHUB_AGENT_IDENTITY_UNAVAILABLE" {
		t.Fatalf("want GITHUB_AGENT_IDENTITY_UNAVAILABLE, got %v", err)
	}
}

func TestSetAgentIdentity_RequiresAppConnection(t *testing.T) {
	tc := setupAppsPlugin(t)
	tc.DB.SeedRows("github_integrations",
		[]string{"id", "project_id", "auth_type", "installation_id", "access_token_enc"},
		[][]any{{"integ-1", testProjectID, "pat", nil, "enc"}})
	req := callerReq().WithJSONBody(map[string]any{"member_id": "m1", "installation_id": "inst-1"})
	if res := tc.Call("POST", "/integration/agent-identities", req); res.StatusCode != 409 {
		t.Fatalf("want 409, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestSetAgentIdentity_UnknownMemberIs404(t *testing.T) {
	tc, _ := seedTwoInstallations(t, false, true)
	tc.DB.SeedRows("project_members", []string{"id", "project_id", "account_id", "deleted_at"},
		[][]any{{"member-other-project", "project-2", "acc-x", nil}})
	req := callerReq().WithJSONBody(map[string]any{"member_id": "member-other-project", "installation_id": "inst-reviewer"})
	if res := tc.Call("POST", "/integration/agent-identities", req); res.StatusCode != 404 {
		t.Fatalf("a member of another project must not be addressable, got %d: %s", res.StatusCode, res.BodyString())
	}
}

func TestSetAgentIdentity_RejectsInstallationNotOpenToProject(t *testing.T) {
	tc, _ := seedTwoInstallations(t, false, false)
	tc.DB.SeedRows("project_members", []string{"id", "project_id", "account_id", "deleted_at"},
		[][]any{{"member-1", testProjectID, "agent-account", nil}})
	req := callerReq().WithJSONBody(map[string]any{"member_id": "member-1", "installation_id": "inst-reviewer"})
	if res := tc.Call("POST", "/integration/agent-identities", req); res.StatusCode != 403 {
		t.Fatalf("want 403, got %d: %s", res.StatusCode, res.BodyString())
	}
}
