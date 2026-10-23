import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Bot, Trash2, UserRound } from "lucide-react";
import { useState } from "react";
import {
  agentIdentitiesKey,
  deleteAgentIdentity,
  ErrorCode,
  fetchMembers,
  getPluginErrorCode,
  listAgentIdentities,
  listProjectInstallations,
  projectInstallationsKey,
  setAgentIdentity,
} from "./github-api";
import { Avatar, Banner, Button, EmptyState, Field, Select, Skeleton } from "./ui";

/**
 * Maps project members (typically AI agents) to their own GitHub App
 * installation, so each acts on GitHub as its own bot. Members without a
 * mapping use the project's default connection.
 */
export function AgentIdentitiesPanel({ api, projectId, canEdit }: { api: PluginApiClient; projectId: string; canEdit: boolean }) {
  const queryClient = useQueryClient();
  const [memberId, setMemberId] = useState("");
  const [installationId, setInstallationId] = useState("");
  const [error, setError] = useState<string | null>(null);

  const { data: identities = [], isLoading } = useQuery({
    queryKey: agentIdentitiesKey(projectId),
    queryFn: () => listAgentIdentities(api),
    retry: false,
  });
  const { data: installations = [] } = useQuery({
    queryKey: projectInstallationsKey(projectId),
    queryFn: () => listProjectInstallations(api),
    retry: false,
  });
  const { data: members } = useQuery({
    queryKey: [projectId, "github-agent-members"],
    queryFn: () => fetchMembers(projectId, { memberType: "agent" }),
    retry: false,
  });

  // Names for mapped members are looked up by id so they resolve even when the
  // member is not in the picker's current search page.
  const mappedIds = identities.map((i) => i.member_id);
  const { data: mapped, isLoading: mappedLoading } = useQuery({
    queryKey: [projectId, "github-mapped-members", mappedIds.join(",")],
    queryFn: () => fetchMembers(projectId, { ids: mappedIds }),
    enabled: mappedIds.length > 0,
    retry: false,
  });
  const nameOf = (id: string) => {
    const m = mapped?.find((x) => x.id === id) ?? members?.find((x) => x.id === id);
    if (m) return m.name;
    return mappedLoading ? "…" : "Former member";
  };

  const refresh = () => queryClient.invalidateQueries({ queryKey: agentIdentitiesKey(projectId) });
  const save = useMutation({
    mutationFn: (v: { member: string; installation: string }) => setAgentIdentity(api, v.member, v.installation),
    onSuccess: async () => {
      setMemberId("");
      setError(null);
      await refresh();
    },
    onError: (err: unknown) => {
      setError(
        getPluginErrorCode(err) === ErrorCode.GitHubInstallationNotAllowed
          ? "That identity isn’t available to this project."
          : "GitHub rejected this identity. Check the app is still installed.",
      );
    },
  });
  const remove = useMutation({ mutationFn: (id: string) => deleteAgentIdentity(api, id), onSuccess: refresh });

  if (isLoading) return <Skeleton height={120} />;

  return (
    <div className="ghp-stack">
      <div className="ghp-flow" aria-hidden="true">
        <span className="ghp-flow-chip">
          <UserRound size={13} />
          Developer agent
        </span>
        <ArrowRight size={14} />
        <span className="ghp-flow-chip">
          <Bot size={13} />
          dev-app[bot]
        </span>
        <span className="ghp-muted">opens the PR</span>
        <span style={{ flex: 1 }} />
        <span className="ghp-flow-chip">
          <UserRound size={13} />
          Reviewer agent
        </span>
        <ArrowRight size={14} />
        <span className="ghp-flow-chip">
          <Bot size={13} />
          review-app[bot]
        </span>
        <span className="ghp-muted">approves it</span>
      </div>
      <p className="ghp-muted">
        Give an agent its own GitHub App so its commits, pull requests and reviews are attributed to that app’s bot — which is what lets one agent formally approve another’s work. Agents without an identity use the project’s connection.
      </p>

      {identities.length === 0 ? (
        <EmptyState icon={<Bot size={20} />} title="No agent identities yet">
          Assign an identity below. You can create more GitHub Apps from the GitHub Apps tab.
        </EmptyState>
      ) : (
        <div className="ghp-list">
          {identities.map((i) => (
            <div key={i.member_id} className="ghp-item">
              <Avatar name={nameOf(i.member_id)} round />
              <div className="ghp-grow">
                <p className="ghp-item-title ghp-truncate">{nameOf(i.member_id)}</p>
                <p className="ghp-muted ghp-small ghp-truncate">
                  acts as <strong>{i.app_name}</strong> on {i.account_login}
                </p>
              </div>
              {canEdit && (
                <Button variant="ghost" size="sm" icon className="ghp-btn-danger-quiet" aria-label={`Remove identity for ${nameOf(i.member_id)}`} disabled={remove.isPending} onClick={() => remove.mutate(i.member_id)}>
                  <Trash2 size={14} aria-hidden="true" />
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      {canEdit &&
        (installations.length === 0 ? (
          <Banner>No installations are available to this project yet.</Banner>
        ) : (
          <form
            className="ghp-card ghp-card-pad ghp-stack"
            onSubmit={(e) => {
              e.preventDefault();
              if (memberId && installationId) save.mutate({ member: memberId, installation: installationId });
            }}
          >
            <p className="ghp-section-title">Assign an identity</p>
            <div className="ghp-add-row">
              <Field label="Agent">
                {(id) => (
                  <Select id={id} value={memberId} onChange={(e) => setMemberId(e.target.value)}>
                    <option value="">Select an agent…</option>
                    {members?.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="Acts as">
                {(id) => (
                  <Select id={id} value={installationId} onChange={(e) => setInstallationId(e.target.value)}>
                    <option value="">Select an identity…</option>
                    {installations.map((inst) => (
                      <option key={inst.id} value={inst.id}>
                        {inst.app_name} · {inst.account_login}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Button type="submit" variant="primary" disabled={!memberId || !installationId} loading={save.isPending}>
                Assign
              </Button>
            </div>
            {error && <Banner tone="danger">{error}</Banner>}
          </form>
        ))}
    </div>
  );
}
