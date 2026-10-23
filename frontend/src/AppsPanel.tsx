import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, ExternalLink, Plus, Trash2, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { CreateAppDialog, rememberReturnUrl } from "./app-flow";
import {
  appsKey,
  deleteGitHubApp,
  type GitHubApp,
  listGitHubApps,
  setInstallationAccess,
  startAppInstall,
} from "./github-api";
import { Badge, Banner, Button, EmptyState, Modal, Skeleton, Switch } from "./ui";

/** Instance-wide management of GitHub Apps (requires github:manage_apps). */
export function AppsPanel({ api }: { api: PluginApiClient }) {
  const queryClient = useQueryClient();
  const { data: apps, isLoading } = useQuery<GitHubApp[]>({ queryKey: appsKey(), queryFn: () => listGitHubApps(api), retry: false });
  const [createOpen, setCreateOpen] = useState(false);
  const [deleting, setDeleting] = useState<GitHubApp | null>(null);

  const refresh = () => queryClient.invalidateQueries({ queryKey: appsKey() });
  const install = useMutation({
    mutationFn: (appId: string) => startAppInstall(api, appId, rememberReturnUrl()),
    onSuccess: ({ url }) => {
      window.location.href = url;
    },
  });
  const access = useMutation({
    mutationFn: (v: { app: string; inst: string; all: boolean; ids: string[] }) => setInstallationAccess(api, v.app, v.inst, v.all, v.ids),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (appId: string) => deleteGitHubApp(api, appId),
    onSuccess: async () => {
      setDeleting(null);
      await refresh();
    },
  });

  if (isLoading || !apps) return <Skeleton height={140} />;

  return (
    <div className="ghp-stack">
      <div className="ghp-toolbar">
        <p className="ghp-muted">Instance-wide. Projects connect through one of these installations.</p>
        {apps.length > 0 && (
          <Button variant="primary" size="sm" onClick={() => setCreateOpen(true)}>
            <Plus size={14} aria-hidden="true" />
            Create GitHub App
          </Button>
        )}
      </div>

      {apps.length === 0 ? (
        <EmptyState
          icon={<Bot size={20} />}
          title="No GitHub Apps yet"
          action={
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus size={14} aria-hidden="true" />
              Create GitHub App
            </Button>
          }
        >
          Create one in about a minute — Paca pre-fills GitHub for you.
        </EmptyState>
      ) : (
        apps.map((app) => (
          <section key={app.id} className="ghp-card ghp-app" aria-label={app.name}>
            <div className="ghp-app-head">
              <div className="ghp-row" style={{ minWidth: 0 }}>
                <span className="ghp-avatar" aria-hidden="true">
                  <Bot size={16} />
                </span>
                <div style={{ minWidth: 0 }}>
                  <p className="ghp-strong ghp-truncate">{app.name}</p>
                  <a className="ghp-muted ghp-small" href={app.html_url} target="_blank" rel="noopener noreferrer" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                    {app.owner_login || "View on GitHub"}
                    <ExternalLink size={11} aria-hidden="true" />
                  </a>
                </div>
              </div>
              <div className="ghp-row">
                <Button variant="secondary" size="sm" loading={install.isPending && install.variables === app.id} onClick={() => install.mutate(app.id)}>
                  Install on an account
                </Button>
                <Button variant="ghost" size="sm" icon className="ghp-btn-danger-quiet" aria-label={`Delete ${app.name}`} onClick={() => setDeleting(app)}>
                  <Trash2 size={14} aria-hidden="true" />
                </Button>
              </div>
            </div>

            {!app.webhook_active && (
              <div style={{ padding: "0 20px 16px" }}>
                <Banner tone="warning" icon={<TriangleAlert size={16} aria-hidden="true" />}>
                  <span className="ghp-small">
                    <code>PUBLIC_URL</code> isn’t reachable from GitHub, so this app delivers no webhooks — pull request status won’t update automatically.
                  </span>
                </Banner>
              </div>
            )}

            <div className="ghp-app-body">
              {app.installations.length === 0 ? (
                <div className="ghp-item">
                  <p className="ghp-muted ghp-grow">Not installed on any account yet.</p>
                  <Button variant="primary" size="sm" onClick={() => install.mutate(app.id)}>
                    Install now
                  </Button>
                </div>
              ) : (
                app.installations.map((inst) => (
                  <div key={inst.id} className="ghp-item">
                    <div className="ghp-grow">
                      <p className="ghp-item-title ghp-truncate">
                        <a href={inst.manage_url} target="_blank" rel="noopener noreferrer">
                          {inst.account_login}
                        </a>{" "}
                        {inst.suspended && <Badge tone="danger">Suspended</Badge>}
                      </p>
                      <p className="ghp-muted ghp-small">
                        {inst.repository_selection === "all" ? "All repositories" : "Selected repositories"}
                        {!inst.allow_all_projects && ` · ${inst.project_ids.length} project${inst.project_ids.length === 1 ? "" : "s"} allowed`}
                      </p>
                    </div>
                    <span className="ghp-row ghp-small ghp-muted">
                      Every project
                      <Switch label={`Make ${inst.account_login} available to every project`} checked={inst.allow_all_projects} disabled={access.isPending} onChange={(all) => access.mutate({ app: app.id, inst: inst.id, all, ids: inst.project_ids })} />
                    </span>
                  </div>
                ))
              )}
            </div>
          </section>
        ))
      )}

      <CreateAppDialog api={api} open={createOpen} onClose={() => setCreateOpen(false)} />

      <Modal
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? "app"}?`}
        description="Projects connected through this app lose their GitHub connection. This removes the app from Paca only — delete it on GitHub too to revoke its access."
        footer={
          <>
            <Button variant="secondary" onClick={() => setDeleting(null)} disabled={remove.isPending}>
              Cancel
            </Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleting && remove.mutate(deleting.id)}>
              Delete app
            </Button>
          </>
        }
      />
    </div>
  );
}
