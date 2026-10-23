import { PluginApiClient, PluginQueryClientProvider } from "@paca-ai/plugin-sdk-react";
import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { BranchesSection } from "./Branches";
import { PullRequestsSection } from "./PullRequests";
import { type LinkedRepository, linkedReposKey, listLinkedRepositories } from "./github-api";
import "./styles.css";

interface GitHubTaskSectionProps {
  projectId: string;
  taskId: string;
  canEdit?: boolean;
}

function GitHubTaskSectionInner({ api, projectId, taskId, canEdit }: { api: PluginApiClient; projectId: string; taskId: string; canEdit: boolean }) {
  // Task and project metadata are only needed to suggest a branch name.
  const { data: task } = useQuery({ queryKey: ["plugin", "task", taskId], queryFn: () => api.getTask(taskId), staleTime: 60_000 });
  const { data: project } = useQuery({ queryKey: ["plugin", "project", projectId], queryFn: () => api.getProject(), staleTime: 60_000 });
  const { data: repos, isLoading } = useQuery<LinkedRepository[]>({
    queryKey: linkedReposKey(projectId),
    queryFn: () => listLinkedRepositories(api),
    retry: false,
    throwOnError: false,
  });

  // Nothing to show until the project has a linked repository.
  if (isLoading || !repos || repos.length === 0) return null;

  // Two sibling sections, not one wrapper: the task drawer spaces its sections
  // as direct children, so each of ours gets the same rhythm as the host's.
  return (
    <>
      <BranchesSection
        api={api}
        projectId={projectId}
        taskId={taskId}
        taskIdPrefix={project?.task_id_prefix ?? ""}
        taskNumber={task?.task_number ?? 0}
        taskTitle={task?.title}
        repos={repos}
        canEdit={canEdit}
      />
      <PullRequestsSection api={api} projectId={projectId} taskId={taskId} repos={repos} canEdit={canEdit} />
    </>
  );
}

export default function GitHubTaskSection({ projectId, taskId, canEdit = true }: GitHubTaskSectionProps) {
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
      <GitHubTaskSectionInner api={api} projectId={projectId} taskId={taskId} canEdit={canEdit} />
    </PluginQueryClientProvider>
  );
}
