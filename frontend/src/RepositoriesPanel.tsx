import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, GitBranch, Lock, Plus, RefreshCw, Search, Unlink } from "lucide-react";
import { useState } from "react";
import {
  type AccessibleRepo,
  accessibleReposKey,
  ErrorCode,
  getPluginErrorCode,
  type LinkedRepository,
  linkedReposKey,
  linkRepository,
  listAccessibleRepos,
  unlinkRepository,
} from "./github-api";
import { Avatar, Badge, Banner, Button, EmptyState, Input, Modal, Skeleton } from "./ui";

// ── Add repository ────────────────────────────────────────────────────────────

function AddRepoDialog({
  api,
  projectId,
  isApp,
  open,
  onClose,
}: {
  api: PluginApiClient;
  projectId: string;
  isApp: boolean;
  open: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [error, setError] = useState<string | null>(null);

  const { data: repos = [], isLoading, isFetching } = useQuery({
    queryKey: accessibleReposKey(projectId),
    queryFn: () => listAccessibleRepos(api),
    enabled: open,
    staleTime: 0,
  });

  const link = useMutation({
    mutationFn: (repo: AccessibleRepo) => linkRepository(api, repo.owner, repo.repo_name),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: linkedReposKey(projectId) });
      close();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubWebhookURLNotPublic
          ? "GitHub can’t reach this server to register the webhook (is it on localhost?). Set PUBLIC_URL to a public HTTPS address and try again."
          : code === ErrorCode.GitHubWebhookCreationFailed
            ? "Couldn’t create the webhook. The token needs the admin:repo_hook scope and admin access to the repository."
            : code === ErrorCode.GitHubRepoAlreadyLinked
              ? "This repository is already linked to the project."
              : code === ErrorCode.GitHubRepoNotAccessible
                ? isApp
                  ? "The app can’t see this repository. Add it to the app’s installation on GitHub."
                  : "Repository not found or not accessible with this token."
                : "Couldn’t link the repository. Try again.",
      );
    },
  });

  function close() {
    setSearch("");
    setError(null);
    onClose();
  }

  const q = search.trim().toLowerCase();
  const filtered = repos.filter((r) => r.full_name.toLowerCase().includes(q));

  return (
    <Modal
      open={open}
      onClose={close}
      wide
      title="Add a repository"
      description={isApp ? "Repositories the GitHub App can see. Missing one? Add it to the installation on GitHub." : "Repositories your token can access. A webhook is registered on each one you link."}
      footer={
        <Button variant="secondary" onClick={close} disabled={link.isPending}>
          Close
        </Button>
      }
    >
      <div className="ghp-row">
        <div className="ghp-input-wrap">
          <Search size={14} aria-hidden="true" />
          <Input aria-label="Search repositories" placeholder="Search repositories…" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <Button variant="secondary" icon aria-label="Reload repositories" disabled={isFetching} onClick={() => queryClient.refetchQueries({ queryKey: accessibleReposKey(projectId) })}>
          <RefreshCw size={14} className={isFetching ? "ghp-spin" : undefined} aria-hidden="true" />
        </Button>
      </div>

      {error && <Banner tone="danger">{error}</Banner>}

      <div className="ghp-stack" style={{ gap: 8 }}>
        {isLoading ? (
          [0, 1, 2].map((i) => <Skeleton key={i} height={52} />)
        ) : repos.length === 0 ? (
          <EmptyState icon={<BookOpen size={20} />} title="No repositories found">
            {isApp ? "Install the app on at least one repository, then reload." : "This token can’t see any repositories."}
          </EmptyState>
        ) : filtered.length === 0 ? (
          <p className="ghp-muted" style={{ textAlign: "center", padding: 24 }}>
            Nothing matches “{search}”.
          </p>
        ) : (
          filtered.map((repo) => (
            <button key={repo.full_name} type="button" className="ghp-repo-pick" disabled={link.isPending} onClick={() => link.mutate(repo)}>
              <span className="ghp-grow">
                <span className="ghp-row" style={{ gap: 8 }}>
                  <GitBranch size={14} aria-hidden="true" />
                  <span className="ghp-strong ghp-truncate">{repo.full_name}</span>
                  {repo.private && (
                    <Badge>
                      <Lock size={10} aria-hidden="true" />
                      Private
                    </Badge>
                  )}
                </span>
                {repo.description && <span className="ghp-muted ghp-small ghp-truncate" style={{ display: "block", paddingLeft: 22 }}>{repo.description}</span>}
              </span>
              {link.isPending && link.variables?.full_name === repo.full_name ? <RefreshCw size={14} className="ghp-spin" aria-label="Linking" /> : <Plus size={14} className="ghp-muted" aria-hidden="true" />}
            </button>
          ))
        )}
      </div>
    </Modal>
  );
}

// ── Linked repo row ───────────────────────────────────────────────────────────

function LinkedRepoRow({ api, projectId, repo, canEdit, isApp }: { api: PluginApiClient; projectId: string; repo: LinkedRepository; canEdit: boolean; isApp: boolean }) {
  const queryClient = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const unlink = useMutation({
    mutationFn: () => unlinkRepository(api, repo.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: linkedReposKey(projectId) });
      setConfirmOpen(false);
    },
  });

  return (
    <>
      <div className="ghp-item">
        <Avatar name={repo.repo_name} />
        <div className="ghp-grow">
          <p className="ghp-item-title ghp-truncate">
            <a href={`https://github.com/${repo.full_name}`} target="_blank" rel="noopener noreferrer">
              {repo.full_name}
            </a>
          </p>
          <p className="ghp-muted ghp-small">
            Default branch <code>{repo.default_branch}</code>
          </p>
        </div>
        <Badge tone={repo.webhook_active ? "success" : "warning"} dot>
          {repo.webhook_active ? "Syncing" : "Not syncing"}
        </Badge>
        {canEdit && (
          <Button variant="ghost" size="sm" icon className="ghp-btn-danger-quiet" aria-label={`Unlink ${repo.full_name}`} onClick={() => setConfirmOpen(true)}>
            <Unlink size={14} aria-hidden="true" />
          </Button>
        )}
      </div>

      <Modal
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        title="Unlink repository?"
        description={`${repo.full_name} will no longer be tracked in this project.${isApp ? "" : " Paca will also try to delete its webhook from GitHub."} Existing task links to its branches and pull requests are kept.`}
        footer={
          <>
            <Button variant="secondary" onClick={() => setConfirmOpen(false)} disabled={unlink.isPending}>
              Cancel
            </Button>
            <Button variant="danger" loading={unlink.isPending} onClick={() => unlink.mutate()}>
              Unlink
            </Button>
          </>
        }
      >
        {unlink.isError && <Banner tone="danger">Couldn’t unlink. Try again.</Banner>}
      </Modal>
    </>
  );
}

// ── Panel ─────────────────────────────────────────────────────────────────────

export function RepositoriesPanel({
  api,
  projectId,
  repos,
  loading,
  canEdit,
  isApp,
  autoOpen,
}: {
  api: PluginApiClient;
  projectId: string;
  repos: LinkedRepository[];
  loading: boolean;
  canEdit: boolean;
  isApp: boolean;
  /** Open the add dialog straight away (right after connecting). */
  autoOpen?: boolean;
}) {
  const [addOpen, setAddOpen] = useState(!!autoOpen);
  const [filter, setFilter] = useState("");
  const shown = repos.filter((r) => r.full_name.toLowerCase().includes(filter.trim().toLowerCase()));

  return (
    <div className="ghp-stack">
      <div className="ghp-toolbar">
        {repos.length > 5 ? (
          <div className="ghp-input-wrap">
            <Search size={14} aria-hidden="true" />
            <Input aria-label="Filter linked repositories" placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} />
          </div>
        ) : (
          <p className="ghp-muted">Pull requests and branches from these repositories can be linked to tasks.</p>
        )}
        {canEdit && repos.length > 0 && (
          <Button variant="primary" size="sm" onClick={() => setAddOpen(true)}>
            <Plus size={14} aria-hidden="true" />
            Add repository
          </Button>
        )}
      </div>

      {loading ? (
        <div className="ghp-stack" style={{ gap: 8 }}>
          <Skeleton /> <Skeleton />
        </div>
      ) : repos.length === 0 ? (
        <EmptyState
          icon={<GitBranch size={20} />}
          title="No repositories linked yet"
          action={
            canEdit ? (
              <Button variant="primary" onClick={() => setAddOpen(true)}>
                <Plus size={14} aria-hidden="true" />
                Add your first repository
              </Button>
            ) : undefined
          }
        >
          Link a repository to track its pull requests and create branches straight from tasks.
        </EmptyState>
      ) : shown.length === 0 ? (
        <p className="ghp-muted" style={{ textAlign: "center", padding: 24 }}>
          No linked repositories match “{filter}”.
        </p>
      ) : (
        <div className="ghp-list">
          {shown.map((repo) => (
            <LinkedRepoRow key={repo.id} api={api} projectId={projectId} repo={repo} canEdit={canEdit} isApp={isApp} />
          ))}
        </div>
      )}

      {canEdit && <AddRepoDialog api={api} projectId={projectId} isApp={isApp} open={addOpen} onClose={() => setAddOpen(false)} />}
    </div>
  );
}
