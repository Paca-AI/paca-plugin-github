---
name: paca-github-workflow
description: Link a GitHub repository to a Paca project, create branches, open and manage pull requests, review PRs, and check CI status — using the com.paca.github plugin's tools. Use when asked to open a PR, create a branch, link a pull request to a task, review or comment on a PR, or check CI/build status for a task.
triggers:
  - /paca-github-workflow
  - create a pull request
  - open a pr
  - create branch
  - link pull request
  - review this pr
  - check ci status
---

# GitHub Workflow Skill

This plugin (`com.paca.github`) connects a Paca project to GitHub via a Personal Access Token (PAT) — not a GitHub App — and links repositories, branches, and pull requests to individual tasks. All GitHub API calls happen server-side; you only ever call this plugin's tools.

## Prerequisites — check before doing anything else

1. `github_get_integration({projectId})`. If `connected` is false, a GitHub PAT must be connected first (scopes `repo` + `admin:repo_hook`) — you cannot mint one yourself. Tell the user to add it under Project Settings → GitHub, or, if they give you a token directly in the conversation, call `github_set_token({projectId, token})` yourself.
2. `github_list_linked_repos({projectId})`. If empty and you already know the exact `owner`/`repo_name`, call `github_link_repository({projectId, owner, repo_name})` — this also creates the required webhook automatically. If you don't know the exact owner/repo name, ask the user; there's no tool to browse "which repos can this token see" (that picker is UI-only).

Every branch/PR tool below 404s (`GITHUB_INTEGRATION_NOT_FOUND` / `GITHUB_REPOSITORY_NOT_FOUND`) until both of these are satisfied.

## Tools

**Integration**
- `github_get_integration(projectId)` — connection status.
- `github_set_token(projectId, token)` — only when the user hands you a token directly.
- `github_delete_token(projectId)` — disconnects and removes webhooks.

**Repositories**
- `github_list_linked_repos(projectId)`
- `github_link_repository(projectId, owner, repo_name)`
- `github_unlink_repository(projectId, repoId)`

**Branches**
- `github_create_branch(projectId, taskId, repoId, branch_name, source_branch?)` — `source_branch` defaults to the repo's default branch if omitted.
- `github_list_task_branches(projectId, taskId)`

**Pull requests**
- `github_list_task_prs(projectId, taskId)`
- `github_create_pull_request(projectId, taskId, repoId, title, head_branch, base_branch, body?)` — creates the PR on GitHub **and** links it to the task in the same call.
- `github_link_pr_to_task(projectId, taskId, repoId, pr_number)` — only for a PR that already existed before you touched it (opened by a human, or via `gh pr create` directly).
- `github_unlink_pr_from_task(projectId, taskId, prId)`
- `github_get_pull_request(projectId, taskId, prId)` — title, state, body, and the full diff.
- `github_get_pull_request_ci_status(projectId, taskId, prId)` — combined GitHub Actions / check-run / legacy status state.
- `github_comment_pull_request(projectId, taskId, prId, body)` — a plain issue-style comment.
- `github_review_pull_request(projectId, taskId, prId, event, body?)` — `event` is `APPROVE`, `REQUEST_CHANGES`, or `COMMENT`; `body` is required by GitHub for the latter two.

`repoId` and `prId` are Paca's own internal ids — get `repoId` from `github_list_linked_repos` and `prId` from `github_list_task_prs`. Neither is a GitHub PR number or `owner/repo` string; don't substitute one for the other.

## Workflow — finishing a task with a PR

1. Confirm the prerequisites above.
2. Name the branch `<type>/<PREFIX>-<number>[-slug]` — e.g. `feat/PROJ-42-add-auth` — matching the task's own reference. This is load-bearing, not cosmetic: both the manual UI and the webhook auto-linker match on exactly this pattern to associate branches and PRs with a task without an explicit link call.
3. `github_create_branch`, or push a branch with a matching name yourself.
4. Commit and push your changes (plain git — outside this plugin's scope).
5. `github_create_pull_request`. This both opens the PR and links it to the task — don't also call `github_link_pr_to_task` afterward; that's only for PRs that already existed independently of you.
6. Before merging or requesting review, call `github_get_pull_request_ci_status`. Treat `pending` **and** `unknown` as not yet safe to merge — `unknown` means no checks have reported at all, not that everything passed.

## Reviewing a PR

1. `github_get_pull_request` first, to read the diff — never review blind.
2. `github_review_pull_request` with `event: APPROVE | REQUEST_CHANGES | COMMENT`.
3. For a lighter note that isn't a formal review verdict, use `github_comment_pull_request` instead.

## Constraints

- `github_comment_pull_request` and `github_review_pull_request` post directly to GitHub — they do **not** add anything to the Paca task's own activity or comments. If the user also wants a note left on the Paca task, add that separately with the core `add_task_comment` tool.
- You cannot mint a GitHub PAT and cannot enumerate which repos a token can see — if you don't already know the exact `owner`/`repo_name`, ask rather than guess.
