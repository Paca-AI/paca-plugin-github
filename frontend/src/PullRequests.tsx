import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, GitMerge, GitPullRequest, GitPullRequestClosed, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import {
  ErrorCode,
  getPluginErrorCode,
  type LinkedRepository,
  linkPRToTask,
  listTaskPRs,
  type PullRequest,
  taskPRsKey,
  unlinkPRFromTask,
} from "./github-api";
import { Badge, Button, Field, Input, Pill, RowIconButton, Section, SectionEmpty, Select, Skeleton } from "./ui";

// ── State presentation ────────────────────────────────────────────────────────

const STATE: Record<PullRequest["state"], { label: string; tone: "success" | "merged" | "danger"; Icon: typeof GitPullRequest }> = {
  open: { label: "Open", tone: "success", Icon: GitPullRequest },
  merged: { label: "Merged", tone: "merged", Icon: GitMerge },
  closed: { label: "Closed", tone: "danger", Icon: GitPullRequestClosed },
};

function PRRow({ api, pr, repoName, projectId, taskId, canEdit }: { api: PluginApiClient; pr: PullRequest; repoName?: string; projectId: string; taskId: string; canEdit: boolean }) {
  const queryClient = useQueryClient();
  const unlink = useMutation({
    mutationFn: () => unlinkPRFromTask(api, taskId, pr.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: taskPRsKey(projectId, taskId) }),
  });
  const { label, tone, Icon } = STATE[pr.state] ?? STATE.open;

  return (
    <div className="ghp-row-item">
      <Icon className="ghp-row-icon" size={14} data-state={pr.state} aria-hidden="true" />
      <span className="ghp-row-ref">#{pr.pr_number}</span>
      <div className="ghp-row-body">
        <a className="ghp-row-title" href={pr.html_url} target="_blank" rel="noopener noreferrer" title={pr.title}>
          {pr.title}
        </a>
        <div className="ghp-row-meta">
          {repoName && <span className="ghp-truncate">{repoName}</span>}
          {pr.head_branch && (
            <span className="ghp-ref" title={`${pr.head_branch} into ${pr.base_branch}`}>
              {pr.head_branch}
              {pr.base_branch && (
                <>
                  <ArrowRight size={10} aria-hidden="true" />
                  {pr.base_branch}
                </>
              )}
            </span>
          )}
          {pr.author && <span className="ghp-truncate">by {pr.author}</span>}
        </div>
      </div>
      <Badge tone={tone}>{label}</Badge>
      {canEdit && (
        <div className="ghp-row-actions">
          <RowIconButton danger aria-label={`Unlink pull request #${pr.pr_number}`} title="Unlink" disabled={unlink.isPending} onClick={() => unlink.mutate()}>
            <Trash2 size={13} aria-hidden="true" />
          </RowIconButton>
        </div>
      )}
    </div>
  );
}

// ── URL parsing ───────────────────────────────────────────────────────────────

function parseGitHubPRUrl(raw: string): { fullName: string; prNumber: number } | null {
  try {
    const url = new URL(raw.trim());
    if (url.hostname !== "github.com") return null;
    const parts = url.pathname.replace(/^\//, "").split("/");
    if (parts.length < 4 || parts[2] !== "pull") return null;
    const prNumber = Number(parts[3]);
    if (!Number.isInteger(prNumber) || prNumber <= 0) return null;
    return { fullName: `${parts[0]}/${parts[1]}`, prNumber };
  } catch {
    return null;
  }
}

// ── Link form ─────────────────────────────────────────────────────────────────

function LinkPRForm({ api, projectId, taskId, repos, onDone }: { api: PluginApiClient; projectId: string; taskId: string; repos: LinkedRepository[]; onDone: () => void }) {
  const queryClient = useQueryClient();
  const [selectedRepoId, setSelectedRepoId] = useState(repos.length === 1 ? repos[0].id : "");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const parsed = parseGitHubPRUrl(value);
  const urlRepo = parsed ? (repos.find((r) => r.full_name === parsed.fullName) ?? null) : null;
  const repoId = parsed ? (urlRepo?.id ?? "") : selectedRepoId;
  const display = parsed ? parsed.prNumber : value.trim();

  const link = useMutation({
    mutationFn: () => linkPRToTask(api, taskId, repoId, parsed ? parsed.prNumber : Number(value)),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: taskPRsKey(projectId, taskId) });
      onDone();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubIntegrationNotFound
          ? "GitHub isn’t connected for this project."
          : code === ErrorCode.GitHubRepositoryNotFound
            ? "That repository isn’t linked any more."
            : code === ErrorCode.GitHubPRNotFound
              ? `PR #${display} wasn’t found in that repository.`
              : code === ErrorCode.GitHubPRAlreadyLinked
                ? `PR #${display} is already linked to this task.`
                : code === ErrorCode.GitHubTokenInsufficientPermissions
                  ? "The GitHub connection can’t read pull requests. Check it in Project Settings → GitHub."
                  : "Couldn’t link the pull request. Try again.",
      );
    },
  });

  function submit() {
    if (parsed) {
      if (!urlRepo) return setError(`“${parsed.fullName}” isn’t linked to this project.`);
    } else {
      const n = Number(value);
      if (!repoId) return setError("Select a repository.");
      if (!value.trim() || !Number.isInteger(n) || n <= 0) return setError("Enter a PR number or paste a GitHub PR link.");
    }
    setError(null);
    link.mutate();
  }

  return (
    <form
      className="ghp-form"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
    >
      <Field label="Pull request" hint={parsed && urlRepo ? `Will link #${parsed.prNumber} from ${urlRepo.full_name}` : "Paste a GitHub link, or enter a number."}>
        {(id) => (
          <Input
            id={id}
            value={value}
            placeholder="https://github.com/owner/repo/pull/42"
            disabled={link.isPending}
            aria-invalid={!!error}
            onChange={(e) => {
              setValue(e.target.value);
              setError(null);
            }}
            // biome-ignore lint/a11y/noAutofocus: opened on demand by the user
            autoFocus
          />
        )}
      </Field>
      {!parsed && repos.length > 1 && (
        <Field label="Repository">
          {(id) => (
            <Select id={id} value={selectedRepoId} disabled={link.isPending} onChange={(e) => { setSelectedRepoId(e.target.value); setError(null); }}>
              <option value="">Select a repository…</option>
              {repos.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.full_name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
      {error && (
        <p className="ghp-sec-error" role="alert">
          {error}
        </p>
      )}
      <div className="ghp-form-actions">
        <Button type="submit" variant="primary" size="sm" disabled={!value.trim()} loading={link.isPending}>
          Link pull request
        </Button>
        <Button variant="ghost" size="sm" onClick={onDone} disabled={link.isPending}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// ── Section ───────────────────────────────────────────────────────────────────

export function PullRequestsSection({ api, projectId, taskId, repos, canEdit }: { api: PluginApiClient; projectId: string; taskId: string; repos: LinkedRepository[]; canEdit: boolean }) {
  const { data: prs = [], isLoading } = useQuery<PullRequest[]>({
    queryKey: taskPRsKey(projectId, taskId),
    queryFn: () => listTaskPRs(api, taskId),
    retry: false,
    throwOnError: false,
  });
  const [linking, setLinking] = useState(false);
  const repoName = (id: string) => repos.find((r) => r.id === id)?.full_name;

  return (
    <Section
      title="Pull requests"
      actions={
        canEdit && !linking ? (
          <Pill onClick={() => setLinking(true)}>
            <Plus size={12} aria-hidden="true" />
            Link pull request
          </Pill>
        ) : undefined
      }
    >
      {isLoading ? (
        <Skeleton height={44} />
      ) : (
        <>
          {prs.length > 0 && (
            <div className="ghp-rows">
              {prs.map((pr) => (
                <PRRow key={pr.id} api={api} pr={pr} repoName={repoName(pr.repo_id)} projectId={projectId} taskId={taskId} canEdit={canEdit} />
              ))}
            </div>
          )}
          {prs.length === 0 && !linking && (
            <SectionEmpty icon={<GitPullRequest size={16} aria-hidden="true" />}>No pull requests linked yet.</SectionEmpty>
          )}
          {linking && <LinkPRForm api={api} projectId={projectId} taskId={taskId} repos={repos} onDone={() => setLinking(false)} />}
        </>
      )}
    </Section>
  );
}
