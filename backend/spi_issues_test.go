package main

import (
	"encoding/json"
	"net/url"
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

func TestListIssuesFiltersPullRequestsWithoutLosingPages(t *testing.T) {
	p, g, _ := seededProvider(t)
	fetch = func(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != "/repos/acme/web/issues" {
			return g.fetch(method, rawURL, headers, body)
		}
		if method != "GET" || u.Query().Get("state") != "closed" || u.Query().Get("per_page") != "1" {
			t.Fatalf("unexpected request: %s %s", method, rawURL)
		}
		switch u.Query().Get("page") {
		case "1":
			return resp(200, []map[string]any{{"number": 1, "pull_request": map[string]any{"url": "x"}}})
		case "2":
			return resp(200, []map[string]any{{"number": 101, "title": "Bug", "body": "Details", "state": "closed", "html_url": "https://github.com/acme/web/issues/101", "user": map[string]string{"login": "alice"}, "labels": []map[string]string{{"name": "bug"}}, "assignees": []map[string]string{{"login": "bob"}}}})
		case "3":
			return resp(200, []map[string]any{})
		default:
			t.Fatalf("unexpected page: %s", rawURL)
			return nil, nil
		}
	}
	first, err := p.ListIssues(bg, gp.ListIssuesRequest{Target: webTarget(), State: "closed"})
	if err != nil || len(first.Items) != 0 || first.NextCursor != "2" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, err := p.ListIssues(bg, gp.ListIssuesRequest{Target: webTarget(), State: "closed", Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "3" {
		t.Fatalf("second page: %+v, %v", second, err)
	}
	third, err := p.ListIssues(bg, gp.ListIssuesRequest{Target: webTarget(), State: "closed", Cursor: second.NextCursor})
	if err != nil || len(third.Items) != 0 || third.NextCursor != "" {
		t.Fatalf("third page: %+v, %v", third, err)
	}
	i := second.Items[0]
	if i.Number != "101" || i.Title != "Bug" || i.Body != "Details" || i.State != "closed" || i.Author != "alice" || i.URL != "https://github.com/acme/web/issues/101" || len(i.Labels) != 1 || i.Labels[0] != "bug" || len(i.Assignees) != 1 || i.Assignees[0] != "bob" {
		t.Fatalf("issue fields: %+v", i)
	}
}

func TestListIssuesValidatesAndPropagatesErrors(t *testing.T) {
	p, g, _ := seededProvider(t)
	for _, req := range []gp.ListIssuesRequest{
		{Target: webTarget(), State: "invalid"},
		{Target: webTarget(), Cursor: "-1"},
	} {
		_, err := p.ListIssues(bg, req)
		if codeOf(t, err) != gp.CodeValidation {
			t.Fatalf("request %+v: %v", req, err)
		}
	}
	g.forceError["GET /repos/acme/web/issues"] = fakeResponse{403, `{"message":"Resource not accessible by integration"}`, nil}
	_, err := p.ListIssues(bg, gp.ListIssuesRequest{Target: webTarget()})
	if codeOf(t, err) != gp.CodeForbidden {
		t.Fatalf("GitHub error: %v", err)
	}
}

func TestListIssuesWireAndDefaultState(t *testing.T) {
	p, g, _ := seededProvider(t)
	fetch = func(method, rawURL string, headers map[string]string, body string) (*plugin.FetchResponse, error) {
		u, _ := url.Parse(rawURL)
		if u.Path == "/repos/acme/web/issues" {
			if u.Query().Get("state") != "open" {
				t.Fatalf("default state: %s", rawURL)
			}
			return resp(200, []any{})
		}
		return g.fetch(method, rawURL, headers, body)
	}
	if !p.Describe().Capabilities.IssueList {
		t.Fatal("issue listing must be declared")
	}
	body, err := json.Marshal(gp.ListIssuesRequest{Target: webTarget()})
	if err != nil {
		t.Fatal(err)
	}
	status, out := gp.Dispatch(bg, p, "list_issues", body)
	if status != 200 {
		t.Fatalf("wire status %d: %s", status, out)
	}
	var wire struct {
		OK     bool         `json:"ok"`
		Result gp.IssuePage `json:"result"`
	}
	if err := json.Unmarshal(out, &wire); err != nil || !wire.OK || len(wire.Result.Items) != 0 {
		t.Fatalf("wire result: %s (%v)", out, err)
	}
}
