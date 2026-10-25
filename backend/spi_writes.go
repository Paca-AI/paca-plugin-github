package main

import (
	"context"
	"strconv"
	"strings"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// OpenChangeRequest implements gp.Provider. Labels and reviewers are applied
// after the pull request exists; if either fails the request is still returned
// (retrying the open would only collide with the one just created) and the
// result lists what was actually applied. GitHub has no idempotency keys, so
// IdempotentWrites is not declared and core de-duplicates itself.
func (p *ghProvider) OpenChangeRequest(_ context.Context, req gp.OpenCRRequest) (gp.ChangeRequest, error) {
	if blank(req.Title) || blank(req.Head) || blank(req.Base) {
		return gp.ChangeRequest{}, gp.Errorf(gp.CodeValidation, "title, head and base are required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	var pr ghPR
	if err := api.send("POST", repo+"/pulls", map[string]any{
		"title": req.Title, "body": req.Body, "head": req.Head, "base": req.Base, "draft": req.Draft,
	}, &pr); err != nil {
		return gp.ChangeRequest{}, err
	}
	num := strconv.Itoa(pr.Number)
	if len(req.Labels) > 0 {
		if err := api.send("POST", repo+"/issues/"+num+"/labels", map[string]any{"labels": req.Labels}, nil); err == nil {
			pr.Labels = nil
			for _, l := range req.Labels {
				pr.Labels = append(pr.Labels, struct {
					Name string `json:"name"`
				}{l})
			}
		}
	}
	if len(req.Reviewers) > 0 {
		if err := api.send("POST", repo+"/pulls/"+num+"/requested_reviewers", map[string]any{"reviewers": req.Reviewers}, nil); err == nil {
			for _, r := range req.Reviewers {
				pr.RequestedReviewers = append(pr.RequestedReviewers, struct {
					Login string `json:"login"`
				}{r})
			}
		}
	}
	return pr.toCR(), nil
}

// UpdateChangeRequest implements gp.Provider. Only open and closed can be set;
// merging is its own operation. Toggling draft needs GraphQL.
func (p *ghProvider) UpdateChangeRequest(_ context.Context, req gp.UpdateCRRequest) (gp.ChangeRequest, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	patch := map[string]any{}
	if req.Title != nil {
		if blank(*req.Title) {
			return gp.ChangeRequest{}, gp.Errorf(gp.CodeValidation, "title must not be empty")
		}
		patch["title"] = *req.Title
	}
	if req.Body != nil {
		patch["body"] = *req.Body
	}
	if req.State != nil {
		if *req.State != gp.CROpen && *req.State != gp.CRClosed {
			return gp.ChangeRequest{}, gp.Errorf(gp.CodeValidation, "state can only be set to open or closed; merging is a separate operation")
		}
		patch["state"] = string(*req.State)
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	var pr ghPR
	if len(patch) > 0 {
		err = api.send("PATCH", repo+"/pulls/"+num, patch, &pr)
	} else {
		pr, err = p.getPR(api, repo, num)
	}
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	if req.Draft != nil && *req.Draft != pr.Draft {
		mutation := `mutation($id:ID!){ markPullRequestReadyForReview(input:{pullRequestId:$id}){ clientMutationId } }`
		if *req.Draft {
			mutation = `mutation($id:ID!){ convertPullRequestToDraft(input:{pullRequestId:$id}){ clientMutationId } }`
		}
		if err := api.graphql(mutation, map[string]any{"id": pr.NodeID}, nil); err != nil {
			return gp.ChangeRequest{}, err
		}
		pr.Draft = *req.Draft
	}
	return pr.toCR(), nil
}

// Comment implements gp.Provider: a conversation comment on an issue or change request.
func (p *ghProvider) Comment(_ context.Context, req gp.CommentRequest) (gp.CommentResult, error) {
	if blank(req.Body) {
		return gp.CommentResult{}, gp.Errorf(gp.CodeValidation, "the comment is empty")
	}
	num, err := number(req.Subject.Number)
	if err != nil {
		return gp.CommentResult{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.CommentResult{}, err
	}
	var c ghComment
	if err := api.send("POST", repo+"/issues/"+num+"/comments", map[string]any{"body": req.Body}, &c); err != nil {
		return gp.CommentResult{}, err
	}
	return gp.CommentResult{ID: strconv.FormatInt(c.ID, 10), URL: c.HTMLURL}, nil
}

// ReplyThread implements gp.Provider (GraphQL; the thread id is the node id
// ListThreads returned).
func (p *ghProvider) ReplyThread(_ context.Context, req gp.ReplyThreadRequest) (gp.CommentResult, error) {
	if blank(req.Body) {
		return gp.CommentResult{}, gp.Errorf(gp.CodeValidation, "the reply is empty")
	}
	if blank(req.ThreadID) {
		return gp.CommentResult{}, gp.Errorf(gp.CodeValidation, "thread_id is required")
	}
	api, _, err := p.apiFor(req.Target)
	if err != nil {
		return gp.CommentResult{}, err
	}
	var out struct {
		AddPullRequestReviewThreadReply struct {
			Comment struct {
				DatabaseID int64  `json:"databaseId"`
				URL        string `json:"url"`
			} `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	if err := api.graphql(`mutation($id:ID!,$body:String!){ addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$id, body:$body}){ comment{ databaseId url } } }`,
		map[string]any{"id": req.ThreadID, "body": req.Body}, &out); err != nil {
		return gp.CommentResult{}, err
	}
	c := out.AddPullRequestReviewThreadReply.Comment
	return gp.CommentResult{ID: strconv.FormatInt(c.DatabaseID, 10), URL: c.URL}, nil
}

// SubmitReview implements gp.Provider. GitHub reviews are batched natively, so
// BatchReview is declared and nothing is emulated.
func (p *ghProvider) SubmitReview(_ context.Context, req gp.SubmitReviewRequest) (gp.ReviewResult, error) {
	event := map[gp.Verdict]string{gp.VerdictComment: "COMMENT", gp.VerdictApprove: "APPROVE", gp.VerdictRequestChanges: "REQUEST_CHANGES"}[req.Verdict]
	if event == "" {
		return gp.ReviewResult{}, gp.Errorf(gp.CodeValidation, "verdict must be comment, approve or request_changes")
	}
	num, err := number(req.Number)
	if err != nil {
		return gp.ReviewResult{}, err
	}
	comments := make([]map[string]any, 0, len(req.Comments))
	for _, c := range req.Comments {
		if blank(c.Path) || c.Line < 1 || blank(c.Body) {
			return gp.ReviewResult{}, gp.Errorf(gp.CodeValidation, "inline comments need a path, a line and a body")
		}
		side := "RIGHT"
		if c.Side == "old" {
			side = "LEFT"
		}
		comments = append(comments, map[string]any{"path": c.Path, "line": c.Line, "side": side, "body": c.Body})
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ReviewResult{}, err
	}
	body := map[string]any{"event": event, "body": req.Summary}
	if len(comments) > 0 {
		body["comments"] = comments
	}
	if req.HeadSHA != "" {
		body["commit_id"] = req.HeadSHA
	}
	var out struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := api.send("POST", repo+"/pulls/"+num+"/reviews", body, &out); err != nil {
		return gp.ReviewResult{}, err
	}
	return gp.ReviewResult{ID: strconv.FormatInt(out.ID, 10), URL: out.HTMLURL, Posted: len(comments)}, nil
}

// SetStatus implements gp.Provider as a check run (the App's own status line
// on the commit); StatusRef is the check run id, so a later call updates it.
func (p *ghProvider) SetStatus(_ context.Context, req gp.SetStatusRequest) (gp.StatusResult, error) {
	if blank(req.Title) {
		return gp.StatusResult{}, gp.Errorf(gp.CodeValidation, "title is required")
	}
	status, conclusion := "", ""
	switch req.State {
	case gp.RunQueued:
		status = "queued"
	case gp.RunInProgress:
		status = "in_progress"
	case gp.RunSuccess, gp.RunFailure, gp.RunCancelled:
		status, conclusion = "completed", string(req.State)
	default:
		return gp.StatusResult{}, gp.Errorf(gp.CodeValidation, "unknown state %q", req.State)
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.StatusResult{}, err
	}
	summary := req.Summary
	if blank(summary) {
		summary = req.Title
	}
	body := map[string]any{"name": req.Title, "status": status, "output": map[string]any{"title": req.Title, "summary": summary}}
	if conclusion != "" {
		body["conclusion"] = conclusion
	}
	if req.URL != "" {
		body["details_url"] = req.URL
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if req.StatusRef != "" {
		if _, err := strconv.ParseUint(req.StatusRef, 10, 63); err != nil {
			return gp.StatusResult{}, gp.Errorf(gp.CodeValidation, "status_ref is not a check run id")
		}
		if err := api.send("PATCH", repo+"/check-runs/"+req.StatusRef, body, &out); err != nil {
			return gp.StatusResult{}, err
		}
		return gp.StatusResult{StatusRef: req.StatusRef}, nil
	}
	sha := req.SHA
	if sha == "" && req.Subject.Kind == gp.SubjectChangeRequest && req.Subject.Number != "" {
		num, err := number(req.Subject.Number)
		if err != nil {
			return gp.StatusResult{}, err
		}
		pr, err := p.getPR(api, repo, num)
		if err != nil {
			return gp.StatusResult{}, err
		}
		sha = pr.Head.SHA
	}
	if sha == "" {
		return gp.StatusResult{}, gp.Errorf(gp.CodeValidation, "sha or a change request is required")
	}
	body["head_sha"] = sha
	if err := api.send("POST", repo+"/check-runs", body, &out); err != nil {
		return gp.StatusResult{}, err
	}
	return gp.StatusResult{StatusRef: strconv.FormatInt(out.ID, 10)}, nil
}

// ── optional operations ──────────────────────────────────────────────────────

// ResolveThread implements gp.ThreadResolver.
func (p *ghProvider) ResolveThread(_ context.Context, req gp.ResolveThreadRequest) (gp.Empty, error) {
	if blank(req.ThreadID) {
		return gp.Empty{}, gp.Errorf(gp.CodeValidation, "thread_id is required")
	}
	api, _, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Empty{}, err
	}
	name := "resolveReviewThread"
	if !req.Resolved {
		name = "unresolveReviewThread"
	}
	return gp.Empty{}, api.graphql(`mutation($id:ID!){ `+name+`(input:{threadId:$id}){ thread{ id } } }`, map[string]any{"id": req.ThreadID}, nil)
}

// AddLabels implements gp.LabelManager.
func (p *ghProvider) AddLabels(_ context.Context, req gp.LabelsRequest) (gp.Empty, error) {
	num, err := number(req.Subject.Number)
	if err != nil {
		return gp.Empty{}, err
	}
	if len(req.Labels) == 0 {
		return gp.Empty{}, gp.Errorf(gp.CodeValidation, "at least one label is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Empty{}, err
	}
	return gp.Empty{}, api.send("POST", repo+"/issues/"+num+"/labels", map[string]any{"labels": req.Labels}, nil)
}

// RequestReviewers implements gp.ReviewerRequester (people, not the bot: an
// ordinary GitHub App cannot be a requested reviewer).
func (p *ghProvider) RequestReviewers(_ context.Context, req gp.PeopleRequest) (gp.Empty, error) {
	if req.Subject.Kind != gp.SubjectChangeRequest {
		return gp.Empty{}, gp.Errorf(gp.CodeValidation, "reviewers can only be requested on a pull request")
	}
	num, err := number(req.Subject.Number)
	if err != nil {
		return gp.Empty{}, err
	}
	if len(req.Logins) == 0 {
		return gp.Empty{}, gp.Errorf(gp.CodeValidation, "at least one login is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Empty{}, err
	}
	return gp.Empty{}, api.send("POST", repo+"/pulls/"+num+"/requested_reviewers", map[string]any{"reviewers": req.Logins}, nil)
}

// SetAssignees implements gp.AssigneeSetter: the assignees become exactly logins.
func (p *ghProvider) SetAssignees(_ context.Context, req gp.PeopleRequest) (gp.Empty, error) {
	num, err := number(req.Subject.Number)
	if err != nil {
		return gp.Empty{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Empty{}, err
	}
	logins := req.Logins
	if logins == nil {
		logins = []string{}
	}
	return gp.Empty{}, api.send("PATCH", repo+"/issues/"+num, map[string]any{"assignees": logins}, nil)
}

// MergeChangeRequest implements gp.Merger. GitHub itself refuses (409) when the
// head moved since ExpectedHeadSHA, which is the guarantee core relies on.
func (p *ghProvider) MergeChangeRequest(_ context.Context, req gp.MergeRequest) (gp.MergeResult, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.MergeResult{}, err
	}
	if blank(req.ExpectedHeadSHA) {
		return gp.MergeResult{}, gp.Errorf(gp.CodeValidation, "expected_head_sha is required")
	}
	method := req.Method
	if method == "" {
		method = "merge"
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return gp.MergeResult{}, gp.Errorf(gp.CodeValidation, "method must be merge, squash or rebase")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.MergeResult{}, err
	}
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := api.send("PUT", repo+"/pulls/"+num+"/merge", map[string]any{"sha": req.ExpectedHeadSHA, "merge_method": method}, &out); err != nil {
		return gp.MergeResult{}, err
	}
	if !out.Merged {
		return gp.MergeResult{}, gp.Errorf(gp.CodeConflict, "GitHub did not merge the pull request")
	}
	return gp.MergeResult{SHA: out.SHA}, nil
}
