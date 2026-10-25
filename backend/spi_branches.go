package main

import (
	"context"
	"net/url"
	"strings"

	gp "github.com/Paca-AI/plugin-sdk-go/gitprovider"
)

// branchPath escapes a branch name segment by segment: "feat/x" keeps its
// slash, anything else that could change the path is escaped.
func branchPath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// validBranchName applies git's core rules for a branch name, so a bad name is
// refused here as validation rather than as a GitHub 422.
func validBranchName(name string) bool {
	if name == "" || len(name) > 250 || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasPrefix(name, "-") || strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	return true
}

type ghBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func branchInfo(t gp.Target, b ghBranch) gp.BranchInfo {
	return gp.BranchInfo{Name: b.Name, SHA: b.Commit.SHA,
		URL: "https://" + t.Repo.Host + "/" + strings.Trim(t.Repo.FullPath, "/") + "/tree/" + branchPath(b.Name)}
}

// GetBranch implements gp.BranchManager.
func (p *ghProvider) GetBranch(_ context.Context, req gp.GetBranchRequest) (gp.BranchInfo, error) {
	if !validBranchName(req.Name) {
		return gp.BranchInfo{}, gp.Errorf(gp.CodeValidation, "invalid branch name")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.BranchInfo{}, err
	}
	var b ghBranch
	if err := api.get(repo+"/branches/"+branchPath(req.Name), &b); err != nil {
		return gp.BranchInfo{}, err
	}
	return branchInfo(req.Target, b), nil
}

// CreateBranch implements gp.BranchManager: a new ref at the head of From (the
// default branch when empty). GitHub answers 422 "Reference already exists"
// for a taken name, which is classified as a conflict.
func (p *ghProvider) CreateBranch(_ context.Context, req gp.CreateBranchRequest) (gp.BranchInfo, error) {
	if !validBranchName(req.Name) {
		return gp.BranchInfo{}, gp.Errorf(gp.CodeValidation, "invalid branch name")
	}
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = req.Target.Repo.DefaultBranch
	}
	if from == "" {
		return gp.BranchInfo{}, gp.Errorf(gp.CodeValidation, "the source branch is required: the repository has no known default branch")
	}
	if !validBranchName(from) {
		return gp.BranchInfo{}, gp.Errorf(gp.CodeValidation, "invalid source branch name")
	}
	api, repo, err := p.apiFor(req.Target)
	if err != nil {
		return gp.BranchInfo{}, err
	}
	var src ghBranch
	if err := api.get(repo+"/branches/"+branchPath(from), &src); err != nil {
		return gp.BranchInfo{}, err
	}
	if err := api.send("POST", repo+"/git/refs", map[string]any{"ref": "refs/heads/" + req.Name, "sha": src.Commit.SHA}, nil); err != nil {
		return gp.BranchInfo{}, err
	}
	return branchInfo(req.Target, ghBranch{Name: req.Name, Commit: src.Commit}), nil
}
