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
  GitHubTokenInsufficientPermissions: "GITHUB_TOKEN_INSUFFICIENT_PERMISSIONS",
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
  integration_id: string;
  owner: string;
  repo_name: string;
  full_name: string;
  default_branch: string;
  webhook_id: number;
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
  return api.pluginGet<GitHubIntegration>(PLUGIN_ID, `/projects/${api.projectId}/github`);
}

export async function setGitHubToken(
  api: PluginApiClient,
  token: string,
): Promise<GitHubIntegration> {
  return api.pluginPost<GitHubIntegration>(PLUGIN_ID, `/projects/${api.projectId}/github/token`, {
    token,
  });
}

export async function deleteGitHubToken(api: PluginApiClient): Promise<void> {
  return api.pluginDelete(PLUGIN_ID, `/projects/${api.projectId}/github/token`);
}

export async function listAccessibleRepos(
  api: PluginApiClient,
): Promise<AccessibleRepo[]> {
  return api.pluginGet<AccessibleRepo[]>(PLUGIN_ID, `/projects/${api.projectId}/github/repositories`);
}

export async function linkRepository(
  api: PluginApiClient,
  owner: string,
  repoName: string,
): Promise<LinkedRepository> {
  return api.pluginPost<LinkedRepository>(
    PLUGIN_ID,
    `/projects/${api.projectId}/github/linked-repositories`,
    { owner, repo_name: repoName },
  );
}

export async function listLinkedRepositories(
  api: PluginApiClient,
): Promise<LinkedRepository[]> {
  return api.pluginGet<LinkedRepository[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/github/linked-repositories`,
  );
}

export async function unlinkRepository(
  api: PluginApiClient,
  repoId: string,
): Promise<void> {
  return api.pluginDelete(
    PLUGIN_ID,
    `/projects/${api.projectId}/github/linked-repositories/${repoId}`,
  );
}

export async function listTaskPRs(
  api: PluginApiClient,
  taskId: string,
): Promise<PullRequest[]> {
  return api.pluginGet<PullRequest[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/github/pull-requests`,
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
    `/projects/${api.projectId}/tasks/${taskId}/github/pull-requests`,
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
    `/projects/${api.projectId}/tasks/${taskId}/github/pull-requests/${prId}`,
  );
}

export async function listTaskBranches(
  api: PluginApiClient,
  taskId: string,
): Promise<TaskBranch[]> {
  return api.pluginGet<TaskBranch[]>(
    PLUGIN_ID,
    `/projects/${api.projectId}/tasks/${taskId}/github/branches`,
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
    `/projects/${api.projectId}/tasks/${taskId}/github/branches`,
    { repo_id: repoId, branch_name: branchName, source_branch: sourceBranch },
  );
}
