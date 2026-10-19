package main

import "testing"

// The gate is the reason a repository hook subscribed to check_run — which
// GitHub delivers once per CI job per state — costs this plugin nothing.
func TestHandlesWebhookEvent(t *testing.T) {
	cases := map[string]bool{
		"pull_request": true,
		"push":         true,
		"check_run":    false,
		"check_suite":  false,
		"workflow_run": false,
		"issues":       false,
		"":             false,
	}
	for event, want := range cases {
		if got := handlesWebhookEvent(event); got != want {
			t.Errorf("handlesWebhookEvent(%q) = %v, want %v", event, got, want)
		}
	}
}

// A check_run delivery — one per CI job per state — must cost this plugin
// nothing: no repository lookup, no HMAC work, no dispatch. Asserted on the
// log lines, since receiveWebhook answers 204 either way.
func TestReceiveWebhook_IgnoresUnhandledEventType(t *testing.T) {
	tc := setupPlugin(t)
	seedEncryptedRepo(t, tc, "repo-1", testProjectID, "octocat/hello-world", "my-projects-secret")

	payload := []byte(`{"repository":{"full_name":"octocat/hello-world"},"action":"completed"}`)
	req := signedWebhookRequest(testProjectID, payload, "my-projects-secret")
	req.Headers["X-Github-Event"] = "check_run"

	res := tc.Call("POST", "/webhook", req)
	if res.StatusCode != 204 {
		t.Fatalf("expected 204, got %d: %s", res.StatusCode, res.BodyString())
	}
	if !tc.Log.HasMessage("ignoring event this plugin does not handle") {
		t.Fatalf("expected the unhandled event to be dropped before dispatch; log entries: %+v", tc.Log.Entries())
	}
	if tc.Log.HasMessage("webhook received") {
		t.Fatalf("unhandled event reached handleWebhookEvent (repository lookup + HMAC); log entries: %+v", tc.Log.Entries())
	}
}
