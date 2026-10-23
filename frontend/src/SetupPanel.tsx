import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Check, ChevronRight, KeyRound, ShieldCheck, Sparkles } from "lucide-react";
import { useState } from "react";
import { CreateAppDialog, rememberReturnUrl } from "./app-flow";
import {
  appsKey,
  bindInstallation,
  ErrorCode,
  getPluginErrorCode,
  integrationKey,
  listGitHubApps,
  listProjectInstallations,
  projectInstallationsKey,
  setGitHubToken,
  setInstallationAccess,
  startAppInstall,
} from "./github-api";
import { Badge, Banner, Button, Input, Skeleton, cn } from "./ui";

type StepState = "done" | "current" | "todo";

function Steps({ steps }: { steps: { label: string; state: StepState }[] }) {
  return (
    <ol className="ghp-steps" aria-label="Setup progress">
      {steps.map((s, i) => (
        <li key={s.label} className="ghp-step" data-state={s.state} aria-current={s.state === "current" ? "step" : undefined}>
          <div className="ghp-step-top">
            <span className="ghp-step-num">{s.state === "done" ? <Check size={14} aria-hidden="true" /> : i + 1}</span>
            <span className="ghp-step-line" />
          </div>
          <span className="ghp-step-label">{s.label}</span>
        </li>
      ))}
    </ol>
  );
}

/**
 * First-run experience: connect the project to GitHub. The GitHub App path is
 * the recommended one and is shown as a three-step guided setup whose current
 * step is derived from what already exists (no app → create; app but no
 * installation → install; installation → connect). A personal access token is
 * available below as a quieter alternative.
 */
export function SetupPanel({
  api,
  projectId,
  canEdit,
  onConnected,
  variant = "page",
}: {
  api: PluginApiClient;
  projectId: string;
  canEdit: boolean;
  onConnected: () => void;
  /** "modal" omits the hero chrome (used when upgrading from a token). */
  variant?: "page" | "modal";
}) {
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [patOpen, setPatOpen] = useState(false);

  const { data: installations = [], isLoading } = useQuery({
    queryKey: projectInstallationsKey(projectId),
    queryFn: () => listProjectInstallations(api),
    retry: false,
  });
  // Resolves only for holders of the instance-wide github:manage_apps action.
  const { data: apps } = useQuery({ queryKey: appsKey(), queryFn: () => listGitHubApps(api), retry: false });
  const isAdmin = apps !== undefined;

  const hasInstall = installations.length > 0;
  const hasApp = hasInstall || (apps?.length ?? 0) > 0;
  const current = hasInstall ? 2 : hasApp ? 1 : 0;
  const state = (i: number): StepState => (i < current ? "done" : i === current ? "current" : "todo");

  // An installation exists but is not open to this project (admin-only fix).
  const closedInstall = apps?.flatMap((a) => a.installations.map((i) => ({ app: a, inst: i }))).find((x) => !x.inst.suspended);

  const bind = useMutation({
    mutationFn: (installationId: string) => bindInstallation(api, installationId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: integrationKey(projectId) });
      setError(null);
      onConnected();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubUnlinkReposFirst
          ? "Unlink this project’s repositories first — repositories are tied to how the project connects."
          : code === ErrorCode.GitHubInstallationNotAllowed
            ? "This installation is not available to the project."
            : "GitHub rejected the installation. Check that the app is still installed.",
      );
    },
  });
  const install = useMutation({
    mutationFn: (appId: string) => startAppInstall(api, appId, rememberReturnUrl()),
    onSuccess: ({ url }) => {
      window.location.href = url;
    },
  });
  const openAccess = useMutation({
    mutationFn: (v: { app: string; inst: string }) => setInstallationAccess(api, v.app, v.inst, true, []),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: projectInstallationsKey(projectId) });
      await queryClient.invalidateQueries({ queryKey: appsKey() });
    },
  });

  if (isLoading) return <Skeleton height={220} />;

  const stepAction = (() => {
    if (hasInstall) {
      return (
        <div className="ghp-action">
          <div>
            <p className="ghp-action-title">Connect this project</p>
            <p className="ghp-muted ghp-small">
              Agents and automations will act as the app’s bot (<code>your-app[bot]</code>), not as a person.
            </p>
          </div>
          <div className="ghp-option-list">
            {installations.map((inst) => (
              <div key={inst.id} className="ghp-option">
                <div className="ghp-grow">
                  <p className="ghp-strong ghp-truncate">{inst.account_login}</p>
                  <p className="ghp-muted ghp-small ghp-truncate">
                    {inst.app_name} · {inst.repository_selection === "all" ? "all repositories" : "selected repositories"}
                  </p>
                </div>
                {canEdit && (
                  <Button variant="primary" size="sm" loading={bind.isPending && bind.variables === inst.id} disabled={bind.isPending} onClick={() => bind.mutate(inst.id)}>
                    Connect
                  </Button>
                )}
              </div>
            ))}
          </div>
        </div>
      );
    }
    if (!isAdmin) {
      return (
        <Banner tone="neutral" icon={<ShieldCheck size={16} aria-hidden="true" />} title="An administrator needs to set up the GitHub App">
          Creating the app is a one-time, instance-wide step. Ask someone with the “Manage GitHub Apps” permission, or use a personal access token below.
        </Banner>
      );
    }
    if (!hasApp) {
      return (
        <div className="ghp-action">
          <div>
            <p className="ghp-action-title">Create your GitHub App</p>
            <p className="ghp-muted ghp-small">
              Takes about a minute. Paca pre-fills everything on GitHub — you confirm once and install it on your account or organization.
            </p>
          </div>
          <div>
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              Create GitHub App
              <ChevronRight size={14} aria-hidden="true" />
            </Button>
          </div>
        </div>
      );
    }
    // App exists.
    if (closedInstall) {
      return (
        <div className="ghp-action">
          <div>
            <p className="ghp-action-title">Make the installation available</p>
            <p className="ghp-muted ghp-small">
              <strong>{closedInstall.app.name}</strong> is installed on {closedInstall.inst.account_login}, but it is not open to this project yet.
            </p>
          </div>
          <div>
            <Button variant="primary" loading={openAccess.isPending} onClick={() => openAccess.mutate({ app: closedInstall.app.id, inst: closedInstall.inst.id })}>
              Allow every project to use it
            </Button>
          </div>
        </div>
      );
    }
    const app = apps?.[0];
    return (
      <div className="ghp-action">
        <div>
          <p className="ghp-action-title">Install {app?.name}</p>
          <p className="ghp-muted ghp-small">Choose the account or organization that owns your repositories, and which repositories the app may see.</p>
        </div>
        <div>
          <Button variant="primary" loading={install.isPending} onClick={() => app && install.mutate(app.id)}>
            Install on GitHub
            <ChevronRight size={14} aria-hidden="true" />
          </Button>
        </div>
      </div>
    );
  })();

  return (
    <div className="ghp-stack">
      <section className={cn(variant === "page" && "ghp-card ghp-hero")} aria-labelledby="ghp-app-heading">
        <div className="ghp-hero-head">
          <h3 id="ghp-app-heading" className="ghp-hero-title">
            <Bot size={18} aria-hidden="true" />
            Connect with a GitHub App
            <Badge tone="primary">
              <Sparkles size={11} aria-hidden="true" />
              Recommended
            </Badge>
          </h3>
          <p className="ghp-muted">The secure, self-hosted way to connect — no personal token to store, rotate or share.</p>
        </div>

        <ul className="ghp-benefits">
          <li><Check size={14} aria-hidden="true" />Acts as its own bot, not as a person</li>
          <li><Check size={14} aria-hidden="true" />Tokens refresh automatically</li>
          <li><Check size={14} aria-hidden="true" />Give each agent its own identity</li>
        </ul>

        <Steps
          steps={[
            { label: "Create the app", state: state(0) },
            { label: "Install it", state: state(1) },
            { label: "Connect this project", state: state(2) },
          ]}
        />

        {stepAction}
        {error && <Banner tone="danger">{error}</Banner>}
      </section>

      {variant === "page" && (
        <>
          <div className="ghp-divider">or</div>
          <TokenAlternative api={api} projectId={projectId} canEdit={canEdit} open={patOpen} onToggle={() => setPatOpen((v) => !v)} onConnected={onConnected} />
        </>
      )}

      {isAdmin && <CreateAppDialog api={api} open={createOpen} onClose={() => setCreateOpen(false)} />}
    </div>
  );
}

// ── Personal access token (secondary path) ────────────────────────────────────

function TokenAlternative({
  api,
  projectId,
  canEdit,
  open,
  onToggle,
  onConnected,
}: {
  api: PluginApiClient;
  projectId: string;
  canEdit: boolean;
  open: boolean;
  onToggle: () => void;
  onConnected: () => void;
}) {
  const queryClient = useQueryClient();
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: () => setGitHubToken(api, token.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: integrationKey(projectId) });
      setToken("");
      setError(null);
      onConnected();
    },
    onError: (err: unknown) => {
      const code = getPluginErrorCode(err);
      setError(
        code === ErrorCode.GitHubInvalidToken
          ? "GitHub rejected the token. Check it has the repo and admin:repo_hook scopes."
          : code === ErrorCode.GitHubUnlinkReposFirst
            ? "Unlink this project’s repositories first — repositories are tied to how the project connects."
            : "Could not save the token. Try again.",
      );
    },
  });

  return (
    <div>
      <button type="button" className="ghp-disclosure" aria-expanded={open} aria-controls="ghp-pat-body" onClick={onToggle}>
        <span className="ghp-row">
          <KeyRound size={15} aria-hidden="true" />
          Use a personal access token instead
        </span>
        <ChevronRight size={15} className="ghp-chev" data-open={open} aria-hidden="true" />
      </button>
      {open && (
        <div id="ghp-pat-body" className="ghp-disclosure-body">
          <p className="ghp-muted ghp-small">
            Everything done through this project is attributed to the token’s owner. Needs the <code>repo</code> and <code>admin:repo_hook</code> scopes. The token is stored encrypted and never shown again.
          </p>
          <form
            className="ghp-row"
            onSubmit={(e) => {
              e.preventDefault();
              if (token.trim()) save.mutate();
            }}
          >
            <Input
              type="password"
              mono
              autoComplete="off"
              aria-label="GitHub personal access token"
              placeholder="ghp_xxxxxxxxxxxxxxxxxxxx"
              value={token}
              disabled={!canEdit || save.isPending}
              onChange={(e) => {
                setToken(e.target.value);
                setError(null);
              }}
            />
            <Button type="submit" variant="secondary" disabled={!token.trim() || !canEdit} loading={save.isPending}>
              Save token
            </Button>
          </form>
          {error && (
            <p className="ghp-small" style={{ color: "var(--ghp-danger)" }} role="alert">
              {error}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

