import { PluginApiClient, PluginQueryClientProvider } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  ChevronDown,
  ChevronRight,
  ClipboardCopy,
  ExternalLink,
  GitBranch,
  GitMerge,
  GitPullRequest,
  GitPullRequestClosed,
  Loader2,
  Plus,
  Terminal,
  Trash2,
} from "lucide-react";
import { useMemo, useState } from "react";
import {
  type CreateBranchResult,
  createBranch,
  ErrorCode,
  getPluginErrorCode,
  type LinkedRepository,
  linkedReposKey,
  listLinkedRepositories,
  listTaskBranches,
  listTaskPRs,
  linkPRToTask,
  type PullRequest,
  type TaskBranch,
  taskBranchesKey,
  taskPRsKey,
  unlinkPRFromTask,
} from "./github-api";

// ── Utilities ──────────────────────────────────────────────────────────────────

function cn(...classes: (string | undefined | null | false)[]): string {
  return classes.filter(Boolean).join(" ");
}

// ─────────────────────── Pull Requests Section ────────────────────────────────

// ── PR state badge ────────────────────────────────────────────────────────────

function PRStateBadge({ state }: { state: PullRequest["state"] }) {
  if (state === "merged") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-violet-500/15 px-2 py-0.5 text-[10px] font-semibold text-violet-500">
        <GitMerge className="size-3" />
        Merged
      </span>
    );
  }
  if (state === "closed") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/15 px-2 py-0.5 text-[10px] font-semibold text-destructive/80">
        <GitPullRequestClosed className="size-3" />
        Closed
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/15 px-2 py-0.5 text-[10px] font-semibold text-emerald-600 dark:text-emerald-500">
      <GitPullRequest className="size-3" />
      Open
    </span>
  );
}

// ── Single PR row ─────────────────────────────────────────────────────────────

function PRRow({
  api,
  pr,
  projectId,
  taskId,
  canEdit,
}: {
  api: PluginApiClient;
  pr: PullRequest;
  projectId: string;
  taskId: string;
  canEdit: boolean;
}) {
  const queryClient = useQueryClient();

  const unlinkMutation = useMutation({
    mutationFn: () => unlinkPRFromTask(api, taskId, pr.id),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: taskPRsKey(projectId, taskId),
      });
    },
  });

  return (
    <div className="group flex items-start gap-2.5 rounded-lg border border-border/50 bg-card px-3 py-2.5 hover:border-border/80 transition-colors">
      <div className="mt-0.5 shrink-0">
        <PRStateBadge state={pr.state} />
      </div>
      <div className="min-w-0 flex-1">
        <a
          href={pr.html_url}
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center gap-1.5 text-sm font-medium hover:text-primary transition-colors"
        >
          <span className="truncate">{pr.title}</span>
          <ExternalLink className="size-3 shrink-0 opacity-50" />
        </a>
        <div className="flex items-center gap-2 mt-1 flex-wrap">
          <span className="text-[11px] text-muted-foreground font-mono">
            #{pr.pr_number}
          </span>
          <span className="text-muted-foreground/40">·</span>
          <span className="text-[11px] text-muted-foreground">
            {pr.head_branch}
          </span>
          {pr.author && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span className="text-[11px] text-muted-foreground">
                by {pr.author}
              </span>
            </>
          )}
        </div>
      </div>
      {canEdit && (
        <button
          type="button"
          aria-label="Unlink pull request"
          className="shrink-0 mt-0.5 opacity-0 group-hover:opacity-100 transition-opacity text-muted-foreground/60 hover:text-destructive"
          onClick={() => unlinkMutation.mutate()}
          disabled={unlinkMutation.isPending}
        >
          {unlinkMutation.isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Trash2 className="size-3.5" />
          )}
        </button>
      )}
    </div>
  );
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function parseGitHubPRUrl(
  raw: string,
): { fullName: string; prNumber: number } | null {
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

// ── Link PR form ──────────────────────────────────────────────────────────────

function LinkPRForm({
  api,
  projectId,
  taskId,
  repos,
  onDone,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  repos: LinkedRepository[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [selectedRepoId, setSelectedRepoId] = useState(
    repos.length === 1 ? repos[0].id : "",
  );
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const parsed = parseGitHubPRUrl(value);
  const urlMatchedRepo = parsed
    ? (repos.find((r) => r.full_name === parsed.fullName) ?? null)
    : null;
  const effectiveRepoId = parsed ? (urlMatchedRepo?.id ?? "") : selectedRepoId;

  const mutation = useMutation({
    mutationFn: () => {
      const prNum = parsed ? parsed.prNumber : Number(value);
      return linkPRToTask(api, taskId, effectiveRepoId, prNum);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: taskPRsKey(projectId, taskId),
      });
      setValue("");
      setError(null);
      onDone();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      if (code === ErrorCode.GitHubIntegrationNotFound) {
        setError("No GitHub token configured for this project.");
        return;
      }
      if (code === ErrorCode.GitHubRepositoryNotFound) {
        setError("Repository not found. It may have been unlinked.");
        return;
      }
      if (code === ErrorCode.GitHubPRNotFound) {
        const displayNum = parsed ? parsed.prNumber : value;
        setError(
          `PR #${displayNum} was not found in the selected repository.`,
        );
        return;
      }
      if (code === ErrorCode.GitHubPRAlreadyLinked) {
        const displayNum = parsed ? parsed.prNumber : value;
        setError(`PR #${displayNum} is already linked to this task.`);
        return;
      }
      if (code === ErrorCode.GitHubTokenInsufficientPermissions) {
        setError(
          "Your GitHub token does not have permission to read pull requests. Update it in Project Settings > GitHub.",
        );
        return;
      }
      setError("Failed to link pull request. Please try again.");
    },
  });

  function submit() {
    if (parsed) {
      if (!urlMatchedRepo) {
        setError(
          `Repository "${parsed.fullName}" is not linked to this project.`,
        );
        return;
      }
    } else {
      if (!effectiveRepoId) {
        setError("Select a repository.");
        return;
      }
      const num = Number(value);
      if (!value.trim() || !Number.isInteger(num) || num <= 0) {
        setError("Enter a valid PR number or paste a GitHub PR URL.");
        return;
      }
    }
    mutation.mutate();
  }

  return (
    <div className="space-y-3 rounded-lg border border-border/50 bg-card px-3 py-3">
      {/* Repository selector */}
      <div>
        <p className="text-[11px] text-muted-foreground mb-1">Repository</p>
        <select
          value={parsed ? (urlMatchedRepo?.id ?? "") : selectedRepoId}
          onChange={(e) => {
            setSelectedRepoId(e.target.value);
            setError(null);
          }}
          disabled={mutation.isPending || !!parsed}
          className="w-full rounded-md border border-border/60 bg-background px-2.5 py-1.5 text-xs focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
        >
          <option value="">Select repository…</option>
          {repos.map((r) => (
            <option key={r.id} value={r.id}>
              {r.full_name}
            </option>
          ))}
        </select>
      </div>

      {/* PR number or URL */}
      <div>
        <p className="text-[11px] text-muted-foreground mb-1">
          PR number or GitHub URL
        </p>
        <input
          type="text"
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
            if (e.key === "Escape") onDone();
          }}
          placeholder="42 or https://github.com/owner/repo/pull/42"
          className={cn(
            "w-full rounded-md border bg-background px-2.5 py-1.5 text-xs focus:outline-none focus:ring-1 focus:ring-ring",
            error ? "border-destructive" : "border-border/60",
          )}
          // biome-ignore lint/a11y/noAutofocus: intentional for inline form
          autoFocus
          disabled={mutation.isPending}
        />
        {parsed && urlMatchedRepo && (
          <p className="mt-1 text-[11px] text-muted-foreground">
            Will link PR #{parsed.prNumber} from{" "}
            <span className="font-medium">{urlMatchedRepo.full_name}</span>
          </p>
        )}
      </div>

      {error && (
        <p className="text-[11px] text-destructive/80 leading-relaxed">
          {error}
        </p>
      )}

      {/* Action buttons */}
      <div className="flex items-center gap-2 pt-0.5">
        <button
          type="button"
          onClick={submit}
          disabled={!value.trim() || mutation.isPending}
          className="flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60 transition-colors"
        >
          {mutation.isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <GitPullRequest className="size-3.5" />
          )}
          Link pull request
        </button>
        <button
          type="button"
          onClick={onDone}
          className="text-xs text-muted-foreground/60 hover:text-muted-foreground transition-colors"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

// ── Pull Requests section ─────────────────────────────────────────────────────

function PullRequestsSection({
  api,
  projectId,
  taskId,
  canEdit,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  canEdit: boolean;
}) {
  const { data: prs = [], isLoading } = useQuery<PullRequest[]>({
    queryKey: taskPRsKey(projectId, taskId),
    queryFn: () => listTaskPRs(api, taskId),
    retry: false,
    throwOnError: false,
  });
  const { data: linkedRepos = [] } = useQuery<LinkedRepository[]>({
    queryKey: linkedReposKey(projectId),
    queryFn: () => listLinkedRepositories(api),
    retry: false,
    throwOnError: false,
  });
  const [expanded, setExpanded] = useState(true);
  const [linking, setLinking] = useState(false);

  const count = prs.length;
  const canLinkPR = canEdit && linkedRepos.length > 0;

  return (
    <div>
      <button
        type="button"
        className="flex w-full items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground/70 mb-3 hover:text-muted-foreground transition-colors"
        onClick={() => setExpanded((v) => !v)}
      >
        <GitPullRequest className="size-3.5 shrink-0" />
        <span>Pull Requests</span>
        {count > 0 && (
          <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-bold text-muted-foreground normal-case tracking-normal">
            {count}
          </span>
        )}
        <div className="flex-1 h-px bg-linear-to-r from-border/40 to-transparent" />
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0" />
        )}
      </button>

      {expanded && (
        <div className="space-y-2">
          {isLoading ? (
            <div className="flex items-center gap-2 py-2 text-muted-foreground/60 text-xs">
              <Loader2 className="size-3.5 animate-spin" />
              Loading…
            </div>
          ) : (
            <>
              {prs.map((pr) => (
                <PRRow
                  key={pr.id}
                  api={api}
                  pr={pr}
                  projectId={projectId}
                  taskId={taskId}
                  canEdit={canEdit}
                />
              ))}

              {count === 0 && !linking && (
                <p className="text-xs text-muted-foreground/50 italic py-1">
                  No pull requests linked yet.
                </p>
              )}

              {linking ? (
                <LinkPRForm
                  api={api}
                  projectId={projectId}
                  taskId={taskId}
                  repos={linkedRepos}
                  onDone={() => setLinking(false)}
                />
              ) : (
                canLinkPR && (
                  <button
                    type="button"
                    className="flex items-center gap-1.5 text-xs text-muted-foreground/60 hover:text-muted-foreground transition-colors py-1"
                    onClick={() => setLinking(true)}
                  >
                    <Plus className="size-3.5" />
                    Link pull request
                  </button>
                )
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

// ─────────────────────── Branches Section ─────────────────────────────────────

// ── Copy button ───────────────────────────────────────────────────────────────

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard?.writeText(text)?.catch(() => {});
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <button
      type="button"
      aria-label="Copy to clipboard"
      onClick={handleCopy}
      className="shrink-0 text-muted-foreground/60 hover:text-muted-foreground transition-colors"
    >
      {copied ? (
        <Check className="size-3.5 text-emerald-500" />
      ) : (
        <ClipboardCopy className="size-3.5" />
      )}
    </button>
  );
}

// ── Command block ─────────────────────────────────────────────────────────────

function CommandBlock({ command }: { command: string }) {
  return (
    <div className="flex items-center gap-2 rounded-md bg-muted/60 border border-border/50 px-3 py-2 mt-1.5">
      <Terminal className="size-3.5 shrink-0 text-muted-foreground/50" />
      <code className="flex-1 text-[11px] font-mono text-foreground/80 break-all">
        {command}
      </code>
      <CopyButton text={command} />
    </div>
  );
}

// ── Existing branch row ───────────────────────────────────────────────────────

function BranchRow({ branch }: { branch: TaskBranch }) {
  const cloneCmd = `git fetch origin && git checkout ${branch.branch_name}`;
  return (
    <div className="rounded-lg border border-border/50 bg-card px-3 py-2.5 space-y-1.5">
      <div className="flex items-center gap-2">
        <GitBranch className="size-3.5 shrink-0 text-muted-foreground/60" />
        <span className="text-[12px] font-mono truncate text-foreground/90 flex-1">
          {branch.branch_name}
        </span>
      </div>
      <CommandBlock command={cloneCmd} />
    </div>
  );
}

// ── Branch creation form ──────────────────────────────────────────────────────

const BRANCH_TYPES = [
  "feat",
  "fix",
  "chore",
  "docs",
  "test",
  "refactor",
] as const;

function CreateBranchForm({
  api,
  projectId,
  taskId,
  taskIdPrefix,
  taskNumber,
  taskTitle,
  repos,
  onDone,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  taskIdPrefix: string;
  taskNumber: number;
  taskTitle?: string;
  repos: { id: string; full_name: string }[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();

  const taskRef = taskIdPrefix
    ? `${taskIdPrefix.toUpperCase()}-${taskNumber}`
    : `${taskNumber}`;

  const defaultSlug = taskTitle
    ? `-${taskTitle
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "")
        .slice(0, 30)}`
    : "";

  const [type, setType] = useState<(typeof BRANCH_TYPES)[number]>("feat");
  const [branchName, setBranchName] = useState(
    `${type}/${taskRef}${defaultSlug}`,
  );
  const [selectedRepoId, setSelectedRepoId] = useState(
    repos.length === 1 ? repos[0].id : "",
  );
  const [sourceBranch, setSourceBranch] = useState("");
  const [error, setError] = useState<string | null>(null);

  function handleTypeChange(newType: (typeof BRANCH_TYPES)[number]) {
    setType(newType);
    setBranchName((prev) => {
      const slash = prev.indexOf("/");
      const rest = slash >= 0 ? prev.slice(slash) : `/${taskRef}`;
      return `${newType}${rest}`;
    });
  }

  const createMutation = useMutation({
    mutationFn: (): Promise<CreateBranchResult> =>
      createBranch(
        api,
        taskId,
        selectedRepoId,
        branchName,
        sourceBranch || undefined,
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: taskBranchesKey(projectId, taskId),
      });
      onDone();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      if (code === ErrorCode.GitHubIntegrationNotFound) {
        setError("No GitHub token configured for this project.");
        return;
      }
      if (code === ErrorCode.GitHubRepositoryNotFound) {
        setError("Repository not found. It may have been unlinked.");
        return;
      }
      if (code === ErrorCode.GitHubBranchAlreadyLinked) {
        setError("This branch is already linked to the task.");
        return;
      }
      if (code === ErrorCode.GitHubTokenInsufficientPermissions) {
        setError(
          "Your GitHub token does not have permission to create branches. Please update it in Project Settings > GitHub with a token that has the repo (contents) scope.",
        );
        return;
      }
      setError("Failed to create branch. Please try again.");
    },
  });

  const localCmd = `git checkout -b ${branchName} && git push -u origin ${branchName}`;

  function validateForm(): boolean {
    if (!branchName.trim()) {
      setError("Branch name is required.");
      return false;
    }
    if (repos.length > 1 && !selectedRepoId) {
      setError("Select a repository.");
      return false;
    }
    return true;
  }

  function handleCreate() {
    setError(null);
    if (!validateForm()) return;
    createMutation.mutate();
  }

  return (
    <div className="space-y-3 rounded-lg border border-border/50 bg-card px-3 py-3">
      {/* Branch type pills */}
      <div>
        <p className="text-[11px] text-muted-foreground mb-1.5">Type</p>
        <div className="flex flex-wrap gap-1.5">
          {BRANCH_TYPES.map((t) => (
            <button
              key={t}
              type="button"
              onClick={() => handleTypeChange(t)}
              className={cn(
                "rounded-full px-2.5 py-0.5 text-[11px] font-medium border transition-colors",
                t === type
                  ? "border-primary/60 bg-primary/10 text-primary"
                  : "border-border/50 text-muted-foreground hover:border-border",
              )}
            >
              {t}
            </button>
          ))}
        </div>
      </div>

      {/* Branch name */}
      <div>
        <p className="text-[11px] text-muted-foreground mb-1">Branch name</p>
        <input
          type="text"
          value={branchName}
          onChange={(e) => {
            setBranchName(e.target.value);
            setError(null);
          }}
          placeholder={`feat/${taskRef}`}
          className="w-full rounded-md border border-border/60 bg-background px-2.5 py-1.5 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
          spellCheck={false}
        />
      </div>

      {/* Repo selector — only when multiple repos */}
      {repos.length > 1 && (
        <div>
          <p className="text-[11px] text-muted-foreground mb-1">Repository</p>
          <select
            value={selectedRepoId}
            onChange={(e) => setSelectedRepoId(e.target.value)}
            className="w-full rounded-md border border-border/60 bg-background px-2.5 py-1.5 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
          >
            <option value="">Select repository…</option>
            {repos.map((r) => (
              <option key={r.id} value={r.id}>
                {r.full_name}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Source branch (optional) */}
      <div>
        <p className="text-[11px] text-muted-foreground mb-1">
          Source branch{" "}
          <span className="opacity-60">(optional, defaults to repo default)</span>
        </p>
        <input
          type="text"
          value={sourceBranch}
          onChange={(e) => setSourceBranch(e.target.value)}
          placeholder="main"
          className="w-full rounded-md border border-border/60 bg-background px-2.5 py-1.5 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
          spellCheck={false}
        />
      </div>

      {error && (
        <p className="text-[11px] text-destructive/80 leading-relaxed">
          {error}
        </p>
      )}

      {/* Action buttons */}
      <div className="flex flex-col gap-2 pt-0.5">
        <button
          type="button"
          disabled={createMutation.isPending}
          onClick={handleCreate}
          className="flex items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60 transition-colors"
        >
          {createMutation.isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <GitBranch className="size-3.5" />
          )}
          Create branch on GitHub
        </button>

        <div>
          <p className="text-[11px] text-muted-foreground mb-0.5">
            Or create locally:
          </p>
          <CommandBlock command={localCmd} />
        </div>
      </div>

      <button
        type="button"
        className="text-xs text-muted-foreground/60 hover:text-muted-foreground transition-colors"
        onClick={onDone}
      >
        Cancel
      </button>
    </div>
  );
}

// ── Branches section ──────────────────────────────────────────────────────────

function BranchesSection({
  api,
  projectId,
  taskId,
  taskIdPrefix,
  taskNumber,
  taskTitle,
  canEdit,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  taskIdPrefix: string;
  taskNumber: number;
  taskTitle?: string;
  canEdit: boolean;
}) {
  const [expanded, setExpanded] = useState(true);
  const [creating, setCreating] = useState(false);

  const { data: branches = [], isLoading } = useQuery<TaskBranch[]>({
    queryKey: taskBranchesKey(projectId, taskId),
    queryFn: () => listTaskBranches(api, taskId),
    retry: false,
    throwOnError: false,
  });

  const { data: linkedRepos = [] } = useQuery<LinkedRepository[]>({
    queryKey: linkedReposKey(projectId),
    queryFn: () => listLinkedRepositories(api),
    retry: false,
    throwOnError: false,
  });

  const count = branches.length;
  const canCreate = canEdit && linkedRepos.length > 0;

  return (
    <div>
      <button
        type="button"
        className="flex w-full items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground/70 mb-3 hover:text-muted-foreground transition-colors"
        onClick={() => setExpanded((v) => !v)}
      >
        <GitBranch className="size-3.5 shrink-0" />
        <span>Branches</span>
        {count > 0 && (
          <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-bold text-muted-foreground normal-case tracking-normal">
            {count}
          </span>
        )}
        <div className="flex-1 h-px bg-linear-to-r from-border/40 to-transparent" />
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0" />
        )}
      </button>

      {expanded && (
        <div className="space-y-2">
          {isLoading ? (
            <div className="flex items-center gap-2 py-2 text-muted-foreground/60 text-xs">
              <Loader2 className="size-3.5 animate-spin" />
              Loading…
            </div>
          ) : (
            <>
              {branches.map((branch) => (
                <BranchRow key={branch.id} branch={branch} />
              ))}

              {count === 0 && !creating && (
                <p className="text-xs text-muted-foreground/50 italic py-1">
                  No branches linked yet.
                </p>
              )}

              {creating ? (
                <CreateBranchForm
                  api={api}
                  projectId={projectId}
                  taskId={taskId}
                  taskIdPrefix={taskIdPrefix}
                  taskNumber={taskNumber}
                  taskTitle={taskTitle}
                  repos={linkedRepos}
                  onDone={() => setCreating(false)}
                />
              ) : (
                canCreate && (
                  <button
                    type="button"
                    className="flex items-center gap-1.5 text-xs text-muted-foreground/60 hover:text-muted-foreground transition-colors py-1"
                    onClick={() => setCreating(true)}
                  >
                    <GitBranch className="size-3.5" />
                    Create branch
                  </button>
                )
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

// ─────────────────────── Main Task Section ────────────────────────────────────

interface GitHubTaskSectionProps {
  projectId: string;
  taskId: string;
  canEdit?: boolean;
}

function GitHubTaskSectionInner({
  api,
  projectId,
  taskId,
  canEdit,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  canEdit: boolean;
}) {
  // Fetch task and project metadata needed for branch creation
  const { data: task } = useQuery({
    queryKey: ["plugin", "task", taskId],
    queryFn: () => api.getTask(taskId),
    staleTime: 60_000,
  });

  const { data: project } = useQuery({
    queryKey: ["plugin", "project", projectId],
    queryFn: () => api.getProject(),
    staleTime: 60_000,
  });

  const { data: linkedRepos, isLoading: reposLoading } = useQuery<LinkedRepository[]>({
    queryKey: linkedReposKey(projectId),
    queryFn: () => listLinkedRepositories(api),
    retry: false,
    throwOnError: false,
  });

  const taskIdPrefix = project?.task_id_prefix ?? "";
  const taskNumber = task?.task_number ?? 0;
  const taskTitle = task?.title;

  // Don't render anything until we know whether repos are linked
  if (reposLoading) return null;
  if (!linkedRepos || linkedRepos.length === 0) return null;

  return (
    <div className="space-y-6">
      <BranchesSection
        api={api}
        projectId={projectId}
        taskId={taskId}
        taskIdPrefix={taskIdPrefix}
        taskNumber={taskNumber}
        taskTitle={taskTitle}
        canEdit={canEdit}
      />
      <PullRequestsSection
        api={api}
        projectId={projectId}
        taskId={taskId}
        canEdit={canEdit}
      />
    </div>
  );
}

export default function GitHubTaskSection({
  projectId,
  taskId,
  canEdit = true,
}: GitHubTaskSectionProps) {
  const api = useMemo(
    () =>
      new PluginApiClient({
        baseUrl: `${window.location.origin}/api/v1`,
        projectId,
        fetch: (url, init) =>
          window.fetch(url, { ...init, credentials: "include" }),
      }),
    [projectId],
  );

  return (
    <PluginQueryClientProvider>
      <GitHubTaskSectionInner
        api={api}
        projectId={projectId}
        taskId={taskId}
        canEdit={canEdit}
      />
    </PluginQueryClientProvider>
  );
}
