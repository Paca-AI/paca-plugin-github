import { PluginApiClient, PluginQueryClientProvider } from "@paca-ai/plugin-sdk-react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, ChevronRight, X } from "lucide-react";
import { useMemo, useState } from "react";
import { AgentIdentitiesPanel } from "./AgentIdentities";
import { AppsPanel } from "./AppsPanel";
import { ConnectionBar } from "./ConnectionBar";
import { RepositoriesPanel } from "./RepositoriesPanel";
import { SetupPanel } from "./SetupPanel";
import { useFlowResult } from "./app-flow";
import {
  appsKey,
  type GitHubIntegration,
  getGitHubIntegration,
  integrationKey,
  type LinkedRepository,
  linkedReposKey,
  listGitHubApps,
  listLinkedRepositories,
} from "./github-api";
import { Banner, Button, GitHubIcon, Skeleton, TabPanel, Tabs } from "./ui";

function GitHubSettingsInner({ api, projectId, canEdit }: { api: PluginApiClient; projectId: string; canEdit: boolean }) {
  const [flow, dismissFlow] = useFlowResult();
  const [tab, setTab] = useState("repositories");
  const [justConnected, setJustConnected] = useState(false);
  const [appsOpen, setAppsOpen] = useState(false);

  const { data: integration, isLoading } = useQuery<GitHubIntegration | undefined>({
    queryKey: integrationKey(projectId),
    queryFn: () => getGitHubIntegration(api),
    retry: false,
    throwOnError: false,
  });
  const connected = integration?.connected === true;
  const isApp = connected && integration?.auth_type === "app";

  const { data: repos = [], isLoading: reposLoading } = useQuery<LinkedRepository[]>({
    queryKey: linkedReposKey(projectId),
    queryFn: () => listLinkedRepositories(api),
    retry: false,
    throwOnError: false,
    enabled: connected,
  });
  // Resolves only for holders of the instance-wide github:manage_apps action.
  const { data: apps } = useQuery({ queryKey: appsKey(), queryFn: () => listGitHubApps(api), retry: false });
  const isAdmin = apps !== undefined;

  const tabs = [
    ...(connected ? [{ id: "repositories", label: "Repositories", count: repos.length }] : []),
    ...(isApp ? [{ id: "identities", label: "Agent identities" }] : []),
    ...(isAdmin ? [{ id: "apps", label: "GitHub Apps", count: apps.length }] : []),
  ];
  const activeTab = tabs.some((t) => t.id === tab) ? tab : (tabs[0]?.id ?? "");

  const onConnected = () => {
    setJustConnected(true);
    setTab("repositories");
  };

  return (
    <div className="ghp">
      <header className="ghp-header">
        <div className="ghp-header-icon">
          <GitHubIcon width={22} height={22} />
        </div>
        <div>
          <h2 className="ghp-title">GitHub</h2>
          <p className="ghp-subtitle">Track pull requests, create branches from tasks and let agents work in your repositories.</p>
        </div>
      </header>

      {flow && (
        <Banner
          tone={flow.ok ? "success" : "danger"}
          icon={flow.ok ? <CheckCircle2 size={16} aria-hidden="true" /> : undefined}
          action={
            <Button variant="ghost" size="sm" icon aria-label="Dismiss" onClick={dismissFlow}>
              <X size={14} aria-hidden="true" />
            </Button>
          }
        >
          {flow.message}
        </Banner>
      )}

      {isLoading ? (
        <Skeleton height={200} />
      ) : !connected ? (
        <>
          <SetupPanel api={api} projectId={projectId} canEdit={canEdit} onConnected={onConnected} />
          {isAdmin && (
            <div>
              <button type="button" className="ghp-disclosure" aria-expanded={appsOpen} aria-controls="ghp-apps-body" onClick={() => setAppsOpen((v) => !v)}>
                <span className="ghp-row">Manage GitHub Apps ({apps.length})</span>
                <ChevronRight size={15} className="ghp-chev" data-open={appsOpen} aria-hidden="true" />
              </button>
              {appsOpen && (
                <div id="ghp-apps-body" className="ghp-disclosure-body">
                  <AppsPanel api={api} />
                </div>
              )}
            </div>
          )}
        </>
      ) : (
        <>
          <ConnectionBar api={api} projectId={projectId} integration={integration as GitHubIntegration} repos={repos} canEdit={canEdit} />
          <Tabs tabs={tabs} active={activeTab} onChange={setTab} />
          {activeTab === "repositories" && (
            <TabPanel id="repositories">
              <RepositoriesPanel api={api} projectId={projectId} repos={repos} loading={reposLoading} canEdit={canEdit} isApp={isApp} autoOpen={justConnected} />
            </TabPanel>
          )}
          {activeTab === "identities" && (
            <TabPanel id="identities">
              <AgentIdentitiesPanel api={api} projectId={projectId} canEdit={canEdit} />
            </TabPanel>
          )}
          {activeTab === "apps" && (
            <TabPanel id="apps">
              <AppsPanel api={api} />
            </TabPanel>
          )}
        </>
      )}
    </div>
  );
}

// ── Public export ─────────────────────────────────────────────────────────────

interface GitHubSettingsTabProps {
  projectId: string;
  canEdit?: boolean;
}

export default function GitHubSettingsTab({ projectId, canEdit = true }: GitHubSettingsTabProps) {
  const api = useMemo(
    () =>
      new PluginApiClient({
        baseUrl: `${window.location.origin}/api/v1`,
        projectId,
        fetch: (url, init) => window.fetch(url, { ...init, credentials: "include" }),
      }),
    [projectId],
  );

  return (
    <PluginQueryClientProvider>
      <GitHubSettingsInner api={api} projectId={projectId} canEdit={canEdit} />
    </PluginQueryClientProvider>
  );
}
