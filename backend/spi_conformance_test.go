package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
	"github.com/Paca-AI/plugin-sdk-go/gitprovider/conformance"
)

// memStore is an in-memory spiStore.
type memStore struct {
	mu       sync.Mutex
	apps     map[string]appRecord
	installs map[string][]installRecord
	setups   map[string]setupSession
	cache    map[string]memToken
	seq      int
}

type memToken struct {
	tok string
	exp time.Time
}

func newMemStore() *memStore {
	return &memStore{apps: map[string]appRecord{}, installs: map[string][]installRecord{}, setups: map[string]setupSession{}, cache: map[string]memToken{}}
}

func (m *memStore) loadApp(id string) (appRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.apps[id]
	if !ok {
		return appRecord{}, errNoSuchApp
	}
	return a, nil
}
func (m *memStore) installations(appID string) ([]installRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]installRecord(nil), m.installs[appID]...), nil
}
func (m *memStore) createApp(a appRecord) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	a.ID = "created-app-" + string(rune('0'+m.seq))
	m.apps[a.ID] = a
	return a.ID, nil
}
func (m *memStore) upsertInstallation(appID string, i installRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.installs[appID] {
		if e.InstallationID == i.InstallationID {
			i.RowID = e.RowID
			m.installs[appID][k] = i
			return nil
		}
	}
	m.seq++
	i.RowID = "inst-row-" + string(rune('0'+m.seq))
	// The same account's older installation of this App is dead (see spiStore).
	kept := m.installs[appID][:0:0]
	for _, e := range m.installs[appID] {
		if !strings.EqualFold(e.AccountLogin, i.AccountLogin) {
			kept = append(kept, e)
		}
	}
	m.installs[appID] = append(kept, i)
	return nil
}
func (m *memStore) deleteApp(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.apps, id)
	delete(m.installs, id)
	return nil
}
func (m *memStore) deleteInstallation(appID string, installationID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.installs[appID][:0:0]
	for _, e := range m.installs[appID] {
		if e.InstallationID != installationID {
			kept = append(kept, e)
		}
	}
	m.installs[appID] = kept
	return nil
}
func (m *memStore) cacheDelete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, key)
}
func (m *memStore) saveSetup(s setupSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setups[s.State] = s
	return nil
}
func (m *memStore) loadSetup(state string) (setupSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.setups[state]
	if !ok {
		return setupSession{}, errNoSuchSetup
	}
	return s, nil
}
func (m *memStore) updateSetup(s setupSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.setups[s.State]; !ok {
		return errNoSuchSetup
	}
	m.setups[s.State] = s
	return nil
}
func (m *memStore) cacheGet(key string) (string, time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.cache[key]
	return t.tok, t.exp, ok
}
func (m *memStore) cacheSet(key, token string, expires time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[key] = memToken{token, expires}
}

// seededProvider returns a provider with the fixture App installed on "acme".
func seededProvider(t *testing.T) (*ghProvider, *fakeGitHub, *memStore) {
	t.Helper()
	g := newFakeGitHub(t)
	st := newMemStore()
	st.apps[fakeAppRowID] = appRecord{ID: fakeAppRowID, GitHubAppID: g.appID, Slug: fakeSlug, Name: "Paca", WebhookSecret: fakeWebhookSecret, PrivateKeyPEM: g.pemKey(), WebhookActive: true}
	st.installs[fakeAppRowID] = []installRecord{{RowID: "inst-1", InstallationID: g.installationID, AccountLogin: "acme", AccountType: "Organization", Selection: "all"}}
	return newGHProvider(st), g, st
}

func sign(body []byte, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// delivery builds a signed GitHub webhook delivery.
func delivery(event, id string, payload map[string]any) gp.WebhookRequest {
	body, _ := json.Marshal(payload)
	return gp.WebhookRequest{
		Identity: gp.IdentityRef{ID: fakeAppRowID, Handle: fakeSlug}, Method: "POST", Body: body,
		Headers: map[string]string{"x-github-event": event, "x-github-delivery": id, "x-hub-signature-256": sign(body, fakeWebhookSecret), "content-type": "application/json"},
	}
}

var (
	repoPayload = map[string]any{"id": 1, "full_name": "acme/web", "default_branch": "main"}
	userSender  = map[string]any{"login": "octocat", "type": "User"}
)

func conformanceFixtures() conformance.Fixtures {
	identity := gp.IdentityRef{ID: fakeAppRowID, Handle: fakeSlug}
	repo := gp.RepoRef{Host: githubHost, FullPath: "acme/web", DefaultBranch: "main"}
	bad := delivery("issue_comment", "d-bad", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender,
		"issue": map[string]any{"number": 1}, "comment": map[string]any{"id": 1, "body": "@paca-bot hi"}})
	bad.Headers["x-hub-signature-256"] = "sha256=deadbeef"

	prComment := func(id, body string, sender map[string]any) gp.WebhookRequest {
		return delivery("issue_comment", id, map[string]any{"action": "created", "repository": repoPayload, "sender": sender,
			"issue":   map[string]any{"number": 1, "title": "Add login", "user": userSender, "pull_request": map[string]any{"url": "x"}},
			"comment": map[string]any{"id": 11, "body": body}})
	}
	return conformance.Fixtures{
		Identity: identity, Repo: repo,
		InvisibleRepo: gp.RepoRef{Host: githubHost, FullPath: "secret/hidden"},
		RepoURL:       "https://github.com/acme/web.git", Candidates: []gp.IdentityRef{identity},
		ExpectedRepoPaths: []string{"acme/web", "acme/api"},

		ChangeRequest: "1", MissingCR: "999",
		ChangedFiles: []string{"src/main.go", "src/util.go", "docs/guide.md"},
		HeadSHA:      "abc123", FailingCheckID: "run:5001",
		IssueNumber: "7", ThreadID: "PRRT_1",
		Actors: map[string]gp.Permission{"octocat": gp.PermissionWrite, "reader": gp.PermissionRead, "owner": gp.PermissionAdmin, "lead": gp.PermissionMaintain},

		BadSignature: bad,
		Secrets:      []string{fakeWebhookSecret, "ghs_token_", "BEGIN RSA PRIVATE KEY"},
		Webhooks: []conformance.WebhookCase{
			{Name: "mention-in-pr-comment", Request: prComment("d1", "@paca-bot please fix the test", userSender),
				Want: []conformance.Want{{Kind: gp.EventComment, Action: "created", RepoPath: "acme/web", SubjectKind: gp.SubjectChangeRequest, SubjectNumber: "1", ActorLogin: "octocat", ActorKind: gp.ActorUser, Addressed: []gp.AddressVia{gp.ViaMention}}}},
			{Name: "mention-with-bot-suffix", Request: prComment("d1b", "hey @Paca-Bot[bot], look", userSender),
				Want: []conformance.Want{{Kind: gp.EventComment, Addressed: []gp.AddressVia{gp.ViaMention}}}},
			{Name: "comment-without-mention",
				Request: delivery("issue_comment", "d2", map[string]any{"action": "created", "repository": repoPayload, "sender": map[string]any{"login": "reader", "type": "User"},
					"issue": map[string]any{"number": 7, "title": "Bug"}, "comment": map[string]any{"id": 12, "body": "thanks!"}}),
				Want: []conformance.Want{{Kind: gp.EventComment, SubjectKind: gp.SubjectIssue, SubjectNumber: "7", ActorLogin: "reader", ActorKind: gp.ActorUser}}},
			{Name: "email-address-is-not-a-mention", Request: prComment("d3", "mail me at dev@paca-bot.example", userSender),
				Want: []conformance.Want{{Kind: gp.EventComment, ActorLogin: "octocat"}}},
			{Name: "longer-handle-is-not-a-mention", Request: prComment("d3b", "@paca-bot-2 is another bot", userSender),
				Want: []conformance.Want{{Kind: gp.EventComment}}},
			{Name: "bot-actor", Request: prComment("d4", "bump", map[string]any{"login": "dependabot[bot]", "type": "Bot"}),
				Want: []conformance.Want{{Kind: gp.EventComment, ActorLogin: "dependabot[bot]", ActorKind: gp.ActorBot}}},
			{Name: "review-request-is-not-a-trigger-on-github",
				Request: delivery("pull_request", "d5", map[string]any{"action": "review_requested", "repository": repoPayload, "sender": userSender,
					"pull_request": map[string]any{"number": 1}}),
				Want: nil},
			{Name: "ci-failed",
				Request: delivery("check_run", "d7", map[string]any{"action": "completed", "repository": repoPayload, "sender": map[string]any{"login": "github-actions[bot]", "type": "Bot"},
					"check_run": map[string]any{"id": 5001, "name": "ci/test", "head_sha": "abc123", "conclusion": "failure",
						"pull_requests": []map[string]any{{"number": 1, "head": map[string]string{"ref": "feat/PACA-12-login", "sha": "abc123"}, "base": map[string]string{"ref": "main"}}}}}),
				Want: []conformance.Want{{Kind: gp.EventCheck, Action: "completed", ActorKind: gp.ActorBot, CheckConclusion: "failure", SubjectKind: gp.SubjectChangeRequest, SubjectNumber: "1"}}},
			{Name: "branch-created-by-push",
				Request: delivery("push", "d10", map[string]any{"ref": "refs/heads/feat/PACA-12-x", "after": "def456", "created": true, "repository": repoPayload, "sender": userSender}),
				Want:    []conformance.Want{{Kind: gp.EventPush, Action: "created", ActorLogin: "octocat"}}},
			{Name: "tag-push-is-dropped",
				Request: delivery("push", "d10b", map[string]any{"ref": "refs/tags/v1", "after": "def456", "repository": repoPayload, "sender": userSender}),
				Want:    nil},
			{Name: "pr-opened-with-mention-in-body",
				Request: delivery("pull_request", "d11", map[string]any{"action": "opened", "repository": repoPayload, "sender": userSender,
					"pull_request": map[string]any{"number": 3, "title": "Fix it", "body": "@paca-bot can you review?", "user": userSender,
						"head": map[string]any{"ref": "fix", "sha": "s1", "repo": map[string]string{"full_name": "acme/web"}}, "base": map[string]any{"ref": "main"}}}),
				Want: []conformance.Want{{Kind: gp.EventChangeRequest, Action: "opened", SubjectKind: gp.SubjectChangeRequest, SubjectNumber: "3", Addressed: []gp.AddressVia{gp.ViaMention}}}},
			{Name: "review-submitted",
				Request: delivery("pull_request_review", "d12", map[string]any{"action": "submitted", "repository": repoPayload, "sender": userSender,
					"review": map[string]any{"id": 9, "state": "changes_requested", "body": "needs work"}, "pull_request": map[string]any{"number": 1, "user": userSender}}),
				Want: []conformance.Want{{Kind: gp.EventReview, Action: "submitted", SubjectNumber: "1"}}},
			{Name: "review-requested-from-the-app",
				Request: delivery("pull_request", "d13", map[string]any{"action": "review_requested", "repository": repoPayload, "sender": userSender,
					"requested_reviewer": map[string]any{"login": "paca-bot[bot]", "type": "Bot"},
					"pull_request": map[string]any{"number": 4, "title": "Add x", "user": userSender,
						"head": map[string]any{"ref": "x", "sha": "s1", "repo": map[string]string{"full_name": "acme/web"}}, "base": map[string]any{"ref": "main"}}}),
				Want: []conformance.Want{{Kind: gp.EventReviewRequest, Action: "requested", SubjectKind: gp.SubjectChangeRequest, SubjectNumber: "4", Addressed: []gp.AddressVia{gp.ViaReviewRequest}}}},
			{Name: "review-requested-from-a-person",
				Request: delivery("pull_request", "d15", map[string]any{"action": "review_requested", "repository": repoPayload, "sender": userSender,
					"requested_reviewer": map[string]any{"login": "bob", "type": "User"},
					"pull_request": map[string]any{"number": 4, "title": "Add x", "user": userSender,
						"head": map[string]any{"ref": "x", "sha": "s1", "repo": map[string]string{"full_name": "acme/web"}}, "base": map[string]any{"ref": "main"}}}),
				Want: nil},
			{Name: "issue-assigned-to-the-app",
				Request: delivery("issues", "d16", map[string]any{"action": "assigned", "repository": repoPayload, "sender": userSender,
					"assignee": map[string]any{"login": "paca-bot[bot]", "type": "Bot"},
					"issue":    map[string]any{"number": 7, "title": "Bug", "body": "It breaks", "user": userSender}}),
				Want: []conformance.Want{{Kind: gp.EventAssignment, Action: "assigned", SubjectKind: gp.SubjectIssue, SubjectNumber: "7", Addressed: []gp.AddressVia{gp.ViaAssignment}}}},
			{Name: "issue-assigned-to-a-person",
				Request: delivery("issues", "d17", map[string]any{"action": "assigned", "repository": repoPayload, "sender": userSender,
					"assignee": map[string]any{"login": "bob", "type": "User"},
					"issue":    map[string]any{"number": 7, "title": "Bug", "user": userSender}}),
				Want: nil},
			{Name: "label-added",
				Request: delivery("issues", "d13l", map[string]any{"action": "labeled", "repository": repoPayload, "sender": userSender,
					"issue": map[string]any{"number": 7, "title": "Bug", "user": userSender}, "label": map[string]string{"name": "paca"}}),
				Want: []conformance.Want{{Kind: gp.EventLabel, Action: "labeled", SubjectKind: gp.SubjectIssue, SubjectNumber: "7"}}},
			{Name: "ping-is-dropped", Request: delivery("ping", "d14", map[string]any{"zen": "Keep it logically awesome.", "sender": userSender}), Want: nil},
			{Name: "unmapped-event-is-dropped",
				Request: delivery("star", "d8", map[string]any{"action": "created", "repository": repoPayload, "sender": userSender}), Want: nil},
		},

		SetupHost:           githubHost,
		SetupCompleteParams: map[string]string{"code": "c0de"},

		MutationsAllowed: true,
		NewBranchHead:    "feat/new", BaseBranch: "main",
		File:        struct{ Ref, Path string }{Ref: "main", Path: "README.md"},
		SearchQuery: "package main",
		Reviewer:    "reader",

		AccessGrantParams: map[string]string{"installation_id": "99"}, AccessOwner: "acme",
	}
}

// TestGitHubProviderConformance runs the SDK's provider conformance suite
// against the GitHub adapter wired to a fake GitHub.
func TestGitHubProviderConformance(t *testing.T) {
	conformance.Run(t, conformance.Harness{
		New: func(t *testing.T) gp.Provider {
			p, _, _ := seededProvider(t)
			return p
		},
		Fixtures: conformanceFixtures(),
	})
}
