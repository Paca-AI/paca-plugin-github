import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { useMutation } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { createAppManifest, ErrorCode, getPluginErrorCode } from "./github-api";
import { Button, Field, Input, Modal } from "./ui";

// Result codes the backend appends to the return URL after a callback.
const RESULT_MESSAGES: Record<string, string> = {
  invalid_state: "The setup link expired or was already used. Start again.",
  missing_code: "GitHub did not return an authorization code. Start again.",
  conversion_failed: "GitHub rejected the app setup. Start again.",
  store_failed: "The app was created on GitHub but Paca could not save it. Check the server logs.",
  installation_failed: "The installation could not be verified. Install the app again.",
};

export interface FlowResult {
  ok: boolean;
  message: string;
}

/** Reads (and removes) the ?github_app=… outcome the backend appends when it redirects back. */
export function useFlowResult(): [FlowResult | null, () => void] {
  const [result, setResult] = useState<FlowResult | null>(null);
  useEffect(() => {
    const url = new URL(window.location.href);
    const status = url.searchParams.get("github_app");
    if (!status) return;
    const code = url.searchParams.get("github_app_code") ?? "";
    setResult(
      status === "ok"
        ? { ok: true, message: "GitHub App installed. Connect it to this project to continue." }
        : { ok: false, message: RESULT_MESSAGES[code] ?? "GitHub App setup failed." },
    );
    url.searchParams.delete("github_app");
    url.searchParams.delete("github_app_code");
    window.history.replaceState(null, "", url.toString());
  }, []);
  return [result, () => setResult(null)];
}

const RETURN_KEY = "ghp:return-url";

// The project settings page opens the tab named by the URL hash
// (`#plugin:<pluginId>` = that plugin's tab), so the flow returns straight to
// the GitHub tab rather than to whichever tab the page opens by default.
const SETTINGS_TAB_HASH = "plugin:com.paca.github";

export function returnUrl(): string {
  const url = new URL(window.location.href);
  url.searchParams.delete("github_app");
  url.searchParams.delete("github_app_code");
  url.hash = SETTINGS_TAB_HASH;
  return url.toString();
}

/**
 * Remembers where the flow started. The server also keeps this, but it can
 * only hand it back on success; if a step fails the callback page uses this
 * copy to still send the user home with an explanation.
 */
export function rememberReturnUrl(): string {
  const url = returnUrl();
  try {
    window.sessionStorage.setItem(RETURN_KEY, url);
  } catch {
    // Storage can be blocked; the callback page then shows its own error panel.
  }
  return url;
}

export function recalledReturnUrl(): string | null {
  try {
    return window.sessionStorage.getItem(RETURN_KEY);
  } catch {
    return null;
  }
}

/** Appends the flow outcome the settings page turns into a banner. */
export function withFlowResult(target: string, status: "ok" | "error", code = ""): string {
  const url = new URL(target, window.location.origin);
  url.searchParams.set("github_app", status);
  if (code) url.searchParams.set("github_app_code", code);
  return url.toString();
}

/** The Manifest Flow requires a browser form POST to GitHub; a fetch would not work. */
function postManifestToGitHub(postUrl: string, manifest: Record<string, unknown>) {
  const form = document.createElement("form");
  form.method = "POST";
  form.action = postUrl;
  const input = document.createElement("input");
  input.type = "hidden";
  input.name = "manifest";
  input.value = JSON.stringify(manifest);
  form.appendChild(input);
  document.body.appendChild(form);
  form.submit();
}

export function CreateAppDialog({
  api,
  open,
  onClose,
}: {
  api: PluginApiClient;
  open: boolean;
  onClose: () => void;
}) {
  const [name, setName] = useState(() => `Paca ${window.location.hostname}`.slice(0, 34));
  const [target, setTarget] = useState<"personal" | "org">("personal");
  const [org, setOrg] = useState("");
  const [allowAll, setAllowAll] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createAppManifest(api, {
        name: name.trim(),
        owner_org: target === "org" ? org.trim() : undefined,
        allow_all_projects: allowAll,
        return_url: rememberReturnUrl(),
      }),
    onSuccess: (r) => postManifestToGitHub(r.post_url, r.manifest),
    onError: (err: unknown) => {
      setError(
        getPluginErrorCode(err) === ErrorCode.GitHubWebhookURLRequired
          ? "PUBLIC_URL is not configured on the server, so GitHub cannot be pointed back at Paca."
          : "Could not start the setup. Check the app name and organization.",
      );
    },
  });

  const valid = name.trim().length > 0 && (target === "personal" || org.trim().length > 0);
  const submit = () => {
    setError(null);
    create.mutate();
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Create a GitHub App"
      description="Paca opens GitHub with everything pre-filled. You confirm once, then install the app on the account that owns your repositories."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} loading={create.isPending} onClick={submit}>
            Continue to GitHub
          </Button>
        </>
      }
    >
      <Field label="App name" hint="Must be unique across GitHub. This is the name of the bot that appears on commits and reviews.">
        {(id) => <Input id={id} value={name} maxLength={34} onChange={(e) => setName(e.target.value)} />}
      </Field>

      <fieldset style={{ border: 0, padding: 0, margin: 0 }} className="ghp-field">
        <legend className="ghp-label" style={{ padding: 0, marginBottom: 6 }}>
          Create it on
        </legend>
        <label className="ghp-radio">
          <input type="radio" name="ghp-target" checked={target === "personal"} onChange={() => setTarget("personal")} />
          My personal GitHub account
        </label>
        <label className="ghp-radio">
          <input type="radio" name="ghp-target" checked={target === "org"} onChange={() => setTarget("org")} />
          An organization
        </label>
        {target === "org" && (
          <Input aria-label="Organization login" placeholder="Organization login, e.g. acme-inc" value={org} onChange={(e) => setOrg(e.target.value)} />
        )}
      </fieldset>

      <label className="ghp-check">
        <input type="checkbox" checked={allowAll} onChange={(e) => setAllowAll(e.target.checked)} />
        <span>
          Available to every project
          <span className="ghp-hint" style={{ display: "block" }}>
            Turn off to choose which projects may use it later.
          </span>
        </span>
      </label>

      <div className="ghp-next">
        <strong style={{ color: "var(--foreground)" }}>What happens next</strong>
        <ol>
          <li>GitHub shows the app’s settings — you click “Create GitHub App”.</li>
          <li>GitHub asks where to install it — pick the account or organization.</li>
          <li>You land back here, ready to connect.</li>
        </ol>
      </div>

      {error && (
        <p className="ghp-small" style={{ color: "var(--ghp-danger)" }} role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}
