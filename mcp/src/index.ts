import {
	PluginAPIClient,
	type PluginMCPContext,
	type PluginMCPEntry,
	type Tool,
	errorResult,
	textResult,
} from "@paca-ai/plugin-sdk-mcp";

// ── Domain types ──────────────────────────────────────────────────────────────

interface GitHubIntegration {
	id: string;
	project_id: string;
	created_at: string;
	updated_at: string;
}

interface LinkedRepository {
	id: string;
	project_id: string;
	owner: string;
	repo_name: string;
	full_name: string;
	private: boolean;
	default_branch: string;
	webhook_id: number | null;
	created_at: string;
}

interface AccessibleRepo {
	id: number;
	owner: string;
	repo_name: string;
	full_name: string;
	private: boolean;
	default_branch: string;
}

interface PullRequest {
	id: string;
	task_id: string;
	repo_id: string;
	pr_number: number;
	title: string;
	state: "open" | "closed" | "merged";
	html_url: string;
	head_branch: string;
	author: string | null;
	merged_at: string | null;
	created_at: string;
}

interface TaskBranch {
	id: string;
	task_id: string;
	repo_id: string;
	branch_name: string;
	created_at: string;
}

interface CreateBranchResult {
	branch_name: string;
	repo_full_name: string;
	html_url: string;
}

// ── Formatting helpers ────────────────────────────────────────────────────────

function formatIntegration(integration: GitHubIntegration): string {
	return `GitHub Integration:
ID: ${integration.id}
Project ID: ${integration.project_id}
Created: ${integration.created_at}
Updated: ${integration.updated_at}`;
}

function formatLinkedRepo(repo: LinkedRepository): string {
	return `Repository: ${repo.full_name}
ID: ${repo.id}
Owner: ${repo.owner}
Repo Name: ${repo.repo_name}
Full Name: ${repo.full_name}
Private: ${repo.private ? "Yes" : "No"}
Default Branch: ${repo.default_branch}
Webhook ID: ${repo.webhook_id ?? "None"}
Created: ${repo.created_at}`;
}

function formatAccessibleRepo(repo: AccessibleRepo): string {
	return `Repository: ${repo.full_name}
ID: ${repo.id}
Owner: ${repo.owner}
Private: ${repo.private ? "Yes" : "No"}
Default Branch: ${repo.default_branch}`;
}

function formatPullRequest(pr: PullRequest): string {
	return `Pull Request: #${pr.pr_number} - ${pr.title}
ID: ${pr.id}
State: ${pr.state}
Author: ${pr.author ?? "Unknown"}
URL: ${pr.html_url}
Head Branch: ${pr.head_branch}
Created: ${pr.created_at}
Merged: ${pr.merged_at ? `Yes (${pr.merged_at})` : "No"}`;
}

function formatBranch(branch: TaskBranch): string {
	return `Branch: ${branch.branch_name}
ID: ${branch.id}
Task ID: ${branch.task_id}
Repo ID: ${branch.repo_id}
Created: ${branch.created_at}`;
}

function formatList<T>(items: T[], formatter: (item: T) => string): string {
	if (items.length === 0) return "(none)";
	return items.map(formatter).join("\n\n---\n\n");
}

// ── Tool definitions ──────────────────────────────────────────────────────────

const UUID_DESC =
	"The technical UUID of the %s (e.g., '550e8400-e29b-41d4-a716-446655440000').";

const projectIdProp = {
	type: "string",
	description:
		UUID_DESC.replace("%s", "project") +
		" Use list_projects to get the project ID. Do NOT use the project name.",
};

const taskIdProp = {
	type: "string",
	description:
		UUID_DESC.replace("%s", "task") + " Use list_tasks to get the task ID.",
};

const tools: Tool[] = [
	// ── Integration ──────────────────────────────────────────────────────────
	{
		name: "github_get_integration",
		description: "Get GitHub integration status for a project.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
			},
			required: ["projectId"],
		},
	},
	{
		name: "github_set_token",
		description:
			"Set (or replace) the GitHub personal access token for a project. The token must have at least the 'repo' scope.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				token: {
					type: "string",
					description: "The GitHub personal access token (e.g., 'ghp_xxxx').",
				},
			},
			required: ["projectId", "token"],
		},
	},
	{
		name: "github_delete_token",
		description: "Delete the GitHub token for a project, removing GitHub integration.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
			},
			required: ["projectId"],
		},
	},
	// ── Repositories ─────────────────────────────────────────────────────────
	{
		name: "github_list_repositories",
		description:
			"List GitHub repositories accessible with the project's GitHub token. Use this before linking a repository.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
			},
			required: ["projectId"],
		},
	},
	{
		name: "github_list_linked_repos",
		description: "List GitHub repositories linked to a project.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
			},
			required: ["projectId"],
		},
	},
	{
		name: "github_link_repository",
		description:
			"Link a GitHub repository to a project. The repository must be accessible with the project's GitHub token.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				owner: {
					type: "string",
					description: "The repository owner (GitHub username or org).",
				},
				repo_name: {
					type: "string",
					description: "The repository name (not including the owner).",
				},
			},
			required: ["projectId", "owner", "repo_name"],
		},
	},
	{
		name: "github_unlink_repository",
		description: "Unlink a GitHub repository from a project.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				repoId: {
					type: "string",
					description:
						UUID_DESC.replace("%s", "linked repository") +
						" Use github_list_linked_repos to get the repo ID.",
				},
			},
			required: ["projectId", "repoId"],
		},
	},
	// ── Pull Requests ─────────────────────────────────────────────────────────
	{
		name: "github_list_task_prs",
		description: "List pull requests linked to a task.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				taskId: taskIdProp,
			},
			required: ["projectId", "taskId"],
		},
	},
	{
		name: "github_link_pr_to_task",
		description: "Link a GitHub pull request to a task.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				taskId: taskIdProp,
				repoId: {
					type: "string",
					description:
						UUID_DESC.replace("%s", "linked repository") +
						" Use github_list_linked_repos to get the repo ID.",
				},
				pr_number: {
					type: "number",
					description: "The GitHub pull request number (e.g., 42).",
				},
			},
			required: ["projectId", "taskId", "repoId", "pr_number"],
		},
	},
	{
		name: "github_unlink_pr_from_task",
		description: "Unlink a pull request from a task.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				taskId: taskIdProp,
				prId: {
					type: "string",
					description:
						UUID_DESC.replace("%s", "linked pull request") +
						" Use github_list_task_prs to get the PR ID.",
				},
			},
			required: ["projectId", "taskId", "prId"],
		},
	},
	// ── Branches ─────────────────────────────────────────────────────────────
	{
		name: "github_create_branch",
		description:
			"Create a new branch on GitHub for a task and link it to the task.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				taskId: taskIdProp,
				repoId: {
					type: "string",
					description:
						UUID_DESC.replace("%s", "linked repository") +
						" Use github_list_linked_repos to get the repo ID.",
				},
				branch_name: {
					type: "string",
					description:
						"The name for the new branch (e.g., 'feat/PROJ-42-add-feature').",
				},
				source_branch: {
					type: "string",
					description:
						"The source branch to branch from. Defaults to the repository's default branch (optional).",
				},
			},
			required: ["projectId", "taskId", "repoId", "branch_name"],
		},
	},
	{
		name: "github_list_task_branches",
		description: "List branches linked to a task.",
		inputSchema: {
			type: "object",
			properties: {
				projectId: projectIdProp,
				taskId: taskIdProp,
			},
			required: ["projectId", "taskId"],
		},
	},
];

// ── Entry ─────────────────────────────────────────────────────────────────────

const entry: PluginMCPEntry = {
	tools,

	async handleToolCall(
		name: string,
		args: Record<string, unknown>,
		context: PluginMCPContext,
	) {
		const api = new PluginAPIClient(context);

		try {
			switch (name) {
				// ── Integration ────────────────────────────────────────────────
				case "github_get_integration": {
					const { projectId } = args as { projectId: string };
					const integration = await api.pluginGet<GitHubIntegration>(
						`projects/${projectId}/github`,
					);
					return textResult(formatIntegration(integration));
				}

				case "github_set_token": {
					const { projectId, token } = args as {
						projectId: string;
						token: string;
					};
					const integration = await api.pluginPost<GitHubIntegration>(
						`projects/${projectId}/github/token`,
						{ token },
					);
					return textResult(
						`GitHub token set successfully:\n\n${formatIntegration(integration)}`,
					);
				}

				case "github_delete_token": {
					const { projectId } = args as { projectId: string };
					await api.pluginDelete(`projects/${projectId}/github/token`);
					return textResult("GitHub token deleted successfully.");
				}

				// ── Repositories ───────────────────────────────────────────────
				case "github_list_repositories": {
					const { projectId } = args as { projectId: string };
					const repos = await api.pluginGet<AccessibleRepo[]>(
						`projects/${projectId}/github/repositories`,
					);
					return textResult(
						`Accessible GitHub Repositories:\n\n${formatList(repos, formatAccessibleRepo)}`,
					);
				}

				case "github_list_linked_repos": {
					const { projectId } = args as { projectId: string };
					const repos = await api.pluginGet<LinkedRepository[]>(
						`projects/${projectId}/github/linked-repositories`,
					);
					return textResult(
						`Linked GitHub Repositories:\n\n${formatList(repos, formatLinkedRepo)}`,
					);
				}

				case "github_link_repository": {
					const { projectId, owner, repo_name } = args as {
						projectId: string;
						owner: string;
						repo_name: string;
					};
					const repo = await api.pluginPost<LinkedRepository>(
						`projects/${projectId}/github/linked-repositories`,
						{ owner, repo_name },
					);
					return textResult(
						`Repository linked successfully:\n\n${formatLinkedRepo(repo)}`,
					);
				}

				case "github_unlink_repository": {
					const { projectId, repoId } = args as {
						projectId: string;
						repoId: string;
					};
					await api.pluginDelete(
						`projects/${projectId}/github/linked-repositories/${repoId}`,
					);
					return textResult(`Repository ${repoId} unlinked successfully.`);
				}

				// ── Pull Requests ──────────────────────────────────────────────
				case "github_list_task_prs": {
					const { projectId, taskId } = args as {
						projectId: string;
						taskId: string;
					};
					const prs = await api.pluginGet<PullRequest[]>(
						`projects/${projectId}/tasks/${taskId}/github/pull-requests`,
					);
					return textResult(
						`Pull Requests:\n\n${formatList(prs, formatPullRequest)}`,
					);
				}

				case "github_link_pr_to_task": {
					const { projectId, taskId, repoId, pr_number } = args as {
						projectId: string;
						taskId: string;
						repoId: string;
						pr_number: number;
					};
					const pr = await api.pluginPost<PullRequest>(
						`projects/${projectId}/tasks/${taskId}/github/pull-requests`,
						{ repo_id: repoId, pr_number },
					);
					return textResult(
						`Pull request linked successfully:\n\n${formatPullRequest(pr)}`,
					);
				}

				case "github_unlink_pr_from_task": {
					const { projectId, taskId, prId } = args as {
						projectId: string;
						taskId: string;
						prId: string;
					};
					await api.pluginDelete(
						`projects/${projectId}/tasks/${taskId}/github/pull-requests/${prId}`,
					);
					return textResult(`Pull request ${prId} unlinked successfully.`);
				}

				// ── Branches ───────────────────────────────────────────────────
				case "github_create_branch": {
					const { projectId, taskId, repoId, branch_name, source_branch } =
						args as {
							projectId: string;
							taskId: string;
							repoId: string;
							branch_name: string;
							source_branch?: string;
						};
					const result = await api.pluginPost<CreateBranchResult>(
						`projects/${projectId}/tasks/${taskId}/github/branches`,
						{ repo_id: repoId, branch_name, source_branch },
					);
					return textResult(
						`Branch created successfully:\n\nBranch: ${result.branch_name}\nRepository: ${result.repo_full_name}\nURL: ${result.html_url}`,
					);
				}

				case "github_list_task_branches": {
					const { projectId, taskId } = args as {
						projectId: string;
						taskId: string;
					};
					const branches = await api.pluginGet<TaskBranch[]>(
						`projects/${projectId}/tasks/${taskId}/github/branches`,
					);
					return textResult(
						`Branches:\n\n${formatList(branches, formatBranch)}`,
					);
				}

				default:
					return errorResult(`Unknown tool: ${name}`);
			}
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			return errorResult(`GitHub plugin error: ${msg}`);
		}
	},
};

export default entry;
