package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

func grantReq(owner string) gp.BeginAccessRequest {
	return gp.BeginAccessRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, Owner: owner, ReturnURL: "https://paca.example.com/projects/p", CallbackURL: "https://paca.example.com/git/setup/callback"}
}

func TestAccessGrant_OpensTheInstallPageOnTheOwnersAccount(t *testing.T) {
	p, g, st := seededProvider(t)
	step, err := p.BeginAccessGrant(bg, grantReq("other-org"))
	if err != nil {
		t.Fatal(err)
	}
	u, perr := url.Parse(step.URL)
	if perr != nil || step.Kind != gp.StepRedirect || u.Host != "github.com" || u.Path != "/apps/"+fakeSlug+"/installations/new/permissions" {
		t.Fatalf("step = %+v", step)
	}
	if u.Query().Get("target_id") != "556" || u.Query().Get("state") != step.State || step.State == "" {
		t.Errorf("query = %v (want the owner's account id and our state)", u.Query())
	}
	if s := st.setups[step.State]; s.Scope != accessScope || s.AppID != fakeAppRowID || s.OwnerOrg != "other-org" || !s.ExpiresAt.After(time.Now()) {
		t.Errorf("session = %+v", s)
	}
	if g.called("GET /users/other-org") != 1 {
		t.Error("the owner's account id comes from GitHub")
	}

	// No owner: GitHub's own account picker.
	step, err = p.BeginAccessGrant(bg, grantReq(""))
	if err != nil || !strings.HasPrefix(step.URL, "https://github.com/apps/"+fakeSlug+"/installations/new?state=") || strings.Contains(step.URL, "target_id") {
		t.Errorf("no owner: %+v %v", step, err)
	}
}

func TestAccessGrant_BeginValidation(t *testing.T) {
	p, _, _ := seededProvider(t)
	r := grantReq("acme")
	r.CallbackURL = ""
	if _, err := p.BeginAccessGrant(bg, r); codeOf(t, err) != gp.CodeValidation {
		t.Errorf("callback: %v", err)
	}
	for _, owner := range []string{"-bad-", "a/b", "../x", "x y"} {
		if _, err := p.BeginAccessGrant(bg, grantReq(owner)); codeOf(t, err) != gp.CodeValidation {
			t.Errorf("owner %q: %v", owner, err)
		}
	}
	if _, err := p.BeginAccessGrant(bg, grantReq("no-such-account")); codeOf(t, err) != gp.CodeNotFound {
		t.Errorf("unknown account: %v", err)
	}
	r = grantReq("acme")
	r.Identity.ID = "nope"
	if _, err := p.BeginAccessGrant(bg, r); codeOf(t, err) != gp.CodeNotFound {
		t.Errorf("unknown identity: %v", err)
	}
}

func TestAccessGrant_CompleteRecordsTheInstallationAndMakesItUsable(t *testing.T) {
	p, _, st := seededProvider(t)
	st.installs[fakeAppRowID] = nil // installed nowhere yet, as right after creating the App
	step, _ := p.BeginAccessGrant(bg, grantReq("acme"))

	grant, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, State: step.State, Params: map[string]string{"installation_id": "99", "setup_action": "install"}})
	if err != nil || grant.Owner != "acme" {
		t.Fatalf("%+v %v", grant, err)
	}
	if ins := st.installs[fakeAppRowID]; len(ins) != 1 || ins[0].AccountLogin != "acme" || ins[0].InstallationID != 99 {
		t.Errorf("installations = %+v", ins)
	}
	// The repository is now reachable for that owner.
	if _, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: []gp.IdentityRef{{ID: fakeAppRowID}}}); err != nil {
		t.Errorf("resolve after grant: %v", err)
	}
	// The state is single-use.
	if _, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, State: step.State, Params: map[string]string{"installation_id": "99"}}); codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("replay: %v", err)
	}
}

func TestAccessGrant_CompleteGuards(t *testing.T) {
	p, _, st := seededProvider(t)
	id := gp.IdentityRef{ID: fakeAppRowID}
	step, _ := p.BeginAccessGrant(bg, grantReq("acme"))
	call := func(state string, params map[string]string) error {
		_, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: id, State: state, Params: params})
		return err
	}
	if codeOf(t, call("", map[string]string{"installation_id": "99"})) != gp.CodeValidation {
		t.Error("state required")
	}
	if codeOf(t, call("never-issued", map[string]string{"installation_id": "99"})) != gp.CodeForbidden {
		t.Error("unknown state")
	}
	if codeOf(t, call(step.State, nil)) != gp.CodeValidation {
		t.Error("installation id required")
	}
	if codeOf(t, call(step.State, map[string]string{"installation_id": "x"})) != gp.CodeValidation {
		t.Error("installation id must be numeric")
	}
	// An organization owner must approve first: nothing is granted, and the state stays usable.
	if codeOf(t, call(step.State, map[string]string{"setup_action": "request"})) != gp.CodeConflict {
		t.Error("pending approval")
	}
	// An installation GitHub does not return for this App stores nothing.
	before := len(st.installs[fakeAppRowID])
	if codeOf(t, call(step.State, map[string]string{"installation_id": "424242"})) != gp.CodeNotFound || len(st.installs[fakeAppRowID]) != before {
		t.Error("another App's installation id must be refused and not stored")
	}
	// Expired.
	s := st.setups[step.State]
	s.ExpiresAt = time.Now().Add(-time.Minute)
	st.setups[step.State] = s
	if codeOf(t, call(step.State, map[string]string{"installation_id": "99"})) != gp.CodeForbidden {
		t.Error("expired")
	}
	// A state issued to one identity cannot be completed as another.
	other := appRecord{ID: "app-row-2", GitHubAppID: 4242, Slug: "other", Name: "Other", WebhookSecret: "x", PrivateKeyPEM: st.apps[fakeAppRowID].PrivateKeyPEM}
	st.apps[other.ID] = other
	step2, _ := p.BeginAccessGrant(bg, grantReq("acme"))
	_, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: gp.IdentityRef{ID: other.ID}, State: step2.State, Params: map[string]string{"installation_id": "99"}})
	if codeOf(t, err) != gp.CodeForbidden {
		t.Errorf("cross-identity: %v", err)
	}
}

// An App installed on GitHub directly (not through a Paca grant flow) takes
// effect the first time a repository of that account is needed.
func TestInstallationsMadeOnGitHubAreDiscovered(t *testing.T) {
	p, g, st := seededProvider(t)
	if len(st.installs[fakeAppRowID]) != 1 {
		t.Fatal("setup assumption")
	}
	res, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "https://github.com/other-org/lib", Candidates: []gp.IdentityRef{{ID: fakeAppRowID}}})
	if err != nil || res.Repository.Ref.FullPath != "other-org/lib" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(st.installs[fakeAppRowID]) != 2 {
		t.Errorf("the installation should now be recorded: %+v", st.installs[fakeAppRowID])
	}
	if g.called("GET /app/installations") < 1 {
		t.Error("GitHub should have been asked which accounts the App is installed on")
	}
}

func TestInstallationDiscoveryIsRateLimited(t *testing.T) {
	p, g, _ := seededProvider(t)
	for i := 0; i < 4; i++ {
		_, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "nobody-here/repo", Candidates: []gp.IdentityRef{{ID: fakeAppRowID}}})
		if codeOf(t, err) != gp.CodeNotFound {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	n := 0
	for _, c := range g.calls {
		if c == "GET /app/installations" { // the list, not one installation
			n++
		}
	}
	if n != 1 {
		// the list endpoint is requested once per window, however often an unknown owner is asked about
		t.Errorf("GitHub was asked %d times for installations, want 1", n)
	}
}

func TestListRepositoriesDiscoversWhenNothingIsKnown(t *testing.T) {
	p, _, st := seededProvider(t)
	st.installs[fakeAppRowID] = nil
	page, err := p.ListRepositories(bg, gp.ListReposRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("%+v %v", page, err)
	}
}

// A public repository answers any token, so reading it proves nothing about
// whether the installation covers it. Linking must not succeed for a repository
// no credential can ever be minted for.
func TestResolveRepository_RequiresTheInstallationToCoverTheRepository(t *testing.T) {
	p, g, _ := seededProvider(t)
	cands := []gp.IdentityRef{{ID: fakeAppRowID}}

	// The installation on other-org covers only "lib".
	covered, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "other-org/lib", Candidates: cands})
	if err != nil || covered.Repository.Ref.FullPath != "other-org/lib" {
		t.Fatalf("covered repository: %+v %v", covered, err)
	}
	_, err = p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "other-org/public-extra", Candidates: cands})
	if codeOf(t, err) != gp.CodeNotFound {
		t.Fatalf("a public repository outside the selection must not resolve: %v", err)
	}
	// What resolves is what a credential can be minted for.
	if _, err := p.MintCredential(bg, gp.MintRequest{Target: gp.Target{Identity: cands[0], Repo: covered.Repository.Ref}, Access: gp.AccessRead}); err != nil {
		t.Errorf("mint for a resolved repository: %v", err)
	}

	// "All repositories" installations need no per-repository check.
	scoped := func() (n int) {
		for _, m := range g.mints {
			if !m.Unscoped {
				n++
			}
		}
		return n
	}
	before := scoped()
	if _, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: cands}); err != nil {
		t.Fatal(err)
	}
	if scoped() != before {
		t.Errorf("an all-repositories installation should cost no scoped token request: %d new", scoped()-before)
	}
}

// GitHub refuses an App JWT on /users/{login}; the owner's account id must be
// looked up with an installation token, or anonymously when there is none.
func TestAccessGrant_LooksUpTheOwnerWithoutAnAppJWT(t *testing.T) {
	p, g, st := seededProvider(t)
	if _, err := p.BeginAccessGrant(bg, grantReq("other-org")); err != nil {
		t.Fatal(err)
	}
	if len(g.userLookups) != 1 || !g.userLookups[0] {
		t.Errorf("with an installation available the lookup should use its token: %v", g.userLookups)
	}

	st.installs[fakeAppRowID] = nil // freshly created App, installed nowhere
	g.userLookups = nil
	if _, err := p.BeginAccessGrant(bg, grantReq("other-org")); err != nil {
		t.Fatalf("an App that is installed nowhere must still be able to start a grant: %v", err)
	}
	if len(g.userLookups) != 1 || g.userLookups[0] {
		t.Errorf("with no installation the lookup should be anonymous: %v", g.userLookups)
	}
}

// Uninstalling and reinstalling an App on an account gives a new installation
// id. The old row is dead and must not be picked ahead of the new one.
func TestAccessGrant_ReinstallReplacesTheDeadInstallation(t *testing.T) {
	p, g, st := seededProvider(t)
	g.installs[199] = fakeInstall{Login: "acme", Type: "Organization"} // acme reinstalled the App: new id
	delete(g.installs, 99)                                             // the old one is gone on GitHub

	step, _ := p.BeginAccessGrant(bg, grantReq("acme"))
	if _, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: gp.IdentityRef{ID: fakeAppRowID}, State: step.State, Params: map[string]string{"installation_id": "199"}}); err != nil {
		t.Fatal(err)
	}
	ins := st.installs[fakeAppRowID]
	if len(ins) != 1 || ins[0].InstallationID != 199 {
		t.Fatalf("only the live installation should remain: %+v", ins)
	}
	if _, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: []gp.IdentityRef{{ID: fakeAppRowID}}}); err != nil {
		t.Errorf("the new installation should be the one used: %v", err)
	}
}

// A cached installation token that GitHub revoked is replaced once, instead of
// failing every call until the cache entry would have expired.
func TestRevokedCachedTokenIsReplacedOnce(t *testing.T) {
	p, g, _ := seededProvider(t)
	if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"}); err != nil {
		t.Fatal(err)
	}
	g.revokeTokens() // the cached token is now dead, the installation is fine
	before := len(g.mints)
	if _, err := p.GetChangeRequest(bg, gp.CRRequest{Target: webTarget(), Number: "1"}); err != nil {
		t.Fatalf("a revoked cached token must be replaced transparently: %v", err)
	}
	if len(g.mints) != before+1 {
		t.Errorf("exactly one new token should have been minted, got %d", len(g.mints)-before)
	}
}

// After GitHub uninstalls the App, the record is dropped so the account reads as
// "not installed" again (and core offers the install) instead of failing forever.
func TestUninstalledAppIsForgottenAndCanBeGrantedAgain(t *testing.T) {
	p, g, st := seededProvider(t)
	cands := []gp.IdentityRef{{ID: fakeAppRowID}}
	if _, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: cands}); err != nil {
		t.Fatal(err) // caches a token for installation 99
	}
	g.uninstall(99)

	_, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: cands})
	if codeOf(t, err) != gp.CodeNotFound {
		t.Fatalf("an uninstalled App must read as not installed: %v", err)
	}
	for _, in := range st.installs[fakeAppRowID] {
		if in.InstallationID == 99 {
			t.Errorf("the dead installation was kept: %+v", st.installs[fakeAppRowID])
		}
	}
	// Starting a grant must still work: the owner lookup cannot depend on the dead token.
	step, err := p.BeginAccessGrant(bg, grantReq("acme"))
	if err != nil || step.State == "" {
		t.Fatalf("grant after uninstall: %+v %v", step, err)
	}
	// The user installs it again: a new installation id.
	g.mu.Lock()
	g.installs[205] = fakeInstall{Login: "acme", Type: "Organization", Selected: []string{"web"}}
	g.mu.Unlock()
	if _, err := p.CompleteAccessGrant(bg, gp.CompleteAccessRequest{Identity: cands[0], State: step.State, Params: map[string]string{"installation_id": "205"}}); err != nil {
		t.Fatal(err)
	}
	if res, err := p.ResolveRepository(bg, gp.ResolveRepoRequest{PathOrURL: "acme/web", Candidates: cands}); err != nil || res.Repository.Ref.FullPath != "acme/web" {
		t.Errorf("after reinstalling: %+v %v", res, err)
	}
}
