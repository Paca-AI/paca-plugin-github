import { type PluginApiClient } from "@paca-ai/plugin-sdk-react";
import { TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { recalledReturnUrl, withFlowResult } from "./app-flow";
import {
  completeAppCreated,
  completeAppInstalled,
  ErrorCode,
  getPluginErrorCode,
  type SetupCallbackResult,
} from "./github-api";
import { Button, GitHubIcon, Spinner } from "./ui";

/**
 * The page GitHub sends the browser back to during GitHub App setup
 * (`/plugin-pages/com.paca.github/github-app-callback`, a plugin `app.page`).
 *
 * It is a page of the web app, not a plugin API route, on purpose: by the time
 * it mounts the host has already restored the user's session (refreshing an
 * expired access token, or signing the user in and returning here), so the two
 * calls below are ordinary authenticated, permission-gated plugin requests.
 *
 * GitHub appends different parameters to each leg of the flow:
 *   created   → ?code=…&state=…               (manifest accepted)
 *   installed → ?installation_id=…&state=…    (app installed on an account)
 */

type Step = "created" | "installed";

const FLOW_CODES: Record<string, string> = {
  [ErrorCode.GitHubSetupInvalidState]: "invalid_state",
  [ErrorCode.GitHubSetupMissingParameter]: "missing_code",
  [ErrorCode.GitHubSetupConversionFailed]: "conversion_failed",
  [ErrorCode.GitHubSetupStoreFailed]: "store_failed",
  [ErrorCode.GitHubSetupInstallationFailed]: "installation_failed",
};

const MESSAGES: Record<string, string> = {
  invalid_state: "This setup link has expired or was already used. Start the setup again from Project Settings → GitHub.",
  missing_code: "GitHub did not send everything Paca needs. Start the setup again.",
  conversion_failed: "GitHub rejected the app setup. Start the setup again.",
  store_failed: "The app was created on GitHub but Paca could not save it. Check the server logs.",
  installation_failed: "GitHub did not confirm the installation. Install the app again from Project Settings → GitHub.",
};

function readParams(): { step: Step; body: Record<string, string> } | null {
  const q = new URLSearchParams(window.location.search);
  const state = q.get("state") ?? "";
  const code = q.get("code");
  const installationId = q.get("installation_id");
  if (code) return { step: "created", body: { code, state } };
  if (installationId) return { step: "installed", body: { installation_id: installationId, state } };
  return null;
}

// Each callback must run exactly once — the code and state are single-use — yet
// React may mount the page twice (StrictMode, a remount). Sharing one promise
// per set of parameters makes the second mount reuse the first result.
const inFlight = new Map<string, Promise<SetupCallbackResult>>();

function run(api: PluginApiClient, step: Step, body: Record<string, string>): Promise<SetupCallbackResult> {
  const key = `${step}:${JSON.stringify(body)}`;
  let p = inFlight.get(key);
  if (!p) {
    p =
      step === "created"
        ? completeAppCreated(api, body as { code: string; state: string })
        : completeAppInstalled(api, body as { installation_id: string; state: string });
    inFlight.set(key, p);
  }
  return p;
}

export default function GitHubAppCallbackPage({ api }: { api: PluginApiClient }) {
  const params = readParams();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!params) {
      setError("missing_code");
      return;
    }
    let cancelled = false;
    run(api, params.step, params.body)
      .then((result) => {
        if (cancelled) return;
        // created → on to GitHub to install; installed → back to where it began.
        window.location.replace(result.next_url ?? withFlowResult(result.return_url, "ok"));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const code = FLOW_CODES[getPluginErrorCode(err) ?? ""] ?? "setup_failed";
        const back = recalledReturnUrl();
        // Send the user home with the explanation shown as a banner there; if we
        // don't know where home is, explain it here.
        if (back) {
          window.location.replace(withFlowResult(back, "error", code));
          return;
        }
        setError(code);
      });
    return () => {
      cancelled = true;
    };
    // The parameters are read once from the URL; the page is not reused.
    // biome-ignore lint/correctness/useExhaustiveDependencies: run once per mount
  }, []);

  return (
    <div className="ghp ghp-callback" role="status" aria-live="polite">
      <div className="ghp-card ghp-callback-card">
        <div className="ghp-header-icon">
          <GitHubIcon width={22} height={22} />
        </div>
        {error === null ? (
          <>
            <Spinner />
            <h1 className="ghp-title">{params?.step === "installed" ? "Confirming the installation…" : "Finishing GitHub App setup…"}</h1>
            <p className="ghp-muted">
              {params?.step === "installed" ? "Checking with GitHub that the app is installed." : "Saving your new app. You’ll be taken to GitHub to install it next."}
            </p>
          </>
        ) : (
          <>
            <TriangleAlert size={28} style={{ color: "var(--ghp-danger)" }} aria-hidden="true" />
            <h1 className="ghp-title">GitHub App setup didn’t finish</h1>
            <p className="ghp-muted">{MESSAGES[error] ?? "Something went wrong. Start the setup again from Project Settings → GitHub."}</p>
            <Button variant="primary" onClick={() => window.location.assign("/home")}>
              Back to Paca
            </Button>
          </>
        )}
      </div>
    </div>
  );
}
