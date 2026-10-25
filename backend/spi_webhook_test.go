package main

import (
	"strings"
	"testing"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

var appBot = map[string]any{"login": fakeSlug + "[bot]", "type": "Bot"}

func prPayload(extra map[string]any) map[string]any {
	pr := map[string]any{"number": 5, "title": "T", "body": "B", "user": userSender,
		"head": map[string]any{"ref": "feat", "sha": "s", "repo": map[string]string{"full_name": "acme/web"}},
		"base": map[string]any{"ref": "main"}, "html_url": "https://github.com/acme/web/pull/5"}
	for k, v := range extra {
		pr[k] = v
	}
	return pr
}

func TestWebhook_ReviewRequestAndAssignmentNamingThisApp(t *testing.T) {
	p, _, _ := seededProvider(t)
	for action, via := range map[string]gp.AddressVia{"review_requested": gp.ViaReviewRequest, "assigned": gp.ViaAssignment} {
		body := map[string]any{"action": action, "repository": repoPayload, "sender": userSender, "pull_request": prPayload(nil)}
		if action == "review_requested" {
			body["requested_reviewer"] = appBot
		} else {
			body["assignee"] = appBot
		}
		ev := parseOne(t, p, delivery("pull_request", "d-"+action, body))
		if len(ev.Addressed) != 1 || ev.Addressed[0].Via != via || ev.Addressed[0].IdentityID != fakeAppRowID {
			t.Errorf("%s: %+v", action, ev.Addressed)
		}
		if ev.Subject.URL != "https://github.com/acme/web/pull/5" {
			t.Errorf("%s: the page url is carried: %q", action, ev.Subject.URL)
		}
		// The same request for somebody else is not Paca's business.
		body["requested_reviewer"], body["assignee"] = map[string]any{"login": "alice", "type": "User"}, map[string]any{"login": "alice", "type": "User"}
		if evs, _ := p.VerifyAndParseWebhook(bg, delivery("pull_request", "d2-"+action, body)); len(evs) != 0 {
			t.Errorf("%s for another user must be dropped", action)
		}
	}
}

func TestWebhook_IssueAssignedToThisApp(t *testing.T) {
	p, _, _ := seededProvider(t)
	body := map[string]any{"action": "assigned", "repository": repoPayload, "sender": userSender, "assignee": appBot,
		"issue": map[string]any{"number": 3, "title": "Crash", "body": "details", "user": userSender}}
	ev := parseOne(t, p, delivery("issues", "d-ia", body))
	if ev.Kind != gp.EventAssignment || len(ev.Addressed) != 1 || !strings.Contains(ev.Text, "Crash") || !strings.Contains(ev.Text, "details") {
		t.Errorf("issue assigned: %+v", ev)
	}
}

func TestWebhook_ReviewsAndReviewComments(t *testing.T) {
	p, _, _ := seededProvider(t)
	review := delivery("pull_request_review", "d-rv", map[string]any{"action": "submitted", "repository": repoPayload, "sender": userSender,
		"pull_request": prPayload(nil), "review": map[string]any{"id": 11, "body": "@paca-bot please fix", "state": "changes_requested"}})
	ev := parseOne(t, p, review)
	if ev.Kind != gp.EventReview || len(ev.Addressed) != 1 || ev.Location.CommentID != "11" {
		t.Errorf("review: %+v", ev)
	}
	dismissed := delivery("pull_request_review", "d-rv2", map[string]any{"action": "dismissed", "repository": repoPayload, "sender": userSender,
		"pull_request": prPayload(nil), "review": map[string]any{"id": 11}})
	if evs, _ := p.VerifyAndParseWebhook(bg, dismissed); len(evs) != 0 {
		t.Error("only a submitted review is an event")
	}

	inline := delivery("pull_request_review_comment", "d-rc", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender,
		"pull_request": prPayload(nil), "comment": map[string]any{"id": 12, "body": "@paca-bot rename this", "path": "a.go", "line": 7}})
	ev = parseOne(t, p, inline)
	if ev.Location.Path != "a.go" || ev.Location.Line != 7 || len(ev.Addressed) != 1 {
		t.Errorf("inline comment: %+v", ev.Location)
	}
}

func TestWebhook_Pushes(t *testing.T) {
	p, _, _ := seededProvider(t)
	push := func(id, ref string, created, deleted bool) gp.WebhookRequest {
		return delivery("push", id, map[string]any{"repository": repoPayload, "sender": userSender, "ref": ref, "after": "abc", "created": created, "deleted": deleted})
	}
	if ev := parseOne(t, p, push("p1", "refs/heads/feat/x", true, false)); ev.Action != "created" || ev.Push.Ref != "feat/x" || !ev.Push.Created {
		t.Errorf("created: %+v", ev.Push)
	}
	if ev := parseOne(t, p, push("p2", "refs/heads/feat/x", false, true)); ev.Action != "deleted" || !ev.Push.Deleted {
		t.Errorf("deleted: %+v", ev.Push)
	}
	if ev := parseOne(t, p, push("p3", "refs/heads/main", false, false)); ev.Action != "pushed" || ev.Push.SHA != "abc" {
		t.Errorf("pushed: %+v", ev.Push)
	}
	for _, ref := range []string{"refs/tags/v1", "refs/notes/x", "refs/heads/"} {
		if evs, _ := p.VerifyAndParseWebhook(bg, push("p-"+ref, ref, false, false)); len(evs) != 0 {
			t.Errorf("%q is not branch activity", ref)
		}
	}
}

func TestWebhook_DroppedDeliveries(t *testing.T) {
	p, _, _ := seededProvider(t)
	cases := map[string]gp.WebhookRequest{
		"ping has no repository":   delivery("ping", "x1", map[string]any{"zen": "hi", "sender": userSender}),
		"no sender":                delivery("issues", "x2", map[string]any{"action": "opened", "repository": repoPayload, "issue": map[string]any{"number": 1}}),
		"unused event":             delivery("star", "x3", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender}),
		"comment without comment":  delivery("issue_comment", "x4", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender, "issue": map[string]any{"number": 1}}),
		"deleted comment":          delivery("issue_comment", "x5", map[string]any{"action": "deleted", "repository": repoPayload, "sender": userSender, "issue": map[string]any{"number": 1}, "comment": map[string]any{"id": 1}}),
		"check run still running":  delivery("check_run", "x6", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender, "check_run": map[string]any{"id": 1, "conclusion": nil}}),
		"check run without result": delivery("check_run", "x7", map[string]any{"action": "completed", "repository": repoPayload, "sender": userSender, "check_run": map[string]any{"id": 1, "conclusion": nil}}),
		"pull request unlabeled":   delivery("pull_request", "x8", map[string]any{"action": "unlabeled", "repository": repoPayload, "sender": userSender, "pull_request": prPayload(nil)}),
		"labeled without a label":  delivery("pull_request", "x9", map[string]any{"action": "labeled", "repository": repoPayload, "sender": userSender, "pull_request": prPayload(nil)}),
	}
	for name, req := range cases {
		evs, err := p.VerifyAndParseWebhook(bg, req)
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: events=%d err=%v", name, len(evs), err)
		}
	}
}

func TestWebhook_BotSenderAndDeliveryIDs(t *testing.T) {
	p, _, _ := seededProvider(t)
	req := delivery("issue_comment", "d-bot", map[string]any{"action": "created", "repository": repoPayload, "sender": map[string]any{"login": "dependabot[bot]", "type": "Bot"},
		"issue": map[string]any{"number": 1}, "comment": map[string]any{"id": 1, "body": "@paca-bot hello"}})
	ev := parseOne(t, p, req)
	if ev.Actor.Kind != gp.ActorBot || ev.Actor.Login != "dependabot[bot]" {
		t.Errorf("a bot sender is reported as a bot: %+v", ev.Actor)
	}
	if ev.ID != "d-bot:issue_comment" || ev.RawRef != "d-bot" {
		t.Errorf("ids: %q %q", ev.ID, ev.RawRef)
	}

	// Without a delivery header the id still identifies the body, so a redelivery stays a duplicate.
	delete(req.Headers, "x-github-delivery")
	a, b := parseOne(t, p, req), parseOne(t, p, req)
	if a.ID == "" || a.ID != b.ID {
		t.Errorf("fallback ids: %q %q", a.ID, b.ID)
	}
}

func TestWebhook_SignatureEdgeCases(t *testing.T) {
	p, _, _ := seededProvider(t)
	base := delivery("issue_comment", "d-sig", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender,
		"issue": map[string]any{"number": 1}, "comment": map[string]any{"id": 1, "body": "hi"}})
	cases := map[string]string{
		"missing":         "",
		"wrong prefix":    "sha1=" + strings.Repeat("0", 40),
		"no prefix":       strings.TrimPrefix(base.Headers["x-hub-signature-256"], "sha256="),
		"truncated":       base.Headers["x-hub-signature-256"][:20],
		"upper-cased hex": "sha256=" + strings.ToUpper(strings.TrimPrefix(base.Headers["x-hub-signature-256"], "sha256=")),
		"trailing space":  base.Headers["x-hub-signature-256"] + " ",
		"valid but other": sign([]byte("other body"), fakeWebhookSecret),
		"valid wrong key": sign(base.Body, "another-secret"),
	}
	for name, sig := range cases {
		req := base
		req.Headers = map[string]string{"x-github-event": "issue_comment", "x-github-delivery": "d", "x-hub-signature-256": sig}
		if _, err := p.VerifyAndParseWebhook(bg, req); codeOf(t, err) != gp.CodeInvalidSignature {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := p.VerifyAndParseWebhook(bg, base); err != nil {
		t.Errorf("the untouched delivery verifies: %v", err)
	}
	unknown := base
	unknown.Identity.ID = "nope"
	if _, err := p.VerifyAndParseWebhook(bg, unknown); codeOf(t, err) != gp.CodeNotFound {
		t.Errorf("unknown identity: %v", err)
	}
	notJSON := base
	notJSON.Body = []byte("{")
	notJSON.Headers = map[string]string{"x-github-event": "push", "x-hub-signature-256": sign(notJSON.Body, fakeWebhookSecret)}
	if _, err := p.VerifyAndParseWebhook(bg, notJSON); codeOf(t, err) != gp.CodeValidation {
		t.Errorf("a signed body that is not JSON: %v", err)
	}
}

func TestMentionsHandle_MoreCases(t *testing.T) {
	for text, want := range map[string]bool{
		"@paca-bot":            true,
		"hey @Paca-Bot, look":  true,
		"@paca-bot[bot] ping":  true,
		"(@paca-bot)":          true,
		"line one\n@paca-bot":  true,
		"mail me@paca-bot.dev": false,
		"@paca-bot-two":        false,
		"@paca-botx":           false,
		"x@paca-bot":           false,
		"@@paca-bot":           false,
		"paca-bot":             false,
		"":                     false,
	} {
		if got := mentionsHandle(text, "paca-bot"); got != want {
			t.Errorf("mentionsHandle(%q) = %v, want %v", text, got, want)
		}
	}
	// A handle with regex metacharacters is matched literally and cached per handle.
	if !mentionsHandle("@a.b", "a.b") || mentionsHandle("@axb", "a.b") {
		t.Error("a handle is matched literally")
	}
	first, second := mentionRegexp("paca-bot"), mentionRegexp("paca-bot")
	if first != second {
		t.Error("the compiled pattern is reused")
	}
}

func TestVerifyHMAC(t *testing.T) {
	body := []byte(`{"a":1}`)
	good := sign(body, "s3cret")
	if !verifyHMAC(body, "s3cret", good) {
		t.Fatal("a correct signature verifies")
	}
	for name, sig := range map[string]string{"empty": "", "bare prefix": "sha256=", "wrong secret": sign(body, "other"), "garbage": "sha256=zz"} {
		if verifyHMAC(body, "s3cret", sig) {
			t.Errorf("%s must not verify", name)
		}
	}
	if verifyHMAC([]byte("tampered"), "s3cret", good) {
		t.Error("a tampered body must not verify")
	}
}
