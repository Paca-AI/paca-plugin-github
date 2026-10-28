package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// ghAPI is one authenticated view of the GitHub REST/GraphQL API, used by the
// Git provider. Every failure comes back as a *gp.Error so core handles one
// error type; GitHub's wording is kept short and never carries credentials.
type ghAPI struct {
	token string
	// user is set for a personal access token: it acts as a person, so it has no
	// check runs (only GitHub Apps create those).
	user bool
	// refresh, when set, replaces a token GitHub answered 401 to (revoked, for
	// example because the App was uninstalled and installed again) and the
	// request is retried once.
	refresh func() (string, error)
}

const maxErrorMessage = 200

func (a *ghAPI) headers(accept string) map[string]string {
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	h := map[string]string{"Accept": accept, "X-GitHub-Api-Version": ghAPIVersion}
	if a.token != "" {
		h["Authorization"] = "Bearer " + a.token
	}
	return h
}

// do performs a request and returns the body of a 2xx answer.
func (a *ghAPI) do(method, path string, body any, accept string) (string, error) {
	status, text, _, err := a.doRaw(method, path, body, accept)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", mapGitHubError(status, text, nil)
	}
	return text, nil
}

func (a *ghAPI) doRaw(method, path string, body any, accept string) (int, string, map[string]string, error) {
	rawURL := path
	if !strings.HasPrefix(path, "http") {
		rawURL = ghBaseURL + path
	}
	h := a.headers(accept)
	payload := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", nil, gp.Errorf(gp.CodeInternal, "could not encode request")
		}
		payload = string(b)
		h["Content-Type"] = "application/json"
	}
	resp, err := fetch(method, rawURL, h, payload)
	if err != nil {
		return 0, "", nil, gp.Errorf(gp.CodeTransient, "GitHub is unreachable")
	}
	if resp.Status == 401 && a.refresh != nil {
		refresh := a.refresh
		a.refresh = nil // one retry only
		tok, rerr := refresh()
		if rerr != nil {
			// More informative than the 401: for an uninstalled App this is
			// "no longer installed on this account".
			return 0, "", nil, rerr
		}
		a.token = tok
		h["Authorization"] = "Bearer " + tok
		if resp, err = fetch(method, rawURL, h, payload); err != nil {
			return 0, "", nil, gp.Errorf(gp.CodeTransient, "GitHub is unreachable")
		}
	}
	if resp.Status >= 400 {
		return resp.Status, resp.Body, resp.Headers, mapGitHubError(resp.Status, resp.Body, resp.Headers)
	}
	return resp.Status, resp.Body, resp.Headers, nil
}

func (a *ghAPI) get(path string, out any) error {
	text, err := a.do("GET", path, nil, "")
	if err != nil {
		return err
	}
	return decodeInto(text, out)
}

func (a *ghAPI) send(method, path string, body, out any) error {
	text, err := a.do(method, path, body, "")
	if err != nil {
		return err
	}
	if out == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	return decodeInto(text, out)
}

func decodeInto(text string, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return gp.Errorf(gp.CodeInternal, "GitHub returned an unreadable response")
	}
	return nil
}

// graphql runs one query. GraphQL answers 200 with an errors array, so those
// are classified too: NOT_FOUND → not_found, FORBIDDEN/INSUFFICIENT_SCOPES →
// forbidden, anything else → validation.
func (a *ghAPI) graphql(query string, vars map[string]any, out any) error {
	text, err := a.do("POST", ghBaseURL+"/graphql", map[string]any{"query": query, "variables": vars}, "")
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return gp.Errorf(gp.CodeInternal, "GitHub returned an unreadable response")
	}
	if len(env.Errors) > 0 {
		e := env.Errors[0]
		switch {
		case e.Type == "NOT_FOUND" || strings.Contains(e.Message, "Could not resolve to"):
			return gp.Errorf(gp.CodeNotFound, "not found on GitHub")
		case e.Type == "FORBIDDEN" || e.Type == "INSUFFICIENT_SCOPES":
			return gp.Errorf(gp.CodeForbidden, "the identity may not do this on GitHub")
		}
		return gp.Errorf(gp.CodeValidation, "%s", clip(e.Message))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorMessage {
		return s[:maxErrorMessage] + "…"
	}
	return s
}

// mapGitHubError classifies a failed REST call onto the SPI's error set.
func mapGitHubError(status int, body string, headers map[string]string) error {
	var eb struct {
		Message string `json:"message"`
		Errors  []any  `json:"errors"`
	}
	_ = json.Unmarshal([]byte(body), &eb)
	msg := clip(eb.Message)
	if msg == "" {
		msg = fmt.Sprintf("GitHub answered HTTP %d", status)
	}
	lower := strings.ToLower(msg)
	switch {
	case status == 429 || (status == 403 && (strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse") || headerOf(headers, "x-ratelimit-remaining") == "0")):
		e := gp.Errorf(gp.CodeRateLimited, "GitHub is rate limiting this identity")
		e.RetryAfterSeconds = retryAfter(headers)
		return e
	case status == 401:
		return gp.Errorf(gp.CodeForbidden, "GitHub rejected the identity's credentials")
	case status == 403:
		return gp.Errorf(gp.CodeForbidden, "%s", msg)
	case status == 404:
		return gp.Errorf(gp.CodeNotFound, "%s", msg)
	case status == 409 || status == 405: // 405: "not mergeable"
		return gp.Errorf(gp.CodeConflict, "%s", msg)
	case status == 406: // e.g. a diff beyond GitHub's size limit
		return gp.Errorf(gp.CodeValidation, "%s", msg)
	case status == 422:
		if strings.Contains(lower, "already exists") || strings.Contains(lower, "a pull request already") {
			return gp.Errorf(gp.CodeConflict, "%s", msg)
		}
		return gp.Errorf(gp.CodeValidation, "%s", msg)
	case status >= 500:
		return gp.Errorf(gp.CodeTransient, "GitHub is temporarily unavailable")
	}
	return gp.Errorf(gp.CodeInternal, "%s", msg)
}

func headerOf(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func retryAfter(h map[string]string) int {
	if v := headerOf(h, "retry-after"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if v := headerOf(h, "x-ratelimit-reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := int(time.Until(time.Unix(n, 0)).Seconds()); d > 0 {
				return d
			}
		}
	}
	return 0
}

// repoPath builds "/repos/{owner}/{name}" from a full path, rejecting anything
// that is not exactly owner/name.
func repoPath(fullPath string) (string, error) {
	owner, name, ok := splitFullPath(fullPath)
	if !ok {
		return "", gp.Errorf(gp.CodeValidation, "repository must be owner/name")
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func splitFullPath(p string) (owner, name string, ok bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
