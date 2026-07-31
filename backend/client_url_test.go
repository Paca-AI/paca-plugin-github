package main

import "testing"

func TestGhBranchHeadRefURL_EncodesSlashes(t *testing.T) {
	got := ghBranchHeadRefURL("torvalds", "linux", "fix/sched-fair")
	want := "https://api.github.com/repos/torvalds/linux/git/ref/heads/fix%2Fsched-fair"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGhBranchHeadRefURL_PlainBranch(t *testing.T) {
	got := ghBranchHeadRefURL("golang", "go", "master")
	want := "https://api.github.com/repos/golang/go/git/ref/heads/master"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGhBranchHeadRefURL_TableDriven(t *testing.T) {
	tests := []struct {
		name   string
		owner  string
		repo   string
		branch string
		want   string
	}{
		{
			name:   "release branch",
			owner:  "nodejs",
			repo:   "node",
			branch: "v20.x",
			want:   "https://api.github.com/repos/nodejs/node/git/ref/heads/v20.x",
		},
		{
			name:   "nested feature branch",
			owner:  "rust-lang",
			repo:   "rust",
			branch: "feature/const-traits",
			want:   "https://api.github.com/repos/rust-lang/rust/git/ref/heads/feature%2Fconst-traits",
		},
		{
			name:   "dependabot style branch",
			owner:  "django",
			repo:   "django",
			branch: "dependabot/pip/wheel-0.43.0",
			want:   "https://api.github.com/repos/django/django/git/ref/heads/dependabot%2Fpip%2Fwheel-0.43.0",
		},
		{
			name:   "owner repo with hyphen",
			owner:  "home-assistant",
			repo:   "core",
			branch: "dev",
			want:   "https://api.github.com/repos/home-assistant/core/git/ref/heads/dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ghBranchHeadRefURL(tt.owner, tt.repo, tt.branch)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
