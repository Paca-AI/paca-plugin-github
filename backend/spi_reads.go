package main

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

const (
	listPerPage     = 100
	maxCheckPages   = 5
	defaultLogBytes = 256 * 1024
	diffAccept      = "application/vnd.github.v3.diff"
)

type ghPR struct {
	NodeID    string `json:"node_id"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Merged    bool   `json:"merged"`
	HTMLURL   string `json:"html_url"`
	User      ghUser `json:"user"`
	UpdatedAt time.Time
	Head      struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
}

func (pr ghPR) toCR() gp.ChangeRequest {
	cr := gp.ChangeRequest{
		Number: strconv.Itoa(pr.Number), Title: pr.Title, Body: pr.Body, Draft: pr.Draft,
		Author: pr.User.Login, URL: pr.HTMLURL, UpdatedAt: pr.UpdatedAt,
		Head: gp.Branch{Ref: pr.Head.Ref, SHA: pr.Head.SHA},
		Base: gp.Branch{Ref: pr.Base.Ref, SHA: pr.Base.SHA, Repo: pr.Base.Repo.FullName},
	}
	if pr.Head.Repo != nil {
		cr.Head.Repo = pr.Head.Repo.FullName
		cr.Head.FromFork = !strings.EqualFold(pr.Head.Repo.FullName, pr.Base.Repo.FullName)
	} else {
		cr.Head.FromFork = true
	}
	switch {
	case pr.Merged:
		cr.State = gp.CRMerged
	case pr.State == "closed":
		cr.State = gp.CRClosed
	default:
		cr.State = gp.CROpen
	}
	for _, l := range pr.Labels {
		cr.Labels = append(cr.Labels, l.Name)
	}
	for _, r := range pr.RequestedReviewers {
		cr.Reviewers = append(cr.Reviewers, r.Login)
	}
	return cr
}

// number validates a change-request or issue number before it goes into a URL.
func number(n string) (string, error) {
	if _, err := strconv.ParseUint(n, 10, 32); err != nil {
		return "", gp.Errorf(gp.CodeValidation, "%q is not a valid number", n)
	}
	return n, nil
}

func page(cursor string) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 1 {
		return 0, gp.Errorf(gp.CodeValidation, "invalid cursor")
	}
	return n, nil
}

func nextPage(got, cur int) string {
	if got >= listPerPage {
		return strconv.Itoa(cur + 1)
	}
	return ""
}

// cutHead keeps at most max bytes from the start, never splitting a rune.
func cutHead(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max], true
}

// cutTail keeps at most max bytes from the end, never splitting a rune.
func cutTail(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	i := len(s) - max
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:], true
}

func (p *ghProvider) getPR(api *ghAPI, repo, num string) (ghPR, error) {
	var pr ghPR
	err := api.get(repo+"/pulls/"+num, &pr)
	return pr, err
}

// GetChangeRequest implements gp.Provider.
func (p *ghProvider) GetChangeRequest(_ context.Context, req gp.CRRequest) (gp.ChangeRequest, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	pr, err := p.getPR(api, repo, num)
	if err != nil {
		return gp.ChangeRequest{}, err
	}
	return pr.toCR(), nil
}

// ListChangedFiles implements gp.Provider.
func (p *ghProvider) ListChangedFiles(_ context.Context, req gp.PagedCRRequest) (gp.ChangedFilePage, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.ChangedFilePage{}, err
	}
	pg, err := page(req.Cursor)
	if err != nil {
		return gp.ChangedFilePage{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ChangedFilePage{}, err
	}
	var files []struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename"`
		Status           string `json:"status"`
		Additions        int    `json:"additions"`
		Deletions        int    `json:"deletions"`
	}
	if err := api.get(repo+"/pulls/"+num+"/files?per_page="+strconv.Itoa(listPerPage)+"&page="+strconv.Itoa(pg), &files); err != nil {
		return gp.ChangedFilePage{}, err
	}
	out := gp.ChangedFilePage{Items: []gp.ChangedFile{}, NextCursor: nextPage(len(files), pg)}
	for _, f := range files {
		st := f.Status
		switch st {
		case "copied":
			st = "added"
		case "changed":
			st = "modified"
		}
		out.Items = append(out.Items, gp.ChangedFile{Path: f.Filename, OldPath: f.PreviousFilename, Status: st, Additions: f.Additions, Deletions: f.Deletions})
	}
	return out, nil
}

// GetDiff implements gp.Provider. A per-file diff is cut out of the whole
// diff; a diff longer than MaxBytes is cut and flagged, never silently.
func (p *ghProvider) GetDiff(_ context.Context, req gp.DiffRequest) (gp.Diff, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.Diff{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Diff{}, err
	}
	text, err := api.do("GET", repo+"/pulls/"+num, nil, diffAccept)
	if err != nil {
		return gp.Diff{}, err
	}
	if req.Path != "" {
		sec, ok := diffSection(text, req.Path)
		if !ok {
			return gp.Diff{}, gp.Errorf(gp.CodeNotFound, "%s is not part of this change", req.Path)
		}
		text = sec
	}
	cut, truncated := cutHead(text, req.MaxBytes)
	return gp.Diff{Text: cut, Truncated: truncated}, nil
}

// diffSection returns the part of a unified git diff that belongs to path.
func diffSection(diff, path string) (string, bool) {
	const marker = "diff --git "
	var sections []string
	for i := 0; i < len(diff); {
		next := strings.Index(diff[i+1:], "\n"+marker)
		if next < 0 {
			sections = append(sections, diff[i:])
			break
		}
		sections = append(sections, diff[i:i+1+next+1])
		i += next + 2
	}
	for _, s := range sections {
		head, _, _ := strings.Cut(s, "\n")
		if strings.HasSuffix(head, " b/"+path) || strings.Contains(head, " a/"+path+" ") {
			return s, true
		}
	}
	return "", false
}

const threadsQuery = `query($owner:String!,$name:String!,$number:Int!,$after:String){
 repository(owner:$owner,name:$name){ pullRequest(number:$number){
  reviewThreads(first:50, after:$after){ pageInfo{hasNextPage endCursor}
   nodes{ id isResolved isOutdated path line
    comments(first:50){ nodes{ databaseId body url createdAt author{ login __typename } } } } } } } }`

type ghThreads struct {
	Repository *struct {
		PullRequest *struct {
			ReviewThreads struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID         string `json:"id"`
					IsResolved bool   `json:"isResolved"`
					IsOutdated bool   `json:"isOutdated"`
					Path       string `json:"path"`
					Line       int    `json:"line"`
					Comments   struct {
						Nodes []struct {
							DatabaseID int64     `json:"databaseId"`
							Body       string    `json:"body"`
							URL        string    `json:"url"`
							CreatedAt  time.Time `json:"createdAt"`
							Author     *struct {
								Login    string `json:"login"`
								Typename string `json:"__typename"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// ListThreads implements gp.Provider (GraphQL: REST has no thread ids or
// resolved state). The cursor is GraphQL's own end cursor.
func (p *ghProvider) ListThreads(_ context.Context, req gp.PagedCRRequest) (gp.ThreadPage, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.ThreadPage{}, err
	}
	api, _, err := p.apiFor(req.Target)
	if err != nil {
		return gp.ThreadPage{}, err
	}
	owner, name, _ := splitFullPath(req.Target.Repo.FullPath)
	n, _ := strconv.Atoi(num)
	vars := map[string]any{"owner": owner, "name": name, "number": n}
	if req.Cursor != "" {
		vars["after"] = req.Cursor
	}
	var data ghThreads
	if err := api.graphql(threadsQuery, vars, &data); err != nil {
		return gp.ThreadPage{}, err
	}
	if data.Repository == nil || data.Repository.PullRequest == nil {
		return gp.ThreadPage{}, gp.Errorf(gp.CodeNotFound, "change request not found")
	}
	rt := data.Repository.PullRequest.ReviewThreads
	out := gp.ThreadPage{Items: []gp.Thread{}}
	for _, t := range rt.Nodes {
		th := gp.Thread{ID: t.ID, Path: t.Path, Line: t.Line, Resolved: t.IsResolved, Outdated: t.IsOutdated}
		for _, c := range t.Comments.Nodes {
			login, kind := "ghost", gp.ActorUser
			if c.Author != nil {
				login = c.Author.Login
				if c.Author.Typename == "Bot" {
					kind = gp.ActorBot
				}
			}
			th.Comments = append(th.Comments, gp.Comment{ID: strconv.FormatInt(c.DatabaseID, 10), Author: login, AuthorKind: kind, Body: c.Body, URL: c.URL, CreatedAt: c.CreatedAt})
		}
		if len(th.Comments) > 0 {
			out.Items = append(out.Items, th)
		}
	}
	if rt.PageInfo.HasNextPage {
		out.NextCursor = rt.PageInfo.EndCursor
	}
	return out, nil
}

type ghComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	User      ghUser    `json:"user"`
}

// ListComments implements gp.Provider: the conversation comments of an issue
// or change request (review-thread comments come from ListThreads).
func (p *ghProvider) ListComments(_ context.Context, req gp.ListCommentsRequest) (gp.CommentPage, error) {
	num, err := number(req.Subject.Number)
	if err != nil {
		return gp.CommentPage{}, err
	}
	pg, err := page(req.Cursor)
	if err != nil {
		return gp.CommentPage{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.CommentPage{}, err
	}
	var cs []ghComment
	if err := api.get(repo+"/issues/"+num+"/comments?per_page="+strconv.Itoa(listPerPage)+"&page="+strconv.Itoa(pg), &cs); err != nil {
		return gp.CommentPage{}, err
	}
	out := gp.CommentPage{Items: []gp.Comment{}, NextCursor: nextPage(len(cs), pg)}
	for _, c := range cs {
		out.Items = append(out.Items, gp.Comment{ID: strconv.FormatInt(c.ID, 10), Author: c.User.Login, AuthorKind: actorOf(c.User).Kind, Body: c.Body, URL: c.HTMLURL, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

// GetIssue implements gp.Provider.
func (p *ghProvider) GetIssue(_ context.Context, req gp.IssueRequest) (gp.Issue, error) {
	num, err := number(req.Number)
	if err != nil {
		return gp.Issue{}, err
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Issue{}, err
	}
	var is struct {
		Number    int                     `json:"number"`
		Title     string                  `json:"title"`
		Body      string                  `json:"body"`
		State     string                  `json:"state"`
		HTMLURL   string                  `json:"html_url"`
		User      ghUser                  `json:"user"`
		Labels    []struct{ Name string } `json:"labels"`
		Assignees []ghUser                `json:"assignees"`
	}
	if err := api.get(repo+"/issues/"+num, &is); err != nil {
		return gp.Issue{}, err
	}
	out := gp.Issue{Number: strconv.Itoa(is.Number), Title: is.Title, Body: is.Body, State: is.State, Author: is.User.Login, URL: is.HTMLURL}
	for _, l := range is.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	for _, a := range is.Assignees {
		out.Assignees = append(out.Assignees, a.Login)
	}
	return out, nil
}

func checkStateOf(status string, conclusion *string) gp.CheckState {
	if status != "completed" || conclusion == nil {
		return gp.CheckPending
	}
	switch *conclusion {
	case "success", "neutral":
		return gp.CheckSuccess
	case "skipped":
		return gp.CheckSkipped
	case "cancelled":
		return gp.CheckCancelled
	}
	return gp.CheckFailure
}

// GetChecks implements gp.Provider: check runs (Actions and most Apps) plus
// legacy commit statuses. Ids are "run:<id>" and "status:<context>".
func (p *ghProvider) GetChecks(_ context.Context, req gp.ChecksRequest) ([]gp.Check, error) {
	if strings.TrimSpace(req.Ref) == "" {
		return nil, gp.Errorf(gp.CodeValidation, "ref is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return nil, err
	}
	ref := url.PathEscape(req.Ref)
	out := []gp.Check{}
	for pg := 1; pg <= maxCheckPages; pg++ {
		var runs struct {
			CheckRuns []struct {
				ID         int64   `json:"id"`
				Name       string  `json:"name"`
				Status     string  `json:"status"`
				Conclusion *string `json:"conclusion"`
				HTMLURL    string  `json:"html_url"`
				Output     struct {
					Title   string `json:"title"`
					Summary string `json:"summary"`
				} `json:"output"`
			} `json:"check_runs"`
		}
		if err := api.get(repo+"/commits/"+ref+"/check-runs?per_page="+strconv.Itoa(listPerPage)+"&page="+strconv.Itoa(pg), &runs); err != nil {
			return nil, err
		}
		for _, r := range runs.CheckRuns {
			sum := r.Output.Title
			if sum == "" {
				sum = clip(r.Output.Summary)
			}
			out = append(out, gp.Check{ID: "run:" + strconv.FormatInt(r.ID, 10), Name: r.Name, State: checkStateOf(r.Status, r.Conclusion), URL: r.HTMLURL, Summary: sum})
		}
		if len(runs.CheckRuns) < listPerPage {
			break
		}
	}
	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := api.get(repo+"/commits/"+ref+"/status", &combined); err == nil {
		for _, s := range combined.Statuses {
			st := gp.CheckPending
			switch s.State {
			case "success":
				st = gp.CheckSuccess
			case "failure", "error":
				st = gp.CheckFailure
			}
			out = append(out, gp.Check{ID: "status:" + s.Context, Name: s.Context, State: st, URL: s.TargetURL, Summary: s.Description})
		}
	} else if gp.AsError(err).Code != gp.CodeNotFound {
		return nil, err
	}
	return out, nil
}

// GetCheckLogs implements gp.Provider. For a GitHub Actions job the log is the
// job log; any other check run falls back to the output its app reported.
func (p *ghProvider) GetCheckLogs(_ context.Context, req gp.CheckLogsRequest) (gp.Logs, error) {
	id, ok := strings.CutPrefix(req.CheckID, "run:")
	if !ok {
		return gp.Logs{}, gp.Errorf(gp.CodeNotFound, "no logs for check %q", clip(req.CheckID))
	}
	if _, err := strconv.ParseUint(id, 10, 63); err != nil {
		return gp.Logs{}, gp.Errorf(gp.CodeNotFound, "no such check")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.Logs{}, err
	}
	text, err := api.do("GET", repo+"/actions/jobs/"+id+"/logs", nil, "")
	if err != nil || strings.TrimSpace(text) == "" {
		var run struct {
			Output struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
				Text    string `json:"text"`
			} `json:"output"`
		}
		if rerr := api.get(repo+"/check-runs/"+id, &run); rerr != nil {
			return gp.Logs{}, rerr
		}
		text = strings.TrimSpace(strings.Join(nonEmpty(run.Output.Title, run.Output.Summary, run.Output.Text), "\n\n"))
		if text == "" {
			return gp.Logs{}, gp.Errorf(gp.CodeNotFound, "this check reported no log output")
		}
	}
	limit := req.TailBytes
	if limit <= 0 {
		limit = defaultLogBytes
	}
	cut, truncated := cutTail(text, limit)
	return gp.Logs{Text: cut, Truncated: truncated}, nil
}

func nonEmpty(in ...string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// GetFile implements gp.FileReader.
func (p *ghProvider) GetFile(_ context.Context, req gp.FileRequest) (gp.File, error) {
	path := strings.Trim(req.Path, "/")
	if path == "" || strings.Contains(path, "..") {
		return gp.File{}, gp.Errorf(gp.CodeValidation, "a file path is required")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.File{}, err
	}
	segs := strings.Split(path, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	u := repo + "/contents/" + strings.Join(segs, "/")
	if req.Ref != "" {
		u += "?ref=" + url.QueryEscape(req.Ref)
	}
	text, err := api.do("GET", u, nil, "")
	if err != nil {
		return gp.File{}, err
	}
	if strings.HasPrefix(strings.TrimSpace(text), "[") {
		return gp.File{}, gp.Errorf(gp.CodeValidation, "%s is a directory", path)
	}
	var f struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := decodeInto(text, &f); err != nil {
		return gp.File{}, err
	}
	var content []byte
	if f.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(f.Content))
		if err != nil {
			return gp.File{}, gp.Errorf(gp.CodeInternal, "GitHub returned an unreadable file")
		}
		content = b
	} else { // large files come back without inline content: fetch the raw bytes
		raw, err := api.do("GET", u, nil, "application/vnd.github.raw+json")
		if err != nil {
			return gp.File{}, err
		}
		content = []byte(raw)
	}
	out := gp.File{Path: path, SHA: f.SHA, Content: content}
	if req.MaxBytes > 0 && len(content) > req.MaxBytes {
		out.Content, out.Truncated = content[:req.MaxBytes], true
	}
	return out, nil
}
