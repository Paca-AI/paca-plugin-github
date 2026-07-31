package main

import "testing"

func TestGhBranchHeadRefURL_EncodesSlashes(t *testing.T) {
	got := ghBranchHeadRefURL("Paca-AI", "paca-plugin-github", "fix/upload-pipeline-t8-investigation")
	want := "https://api.github.com/repos/Paca-AI/paca-plugin-github/git/ref/heads/fix%2Fupload-pipeline-t8-investigation"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGhBranchHeadRefURL_PlainBranch(t *testing.T) {
	got := ghBranchHeadRefURL("owner", "repo", "main")
	want := "https://api.github.com/repos/owner/repo/git/ref/heads/main"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
