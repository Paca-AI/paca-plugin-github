import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Bot, CheckCircle2, KeyRound, TriangleAlert, Unplug } from "lucide-react";
import { useState } from "react";
import { SetupPanel } from "./SetupPanel";
import {
  accessibleReposKey,
  agentIdentitiesKey,
  deleteGitHubToken,
  type GitHubIntegration,
  integrationKey,
  type LinkedRepository,
  linkedReposKey,
} from "./github-api";
import { Badge, Banner, Button, Modal } from "./ui";

/**
 * Compact status for a connected project: what it authenticates as, whether
 * webhooks are flowing, and the few things you can do about it. A project on a
 * personal access token also sees a gentle nudge toward the GitHub App.
 */
export function ConnectionBar({
  api,
  projectId,
  integration,
  repos,
  canEdit,
}: {
  api: PluginApiClient;
  projectId: string;
  integration: GitHubIntegration;
  repos: LinkedRepository[];
  canEdit: boolean;
}) {
  const queryClient = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [upgradeOpen, setUpgradeOpen] = useState(false);
  const isApp = integration.auth_type === "app";

  const disconnect = useMutation({
    mutationFn: () => deleteGitHubToken(api),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: integrationKey(projectId) });
      for (const key of [linkedReposKey(projectId), accessibleReposKey(projectId), agentIdentitiesKey(projectId)]) {
        queryClient.removeQueries({ queryKey: key });
      }
      setConfirmOpen(false);
    },
  });

  // Webhook health is only meaningful once a repository is linked.
  const webhookKnown = repos.length > 0;
  const webhookOk = repos.every((r) => r.webhook_active);

  return (
    <>
      <div className="ghp-card ghp-conn">
        <div className="ghp-conn-icon">{isApp ? <Bot size={20} aria-hidden="true" /> : <KeyRound size={20} aria-hidden="true" />}</div>
        <div className="ghp-grow">
          <p className="ghp-conn-title">
            {isApp ? `${integration.app_name ?? "GitHub App"}${integration.account_login ? ` on ${integration.account_login}` : ""}` : "Personal access token"}
            <Badge tone={isApp ? "primary" : "neutral"}>{isApp ? "GitHub App" : "Token"}</Badge>
          </p>
          <p className="ghp-muted ghp-small">
            {isApp ? "Short-lived tokens are minted automatically. No personal credentials are stored." : "Actions are attributed to the token’s owner. The token is stored encrypted."}
          </p>
        </div>
        {webhookKnown && (
          <Badge tone={webhookOk ? "success" : "warning"} dot>
            {webhookOk ? "Webhooks active" : "Webhooks off"}
          </Badge>
        )}
        {canEdit && (
          <Button variant="secondary" size="sm" onClick={() => setConfirmOpen(true)}>
            <Unplug size={14} aria-hidden="true" />
            Disconnect
          </Button>
        )}
      </div>

      {webhookKnown && !webhookOk && (
        <Banner tone="warning" icon={<TriangleAlert size={16} aria-hidden="true" />} title="Pull request status won’t sync automatically">
          GitHub can’t reach this Paca instance, so webhook events aren’t delivered. Set <code>PUBLIC_URL</code> to a public HTTPS address (or use a tunnel), then reconnect.
        </Banner>
      )}

      {!isApp && canEdit && (
        <Banner
          tone="neutral"
          icon={<CheckCircle2 size={16} aria-hidden="true" />}
          title="Upgrade to a GitHub App"
          action={
            <Button variant="secondary" size="sm" onClick={() => setUpgradeOpen(true)}>
              Set up
            </Button>
          }
        >
          Apps act as their own bot, refresh their own tokens and let each agent have its own identity. You’ll need to unlink repositories to switch.
        </Banner>
      )}

      <Modal
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        title={isApp ? "Disconnect GitHub App?" : "Remove personal access token?"}
        description={
          isApp
            ? "This unlinks every repository and agent identity from the project. The app itself stays installed on GitHub."
            : "This unlinks every repository from the project and removes the webhooks Paca created on GitHub. This can’t be undone."
        }
        footer={
          <>
            <Button variant="secondary" onClick={() => setConfirmOpen(false)} disabled={disconnect.isPending}>
              Cancel
            </Button>
            <Button variant="danger" loading={disconnect.isPending} onClick={() => disconnect.mutate()}>
              {isApp ? "Disconnect" : "Remove token"}
            </Button>
          </>
        }
      >
        {disconnect.isError && (
          <Banner tone="danger">Something went wrong. Try again.</Banner>
        )}
      </Modal>

      <Modal open={upgradeOpen} onClose={() => setUpgradeOpen(false)} title="Switch to a GitHub App" wide>
        <SetupPanel api={api} projectId={projectId} canEdit={canEdit} variant="modal" onConnected={() => setUpgradeOpen(false)} />
      </Modal>
    </>
  );
}
