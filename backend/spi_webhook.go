package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// VerifyAndParseWebhook implements gp.Provider. It is the only place GitHub's
// webhook format lives: it checks X-Hub-Signature-256 against the App's
// webhook secret and turns the delivery into zero or more normalized events.
// Event types Paca has no use for are dropped, not guessed at.
func (p *ghProvider) VerifyAndParseWebhook(_ context.Context, req gp.WebhookRequest) ([]gp.Event, error) {
	app, err := p.loadApp(req.Identity.ID)
	if err != nil {
		return nil, err
	}
	sig := req.Headers["x-hub-signature-256"]
	if app.WebhookSecret == "" || !verifyHMAC(req.Body, app.WebhookSecret, sig) {
		return nil, gp.Errorf(gp.CodeInvalidSignature, "webhook signature does not match")
	}
	name := req.Headers["x-github-event"]
	var d ghDelivery
	if err := json.Unmarshal(req.Body, &d); err != nil {
		return nil, gp.Errorf(gp.CodeValidation, "webhook payload is not valid JSON")
	}
	id := req.Headers["x-github-delivery"]
	if id == "" {
		sum := sha256.Sum256(req.Body)
		id = hex.EncodeToString(sum[:8])
	}
	ev, ok := d.normalize(name, app)
	if !ok {
		return nil, nil
	}
	ev.ID = id + ":" + name
	ev.RawRef = id
	return []gp.Event{ev}, nil
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghDelivery struct {
	Action     string `json:"action"`
	Sender     ghUser `json:"sender"`
	Repository struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`

	Issue *struct {
		Number      int             `json:"number"`
		Title       string          `json:"title"`
		Body        string          `json:"body"`
		User        ghUser          `json:"user"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment *struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		Path string `json:"path"`
		Line int    `json:"line"`
	} `json:"comment"`
	Review *struct {
		ID    int64  `json:"id"`
		Body  string `json:"body"`
		State string `json:"state"`
	} `json:"review"`
	PullRequest *ghPRPayload `json:"pull_request"`
	CheckRun    *struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		HeadSHA     string  `json:"head_sha"`
		Conclusion  *string `json:"conclusion"`
		PullRequest []struct {
			Number int `json:"number"`
			Head   struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_requests"`
	} `json:"check_run"`

	// push
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Created bool   `json:"created"`
	Deleted bool   `json:"deleted"`
}

type ghPRPayload struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	User   ghUser `json:"user"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func actorOf(u ghUser) gp.Actor {
	k := gp.ActorUser
	if strings.EqualFold(u.Type, "Bot") {
		k = gp.ActorBot
	}
	return gp.Actor{Login: u.Login, Kind: k}
}

func (d *ghDelivery) repoRef() gp.RepoRef {
	return gp.RepoRef{
		Host: githubHost, FullPath: d.Repository.FullName, ExternalID: strconv.FormatInt(d.Repository.ID, 10),
		DefaultBranch: d.Repository.DefaultBranch, CloneURL: githubCloneBase + d.Repository.FullName + ".git",
	}
}

func prSubject(pr *ghPRPayload, baseRepo string) *gp.EventSubject {
	head := &gp.Branch{Ref: pr.Head.Ref, SHA: pr.Head.SHA}
	if pr.Head.Repo != nil {
		head.Repo = pr.Head.Repo.FullName
		head.FromFork = !strings.EqualFold(pr.Head.Repo.FullName, baseRepo)
	} else {
		head.FromFork = true // the head repository was deleted: it can only have been a fork
	}
	return &gp.EventSubject{Kind: gp.SubjectChangeRequest, Number: strconv.Itoa(pr.Number), Head: head, Base: pr.Base.Ref, Draft: pr.Draft, Author: pr.User.Login}
}

func conclusionOf(c string) string {
	switch c {
	case "success", "neutral":
		return "success"
	case "skipped":
		return "skipped"
	case "cancelled":
		return "cancelled"
	}
	return "failure" // failure, timed_out, action_required, startup_failure, stale
}

// normalize maps one GitHub event onto the SPI. ok is false for events Paca
// does not use.
func (d *ghDelivery) normalize(name string, app appRecord) (gp.Event, bool) {
	if d.Repository.FullName == "" {
		return gp.Event{}, false // ping and other non-repository deliveries
	}
	ev := gp.Event{Repo: d.repoRef(), Actor: actorOf(d.Sender)}
	if ev.Actor.Login == "" {
		return gp.Event{}, false
	}
	address := func(text string) {
		if mentionsHandle(text, app.Slug) {
			ev.Addressed = append(ev.Addressed, gp.Addressed{IdentityID: app.ID, Via: gp.ViaMention})
		}
	}

	switch name {
	case "issue_comment":
		if d.Issue == nil || d.Comment == nil || (d.Action != "created" && d.Action != "edited") {
			return gp.Event{}, false
		}
		kind := gp.SubjectIssue
		if len(d.Issue.PullRequest) > 0 && string(d.Issue.PullRequest) != "null" {
			kind = gp.SubjectChangeRequest
		}
		ev.Kind, ev.Action, ev.Text = gp.EventComment, d.Action, d.Comment.Body
		ev.Subject = &gp.EventSubject{Kind: kind, Number: strconv.Itoa(d.Issue.Number), Author: d.Issue.User.Login}
		ev.Location = &gp.Location{CommentID: strconv.FormatInt(d.Comment.ID, 10)}
		if d.Action == "created" {
			address(d.Comment.Body)
		}

	case "pull_request_review_comment":
		if d.PullRequest == nil || d.Comment == nil || (d.Action != "created" && d.Action != "edited") {
			return gp.Event{}, false
		}
		ev.Kind, ev.Action, ev.Text = gp.EventComment, d.Action, d.Comment.Body
		ev.Subject = prSubject(d.PullRequest, d.Repository.FullName)
		ev.Location = &gp.Location{Path: d.Comment.Path, Line: d.Comment.Line, CommentID: strconv.FormatInt(d.Comment.ID, 10)}
		if d.Action == "created" {
			address(d.Comment.Body)
		}

	case "pull_request_review":
		if d.PullRequest == nil || d.Review == nil || d.Action != "submitted" {
			return gp.Event{}, false
		}
		ev.Kind, ev.Action, ev.Text = gp.EventReview, "submitted", d.Review.Body
		ev.Subject = prSubject(d.PullRequest, d.Repository.FullName)
		ev.Location = &gp.Location{CommentID: strconv.FormatInt(d.Review.ID, 10)}
		address(d.Review.Body)

	case "pull_request":
		if d.PullRequest == nil {
			return gp.Event{}, false
		}
		ev.Subject = prSubject(d.PullRequest, d.Repository.FullName)
		switch d.Action {
		case "opened", "edited", "reopened", "synchronize", "ready_for_review", "converted_to_draft":
			ev.Kind, ev.Text = gp.EventChangeRequest, d.PullRequest.Title
			ev.Action = map[string]string{"synchronize": "synchronized", "ready_for_review": "ready", "converted_to_draft": "draft"}[d.Action]
			if ev.Action == "" {
				ev.Action = d.Action
			}
			if d.Action == "opened" {
				address(d.PullRequest.Title + "\n" + d.PullRequest.Body)
			}
		case "closed":
			ev.Kind, ev.Text, ev.Action = gp.EventChangeRequest, d.PullRequest.Title, "closed"
			if d.PullRequest.Merged {
				ev.Action = "merged"
			}
		case "labeled":
			if d.Label == nil {
				return gp.Event{}, false
			}
			ev.Kind, ev.Action, ev.Label = gp.EventLabel, "labeled", d.Label.Name
		default:
			return gp.Event{}, false
		}

	case "issues":
		if d.Issue == nil {
			return gp.Event{}, false
		}
		ev.Subject = &gp.EventSubject{Kind: gp.SubjectIssue, Number: strconv.Itoa(d.Issue.Number), Author: d.Issue.User.Login}
		switch d.Action {
		case "opened":
			// The issue body is the first message of the thread; treat it as a comment.
			ev.Kind, ev.Action = gp.EventComment, "created"
			ev.Text = strings.TrimSpace(d.Issue.Title + "\n\n" + d.Issue.Body)
			address(d.Issue.Title + "\n" + d.Issue.Body)
		case "labeled":
			if d.Label == nil {
				return gp.Event{}, false
			}
			ev.Kind, ev.Action, ev.Label = gp.EventLabel, "labeled", d.Label.Name
		default:
			return gp.Event{}, false
		}

	case "check_run":
		if d.CheckRun == nil || d.Action != "completed" || d.CheckRun.Conclusion == nil {
			return gp.Event{}, false
		}
		ev.Kind, ev.Action = gp.EventCheck, "completed"
		ev.Check = &gp.CheckInfo{
			Name: d.CheckRun.Name, Conclusion: conclusionOf(*d.CheckRun.Conclusion),
			RunID: strconv.FormatInt(d.CheckRun.ID, 10), SHA: d.CheckRun.HeadSHA,
		}
		if len(d.CheckRun.PullRequest) > 0 {
			pr := d.CheckRun.PullRequest[0]
			ev.Subject = &gp.EventSubject{Kind: gp.SubjectChangeRequest, Number: strconv.Itoa(pr.Number), Base: pr.Base.Ref,
				Head: &gp.Branch{Ref: pr.Head.Ref, SHA: pr.Head.SHA}}
		}

	case "push":
		ref, isBranch := strings.CutPrefix(d.Ref, "refs/heads/")
		if !isBranch || ref == "" {
			return gp.Event{}, false // tags and other refs are not branch activity
		}
		ev.Kind, ev.Action = gp.EventPush, "pushed"
		if d.Created {
			ev.Action = "created"
		} else if d.Deleted {
			ev.Action = "deleted"
		}
		ev.Push = &gp.PushInfo{Ref: ref, SHA: d.After, Created: d.Created, Deleted: d.Deleted}

	default:
		return gp.Event{}, false
	}
	return ev, true
}

// mentionsHandle reports whether text @-mentions handle (also as handle[bot]).
// An e-mail address or a longer handle sharing the prefix is not a mention.
func mentionsHandle(text, handle string) bool {
	if handle == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)(?:^|[^\w@])@` + regexp.QuoteMeta(handle) + `(?:\[bot\])?(?:$|[^\w-])`)
	return re.MatchString(text)
}
