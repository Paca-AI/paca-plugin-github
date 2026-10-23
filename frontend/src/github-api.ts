import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";

const PLUGIN_ID = "com.paca.github";

// ── Error codes ────────────────────────────────────────────────────────────────

export const ErrorCode = {
  GitHubIntegrationNotFound: "GITHUB_INTEGRATION_NOT_FOUND",
  GitHubRepositoryNotFound: "GITHUB_REPOSITORY_NOT_FOUND",
  GitHubPRNotFound: "GITHUB_PR_NOT_FOUND",
  GitHubPRAlreadyLinked: "GITHUB_PR_ALREADY_LINKED",
  GitHubInvalidToken: "GITHUB_INVALID_TOKEN",
  GitHubRepoNotAccessible: "GITHUB_REPO_NOT_ACCESSIBLE",
  GitHubRepoAlreadyLinked: "GITHUB_REPO_ALREADY_LINKED",
  GitHubWebhookCreationFailed: "GITHUB_WEBHOOK_CREATION_FAILED",
  GitHubWebhookURLNotPublic: "GITHUB_WEBHOOK_URL_NOT_PUBLIC",
  GitHubBranchAlreadyLinked: "GITHUB_BRANCH_ALREADY_LINKED",
  GitHubBranchNotFound: "GITHUB_BRANCH_NOT_FOUND",
  GitHubTokenInsufficientPermissions: "GITHUB_TOKEN_INSUFFICIENT_PERMISSIONS",
  GitHubInstallationNotAllowed: "GITHUB_INSTALLATION_NOT_ALLOWED",
  GitHubAgentIdentityUnavailable: "GITHUB_AGENT_IDENTITY_UNAVAILABLE",
  GitHubUnlinkReposFirst: "GITHUB_UNLINK_REPOS_FIRST",
  GitHubWebhookURLRequired: "GITHUB_WEBHOOK_URL_REQUIRED",
  GitHubSetupInvalidState: "GITHUB_SETUP_INVALID_STATE",
  GitHubSetupMissingParameter: "GITHUB_SETUP_MISSING_PARAMETER",
  GitHubSetupConversionFailed: "GITHUB_SETUP_CONVERSION_FAILED",
  GitHubSetupStoreFailed: "GITHUB_SETUP_STORE_FAILED",
  GitHubSetupInstallationFailed: "GITHUB_SETUP_INSTALLATION_FAILED",
  BadRequest: "BAD_REQUEST",
} as const;

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/**
 * Extracts the error_code field from a PluginApiClient error.
 * The React SDK throws: `[PluginApiClient] METHOD URL → STATUS: BODY`
 * where BODY is the JSON from the plugin backend.
 */
export function getPluginErrorCode(err: unknown): ErrorCodeValue | null {
  if (!(err instanceof Error)) return null;
  const arrowIdx = err.message.lastIndexOf("→ ");
  if (arrowIdx === -1) return null;
  const rest = err.message.slice(arrowIdx + 2);
  const colonIdx = rest.indexOf(": ");
  if (colonIdx === -1) return null;
  const maybeJson = rest.slice(colonIdx + 2);
  try {
    const body = JSON.parse(maybeJson) as { error_code?: string };
    const code = body.error_code;
    if (!code) return null;
    const known = Object.values(ErrorCode) as string[];
    return known.includes(code) ? (code as ErrorCodeValue) : null;
  } catch {
    return null;
  }
}

// ── Domain types ───────────────────────────────────────────────────────────────

export interface GitHubIntegration {
  id?: string;
  project_id: string;
  connected: boolean;
  created_at?: string;
  updated_at?: string;
  /** "pat" or "app"; for "app" the fields below describe the installation. */
  auth_type?: "pat" | "app";
  installation_id?: string;
  app_name?: string;
  account_login?: string;
}

/** An app installation a project may bind (no secrets, no other projects). */
export interface ProjectInstallation {
  id: string;
  app_name: string;
  account_login: string;
  account_type: string;
  repository_selection: string;
}

export interface AppInstallation {
  id: string;
  installation_id: number;
  account_login: string;
  account_type: string;
  repository_selection: string;
  allow_all_projects: boolean;
  project_ids: string[];
  suspended: boolean;
  manage_url: string;
}

export interface GitHubApp {
  id: string;
  github_app_id: number;
  slug: string;
  name: string;
  owner_login: string;
  html_url: string;
  webhook_active: boolean;
  created_at: string;
  installations: AppInstallation[];
}

export interface ManifestResponse {
  post_url: string;
  state: string;
  manifest: Record<string, unknown>;
  webhook_active: boolean;
}

export interface AccessibleRepo {
  full_name: string;
  owner: string;
  repo_name: string;
  default_branch: string;
  private: boolean;
  description: string;
}

export interface LinkedRepository {
  id: string;
  project_id: string;
  owner: string;
  repo_name: string;
  full_name: string;
  default_branch: string;
  clone_url: string;
  webhook_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface PullRequest {
  id: string;
  project_id: string;
  repo_id: string;
  pr_number: number;
  github_pr_id: number;
  title: string;
  state: "open" | "closed" | "merged";
  html_url: string;
  head_branch: string;
  base_branch: string;
  author: string;
  merged_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface TaskBranch {
  id: string;
  task_id: string;
  repo_id: string;
  branch_name: string;
  created_at: string;
}

export interface CreateBranchResult {
  branch_name: string;
}

// ── Query key factories ────────────────────────────────────────────────────────

export const integrationKey = (projectId: string) =>
  [PLUGIN_ID, "integration", projectId] as const;

export const projectInstallationsKey = (projectId: string) =>
  [PLUGIN_ID, "project-installations", projectId] as const;

export const agentIdentitiesKey = (projectId: string) =>
  [PLUGIN_ID, "agent-identities", projectId] as const;

export const appsKey = () => [PLUGIN_ID, "apps"] as const;

export const linkedReposKey = (projectId: string) =>
  [PLUGIN_ID, "linked-repos", projectId] as const;

export const accessibleReposKey = (projectId: string) =>
  [PLUGIN_ID, "accessible-repos", projectId] as const;

export const taskPRsKey = (projectId: string, taskId: string) =>
  [PLUGIN_ID, "prs", projectId, taskId] as const;

export const taskBranchesKey = (projectId: string, taskId: string) =>
  [PLUGIN_ID, "branches", projectId, taskId] as const;

// ── API functions ──────────────────────────────────────────────────────────────

export async function getGitHubIntegration(
  api: PluginApiClient,
): Promise<GitHubIntegration> {
  return api.pluginGet<GitHubIntegration>(PLUGIN_ID, `/projects/${api.projectId}/integration`);
}

export async function setGitHubToken(
  api: PluginApiClient,
  token: string,
): Promise<GitHubIntegration> {
  return api.pluginPost<GitHubIntegration>(PLUGIN_ID, `/projects/${api.projectId}/integration/token`, {
    token,
  });
}

export async function deleteGitHubToken(api: PluginApiClient): Promise<void> {
  return api.pluginDelete(PLUGIN_ID, `/projects/${api.projectId}/integration/token`);
}

export async function listAccessibleRepos(
  api: PluginApiClient,
): Promise<AccessibleRepo[]> {
  return api.pluginGet<AccessibleRepo[]>(PLUGIN_ID, `/projects/${api.projectId}/integration/accessible-repos`);
}

export async function linkRepository(
  api: PluginApiClient,
  owner: string,
  repoName: string,
): Promise<LinkedRepository> {
  return api.pluginPost<LinkedRepository>(
    PLUGIN_ID,
    `/projects/${api.projectId}/repositories`,
    { owner, repo_name: repoName },
  );
}

export async function listLinkedRepositories(
  api: PluginApiClient,
): Promise<LinkedRepository[]> {
  return api.pluginGet<LinkedRepository[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/repositories`,
  );
}

export async function unlinkRepository(
  api: PluginApiClient,
  repoId: string,
): Promise<void> {
  return api.pluginDelete(
    PLUGIN_ID,
    `/projects/${api.projectId}/repositories/${repoId}`,
  );
}

export async function listTaskPRs(
  api: PluginApiClient,
  taskId: string,
): Promise<PullRequest[]> {
  return api.pluginGet<PullRequest[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/pull-requests`,
  );
}

export async function linkPRToTask(
  api: PluginApiClient,
  taskId: string,
  repoId: string,
  prNumber: number,
): Promise<PullRequest> {
  return api.pluginPost<PullRequest>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/pull-requests/link`,
    { repo_id: repoId, pr_number: prNumber },
  );
}

export async function unlinkPRFromTask(
  api: PluginApiClient,
  taskId: string,
  prId: string,
): Promise<void> {
  return api.pluginDelete(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/pull-requests/${prId}`,
  );
}

export async function listTaskBranches(
  api: PluginApiClient,
  taskId: string,
): Promise<TaskBranch[]> {
  return api.pluginGet<TaskBranch[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/branches`,
  );
}

export async function createBranch(
  api: PluginApiClient,
  taskId: string,
  repoId: string,
  branchName: string,
  sourceBranch?: string,
): Promise<CreateBranchResult> {
  return api.pluginPost<CreateBranchResult>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/branches`,
    { repo_id: repoId, branch_name: branchName, source_branch: sourceBranch },
  );
}

export async function linkBranchToTask(
  api: PluginApiClient,
  taskId: string,
  repoId: string,
  branchName: string,
): Promise<TaskBranch> {
  return api.pluginPost<TaskBranch>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/branches/link`,
    { repo_id: repoId, branch_name: branchName },
  );
}

// ── GitHub Apps (instance-level) ───────────────────────────────────────────────

export async function listProjectInstallations(
  api: PluginApiClient,
): Promise<ProjectInstallation[]> {
  return api.pluginGet<ProjectInstallation[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/integration/installations`,
  );
}

export async function bindInstallation(
  api: PluginApiClient,
  installationId: string,
): Promise<GitHubIntegration> {
  return api.pluginPost<GitHubIntegration>(
    PLUGIN_ID,
    `/projects/${api.projectId}/integration/app`,
    { installation_id: installationId },
  );
}

/** Requires the instance-wide github:manage_apps action; 403 otherwise. */
export async function listGitHubApps(api: PluginApiClient): Promise<GitHubApp[]> {
  return api.pluginGet<GitHubApp[]>(PLUGIN_ID, "/apps");
}

export async function createAppManifest(
  api: PluginApiClient,
  body: {
    name: string;
    owner_org?: string;
    allow_all_projects: boolean;
    public?: boolean;
    return_url: string;
  },
): Promise<ManifestResponse> {
  return api.pluginPost<ManifestResponse>(PLUGIN_ID, "/apps/manifest", body);
}

export async function startAppInstall(
  api: PluginApiClient,
  appId: string,
  returnUrl: string,
): Promise<{ url: string }> {
  return api.pluginPost<{ url: string }>(PLUGIN_ID, `/apps/${appId}/install`, {
    return_url: returnUrl,
  });
}

export async function setInstallationAccess(
  api: PluginApiClient,
  appId: string,
  installationId: string,
  allowAllProjects: boolean,
  projectIds: string[],
): Promise<void> {
  await api.pluginPost(
    PLUGIN_ID,
    `/apps/${appId}/installations/${installationId}/access`,
    { allow_all_projects: allowAllProjects, project_ids: projectIds },
  );
}

export async function deleteGitHubApp(api: PluginApiClient, appId: string): Promise<void> {
  return api.pluginDelete(PLUGIN_ID, `/apps/${appId}`);
}

// ── Per-agent identities ───────────────────────────────────────────────────────

export interface AgentIdentity {
  member_id: string;
  installation_id: string;
  app_name: string;
  account_login: string;
}

export async function listAgentIdentities(api: PluginApiClient): Promise<AgentIdentity[]> {
  return api.pluginGet<AgentIdentity[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/integration/agent-identities`,
  );
}

export async function setAgentIdentity(
  api: PluginApiClient,
  memberId: string,
  installationId: string,
): Promise<AgentIdentity[]> {
  return api.pluginPost<AgentIdentity[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/integration/agent-identities`,
    { member_id: memberId, installation_id: installationId },
  );
}

export async function deleteAgentIdentity(api: PluginApiClient, memberId: string): Promise<void> {
  return api.pluginDelete(
    PLUGIN_ID,
    `/projects/${api.projectId}/integration/agent-identities/${memberId}`,
  );
}

// ── Project members (host API) ─────────────────────────────────────────────────

export interface MemberLite {
  id: string;
  name: string;
  memberType?: string;
}

/**
 * Reads the host's project-member list directly. The SDK's own listMembers()
 * predates the paginated endpoint, so it can't filter by type or id.
 */
export async function fetchMembers(
  projectId: string,
  opts: { memberType?: string; ids?: string[]; pageSize?: number } = {},
): Promise<MemberLite[]> {
  const params = new URLSearchParams({ page_size: String(opts.pageSize ?? 100) });
  if (opts.memberType) params.set("member_type", opts.memberType);
  if (opts.ids?.length) params.set("ids", opts.ids.join(","));
  const res = await window.fetch(
    `${window.location.origin}/api/v1/projects/${projectId}/members?${params}`,
    { credentials: "include" },
  );
  if (!res.ok) throw new Error(`members request failed: ${res.status}`);
  const json = (await res.json()) as {
    data?: {
      items?: {
        id: string;
        username: string;
        full_name: string;
        agent_name?: string;
        member_type?: string;
      }[];
    };
  };
  return (json.data?.items ?? []).map((m) => ({
    id: m.id,
    name: m.agent_name || m.full_name || m.username,
    memberType: m.member_type,
  }));
}

// ── GitHub App setup callbacks ─────────────────────────────────────────────────

export interface SetupCallbackResult {
  /** GitHub URL for the next leg of the flow, when there is one. */
  next_url?: string;
  /** The Paca page that started the flow. */
  return_url: string;
}

export async function completeAppCreated(
  api: PluginApiClient,
  body: { code: string; state: string },
): Promise<SetupCallbackResult> {
  return api.pluginPost<SetupCallbackResult>(PLUGIN_ID, "/apps/callback/created", body);
}

export async function completeAppInstalled(
  api: PluginApiClient,
  body: { installation_id: string; state: string },
): Promise<SetupCallbackResult> {
  return api.pluginPost<SetupCallbackResult>(PLUGIN_ID, "/apps/callback/installed", body);
}
