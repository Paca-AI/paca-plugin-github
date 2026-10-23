import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitBranch, Link2, Plus } from "lucide-react";
import { useState } from "react";
import {
  type CreateBranchResult,
  createBranch,
  ErrorCode,
  getPluginErrorCode,
  type LinkedRepository,
  linkBranchToTask,
  listTaskBranches,
  type TaskBranch,
  taskBranchesKey,
} from "./github-api";
import { Button, CommandBlock, CopyIconButton, Field, Input, Pill, Section, SectionEmpty, Segmented, Select, Skeleton } from "./ui";

// ── Existing branch ───────────────────────────────────────────────────────────

function BranchRow({ branch, repoName }: { branch: TaskBranch; repoName?: string }) {
  return (
    <div className="ghp-row-item">
      <GitBranch className="ghp-row-icon" size={14} aria-hidden="true" />
      <div className="ghp-row-body">
        {repoName ? (
          <a className="ghp-row-title ghp-row-title-mono" href={`https://github.com/${repoName}/tree/${branch.branch_name}`} target="_blank" rel="noopener noreferrer" title={branch.branch_name}>
            {branch.branch_name}
          </a>
        ) : (
          <span className="ghp-row-title ghp-row-title-mono" title={branch.branch_name}>{branch.branch_name}</span>
        )}
        {repoName && (
          <div className="ghp-row-meta">
            <span className="ghp-truncate">{repoName}</span>
          </div>
        )}
      </div>
      <div className="ghp-row-actions">
        <CopyIconButton text={`git fetch origin && git checkout ${branch.branch_name}`} label="Copy checkout command" />
      </div>
    </div>
  );
}

// ── Branch name helpers ───────────────────────────────────────────────────────

const BRANCH_TYPES = ["feat", "fix", "chore", "docs", "test", "refactor"] as const;

function parseGitHubBranchUrl(raw: string): { fullName: string; branchName: string } | null {
  try {
    const url = new URL(raw.trim());
    if (url.hostname !== "github.com") return null;
    const parts = url.pathname.replace(/^\//, "").split("/");
    if (parts.length < 4 || parts[2] !== "tree") return null;
    const branchName = parts.slice(3).join("/");
    if (!branchName) return null;
    return { fullName: `${parts[0]}/${parts[1]}`, branchName };
  } catch {
    return null;
  }
}

// ── Create form ───────────────────────────────────────────────────────────────

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
  repos: LinkedRepository[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const taskRef = taskIdPrefix ? `${taskIdPrefix.toUpperCase()}-${taskNumber}` : `${taskNumber}`;
  const slug = taskTitle
    ? `-${taskTitle
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "")
        .slice(0, 30)}`
    : "";

  const [type, setType] = useState<(typeof BRANCH_TYPES)[number]>("feat");
  const [branchName, setBranchName] = useState(`feat/${taskRef}${slug}`);
  const [repoId, setRepoId] = useState(repos.length === 1 ? repos[0].id : "");
  const [source, setSource] = useState("");
  const [error, setError] = useState<string | null>(null);

  function changeType(next: (typeof BRANCH_TYPES)[number]) {
    setType(next);
    setBranchName((prev) => {
      const slash = prev.indexOf("/");
      return `${next}${slash >= 0 ? prev.slice(slash) : `/${taskRef}`}`;
    });
  }

  const create = useMutation({
    mutationFn: (): Promise<CreateBranchResult> => createBranch(api, taskId, repoId, branchName, source.trim() || undefined),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: taskBranchesKey(projectId, taskId) });
      onDone();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubIntegrationNotFound
          ? "GitHub isn’t connected for this project."
          : code === ErrorCode.GitHubRepositoryNotFound
            ? "That repository isn’t linked any more."
            : code === ErrorCode.GitHubBranchAlreadyLinked
              ? "This branch is already linked to the task."
              : code === ErrorCode.GitHubTokenInsufficientPermissions
                ? "The GitHub connection can’t create branches. It needs write access to repository contents — check Project Settings → GitHub."
                : "Couldn’t create the branch. Try again.",
      );
    },
  });

  function submit() {
    setError(null);
    if (!branchName.trim()) return setError("Branch name is required.");
    if (!repoId) return setError("Select a repository.");
    create.mutate();
  }

  const local = `git checkout -b ${branchName} && git push -u origin ${branchName}`;

  return (
    <form
      className="ghp-form"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
    >
      <div className="ghp-field">
        <span className="ghp-label" id="ghp-type-label">Type</span>
        <Segmented label="Branch type" options={BRANCH_TYPES} value={type} onChange={changeType} />
      </div>
      <Field label="Branch name">
        {(id) => (
          <Input id={id} mono spellCheck={false} value={branchName} placeholder={`feat/${taskRef}`} onChange={(e) => { setBranchName(e.target.value); setError(null); }} />
        )}
      </Field>
      {repos.length > 1 && (
        <Field label="Repository">
          {(id) => (
            <Select id={id} value={repoId} onChange={(e) => { setRepoId(e.target.value); setError(null); }}>
              <option value="">Select a repository…</option>
              {repos.map((r) => (
                <option key={r.id} value={r.id}>{r.full_name}</option>
              ))}
            </Select>
          )}
        </Field>
      )}
      <Field label="Branch from" hint="Optional — defaults to the repository’s default branch.">
        {(id) => <Input id={id} mono spellCheck={false} value={source} placeholder="main" onChange={(e) => setSource(e.target.value)} />}
      </Field>
      {error && <p className="ghp-sec-error" role="alert">{error}</p>}
      <div className="ghp-form-actions">
        <Button type="submit" variant="primary" size="sm" loading={create.isPending}>
          <GitBranch size={14} aria-hidden="true" />
          Create on GitHub
        </Button>
        <Button variant="ghost" size="sm" onClick={onDone} disabled={create.isPending}>
          Cancel
        </Button>
      </div>
      <details className="ghp-details">
        <summary>Or create it locally</summary>
        <CommandBlock command={local} />
      </details>
    </form>
  );
}

// ── Link form ─────────────────────────────────────────────────────────────────

function LinkBranchForm({ api, projectId, taskId, repos, onDone }: { api: PluginApiClient; projectId: string; taskId: string; repos: LinkedRepository[]; onDone: () => void }) {
  const queryClient = useQueryClient();
  const [selectedRepoId, setSelectedRepoId] = useState(repos.length === 1 ? repos[0].id : "");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const parsed = parseGitHubBranchUrl(value);
  const urlRepo = parsed ? (repos.find((r) => r.full_name === parsed.fullName) ?? null) : null;
  const repoId = parsed ? (urlRepo?.id ?? "") : selectedRepoId;
  const branchName = parsed ? parsed.branchName : value.trim();

  const link = useMutation({
    mutationFn: () => linkBranchToTask(api, taskId, repoId, branchName),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: taskBranchesKey(projectId, taskId) });
      onDone();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubIntegrationNotFound
          ? "GitHub isn’t connected for this project."
          : code === ErrorCode.GitHubRepositoryNotFound
            ? "That repository isn’t linked any more."
            : code === ErrorCode.GitHubBranchNotFound
              ? `Branch “${branchName}” wasn’t found in that repository.`
              : code === ErrorCode.GitHubBranchAlreadyLinked
                ? `Branch “${branchName}” is already linked to this task.`
                : code === ErrorCode.GitHubTokenInsufficientPermissions
                  ? "The GitHub connection can’t read branches. Check it in Project Settings → GitHub."
                  : "Couldn’t link the branch. Try again.",
      );
    },
  });

  function submit() {
    if (parsed) {
      if (!urlRepo) return setError(`“${parsed.fullName}” isn’t linked to this project.`);
    } else {
      if (!repoId) return setError("Select a repository.");
      if (!branchName) return setError("Enter a branch name or paste a GitHub branch link.");
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
      <Field label="Existing branch" hint={parsed && urlRepo ? `Will link ${parsed.branchName} from ${urlRepo.full_name}` : "Enter a branch name, or paste a GitHub link."}>
        {(id) => (
          <Input
            id={id}
            mono
            spellCheck={false}
            value={value}
            placeholder="feature/foo"
            disabled={link.isPending}
            aria-invalid={!!error}
            onChange={(e) => { setValue(e.target.value); setError(null); }}
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
                <option key={r.id} value={r.id}>{r.full_name}</option>
              ))}
            </Select>
          )}
        </Field>
      )}
      {error && <p className="ghp-sec-error" role="alert">{error}</p>}
      <div className="ghp-form-actions">
        <Button type="submit" variant="primary" size="sm" disabled={!value.trim()} loading={link.isPending}>
          <Link2 size={14} aria-hidden="true" />
          Link branch
        </Button>
        <Button variant="ghost" size="sm" onClick={onDone} disabled={link.isPending}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// ── Section ───────────────────────────────────────────────────────────────────

export function BranchesSection({
  api,
  projectId,
  taskId,
  taskIdPrefix,
  taskNumber,
  taskTitle,
  repos,
  canEdit,
}: {
  api: PluginApiClient;
  projectId: string;
  taskId: string;
  taskIdPrefix: string;
  taskNumber: number;
  taskTitle?: string;
  repos: LinkedRepository[];
  canEdit: boolean;
}) {
  const [mode, setMode] = useState<"create" | "link" | null>(null);
  const { data: branches = [], isLoading } = useQuery<TaskBranch[]>({
    queryKey: taskBranchesKey(projectId, taskId),
    queryFn: () => listTaskBranches(api, taskId),
    retry: false,
    throwOnError: false,
  });
  const repoName = (id: string) => repos.find((r) => r.id === id)?.full_name;
  const done = () => setMode(null);

  return (
    <Section
      title="Branches"
      actions={
        canEdit && mode === null ? (
          <>
            <Pill quiet onClick={() => setMode("link")}>
              <Link2 size={12} aria-hidden="true" />
              Link branch
            </Pill>
            <Pill onClick={() => setMode("create")}>
              <Plus size={12} aria-hidden="true" />
              New branch
            </Pill>
          </>
        ) : undefined
      }
    >
      {isLoading ? (
        <Skeleton height={44} />
      ) : (
        <>
          {branches.length > 0 && (
            <div className="ghp-rows">
              {branches.map((b) => (
                <BranchRow key={b.id} branch={b} repoName={repoName(b.repo_id)} />
              ))}
            </div>
          )}
          {branches.length === 0 && mode === null && (
            <SectionEmpty icon={<GitBranch size={16} aria-hidden="true" />}>No branches linked yet.</SectionEmpty>
          )}
          {mode === "create" && <CreateBranchForm api={api} projectId={projectId} taskId={taskId} taskIdPrefix={taskIdPrefix} taskNumber={taskNumber} taskTitle={taskTitle} repos={repos} onDone={done} />}
          {mode === "link" && <LinkBranchForm api={api} projectId={projectId} taskId={taskId} repos={repos} onDone={done} />}
        </>
      )}
    </Section>
  );
}
