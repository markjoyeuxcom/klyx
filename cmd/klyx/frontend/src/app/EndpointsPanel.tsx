import { useEffect, useState } from "react";
import {
  getEndpoints,
  moveToKeychain,
  removeCredential,
  setCredential,
  testEndpoint,
  type EndpointAction,
  type EndpointRow,
  type EndpointsState,
} from "../bridge/endpoints";

// EndpointsPanel is Settings > Endpoints (coverage C5 §8): each cluster's
// metrics, Alertmanager and Loki endpoints with their credential's state.
// The endpoints are authored in fleet.yaml; this sets, removes, moves and
// tests credentials only. A secret goes in and never comes back, and every
// action carries the endpoint as the person saw it.

const KIND_LABEL: Record<string, string> = { metrics: "Metrics", alertmanager: "Alertmanager", loki: "Loki" };
const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "3px 0", flexWrap: "wrap" };

// A typed credential belongs to the endpoint as the person saw it: its draft
// is keyed by the revision, and dropped when that revision is gone, so it is
// never stored for an endpoint that changed under it (Codex's C5
// implementation review, finding 1).
const draftKey = (r: Pick<EndpointRow, "cluster" | "kind" | "revision">) => `${r.cluster}/${r.kind}@${r.revision}`;

// what the state says, in words; good states read in ink, the rest warn
export function stateText(r: EndpointRow): { text: string; warn: boolean } {
  switch (r.state) {
    case "not needed":
      return { text: "no credential configured", warn: false };
    case "not sent":
      return { text: "not sent: proxied through the Kubernetes API server", warn: false };
    case "stored":
      return { text: "stored in the keychain", warn: false };
    case "legacy":
      return { text: r.storedToo ? "in fleet.yaml in plain text, and stored in the keychain - finish the move" : "in fleet.yaml in plain text", warn: true };
  }
  return { text: r.why ? `${r.state}: ${r.why}` : r.state, warn: true };
}

export function EndpointsPanel() {
  const [state, setState] = useState<EndpointsState | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [inputs, setInputs] = useState<Record<string, string>>({});

  const load = async () => {
    const st = await getEndpoints();
    setLoadFailed(!st);
    if (st) {
      setState(st);
      const live = new Set(st.rows.map(draftKey));
      setInputs((cur) => Object.fromEntries(Object.entries(cur).filter(([k]) => live.has(k))));
    }
  };
  useEffect(() => {
    void load();
  }, []);

  const run = async (fn: () => Promise<EndpointAction>) => {
    setBusy(true);
    const r = await fn();
    setBusy(false);
    setMessage({ ok: r.ok, text: r.message });
    await load();
  };

  if (loadFailed && !state) {
    return (
      <div data-testid="endpoints-load-failed" style={{ ...hint, color: "var(--color-text-danger)" }}>
        Could not read the endpoints. <button className="l-btn" onClick={() => void load()}>Retry</button>
      </div>
    );
  }
  if (!state) return <div style={hint}>Reading endpoints…</div>;

  return (
    <div data-testid="endpoints-settings">
      <div style={{ ...hint, marginBottom: 8 }}>
        Endpoints are set in fleet.yaml (metrics, alertmanager, loki under a cluster), and this list reads the file as it is now. A cluster takes an edited endpoint when you Test it or change its credential here, or when Klyx restarts. A credential is kept in the keychain for the endpoint's origin only, and is sent nowhere else.
      </div>
      {loadFailed && (
        // a reread after an action failed: the rows are as last read
        // (Settings review slice 0)
        <div role="status" data-testid="endpoints-reread-failed" style={{ fontSize: 11, color: "var(--color-text-warning)", marginBottom: 8 }}>
          Klyx could not read the endpoints again: these rows are as last read and may be out of date.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void load()}>Retry</button>
        </div>
      )}
      {state.error && <div role="status" data-testid="endpoints-error" style={{ fontSize: 11, color: "var(--color-text-warning)", marginBottom: 8 }}>fleet.yaml cannot be read: {state.error}</div>}
      {message && (
        <div role="status" data-testid="endpoints-message" style={{ fontSize: 11, marginBottom: 8, color: message.ok ? "var(--color-text-primary)" : "var(--color-text-warning)" }}>{message.text}</div>
      )}
      {state.rows.length === 0 && !state.error && <div style={hint}>No cluster configures a metrics, Alertmanager or Loki endpoint.</div>}
      {state.rows.map((r) => {
        const k = `${r.cluster}/${r.kind}`;
        const dk = draftKey(r);
        const st = stateText(r);
        const value = inputs[dk] ?? "";
        return (
          <div key={k} data-testid="endpoint-row" data-endpoint={k} style={{ padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <div style={row}>
              <span style={{ fontWeight: 600, minWidth: 110 }}>{r.cluster} · {KIND_LABEL[r.kind] ?? r.kind}</span>
              <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere" }}>{r.source}</span>
              {r.tenant && <span style={hint}>tenant {r.tenant}</span>}
              {r.authType && <span style={hint}>{r.authType}{r.username ? ` as ${r.username}` : ""}</span>}
              {r.tlsOff && <span data-testid="endpoint-tls-off" style={{ fontSize: 11, color: "var(--color-text-warning)" }}>certificate not verified (tlsSkipVerify)</span>}
            </div>
            {(r.scope || r.unscoped) && (
              <div style={row}>
                <span data-testid="endpoint-scope" style={{ fontSize: 11, color: r.unscoped ? "var(--color-text-tertiary)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>
                  {r.scope
                    ? <>this cluster's {r.kind === "loki" ? "streams" : "alerts"}: <span style={{ fontFamily: "var(--font-mono)" }}>{r.scope}</span></>
                    : `no match: everything this ${KIND_LABEL[r.kind] ?? r.kind} holds is read as ${r.cluster}'s; set match when other clusters share it`}
                </span>
              </div>
            )}
            <div style={row}>
              <span data-testid="endpoint-state" style={{ fontSize: 11, color: st.warn ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{st.text}</span>
            </div>
            <div style={row}>
              {r.canSet && (
                <>
                  <input
                    aria-label={`${r.cluster} ${r.kind} ${r.authType === "basic" ? "password" : "token"}`}
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    className="l-input"
                    style={{ flex: 1, minWidth: 160 }}
                    placeholder={r.state === "stored" ? "paste a new one to replace it" : r.authType === "basic" ? "paste the password" : "paste the token"}
                    value={value}
                    onChange={(e) => setInputs({ ...inputs, [dk]: e.target.value })}
                  />
                  <button
                    className="l-btn"
                    aria-label={`${r.state === "stored" ? "Replace" : "Store"} the ${r.cluster} ${KIND_LABEL[r.kind] ?? r.kind} credential`}
                    disabled={busy || value.trim() === ""}
                    onClick={() => {
                      setInputs({ ...inputs, [dk]: "" });
                      void run(() => setCredential(r.cluster, r.kind, r.revision, value));
                    }}
                  >
                    {r.state === "stored" ? "Replace" : "Store"}
                  </button>
                </>
              )}
              {r.canMove && <button className="l-btn" aria-label={`Move the ${r.cluster} metrics token to the keychain`} disabled={busy} onClick={() => void run(() => moveToKeychain(r.cluster, r.revision))}>Move to keychain</button>}
              {r.canRemove && (
                <button className="l-btn l-btn-danger" aria-label={`${r.removeLabel ?? "Remove"} (${r.cluster} ${KIND_LABEL[r.kind] ?? r.kind})`} disabled={busy} onClick={() => void run(() => removeCredential(r.cluster, r.kind, r.revision))}>
                  {r.removeLabel ?? "Remove"}
                </button>
              )}
              {r.canTest
                ? <button className="l-btn" aria-label={`Test the ${r.cluster} ${KIND_LABEL[r.kind] ?? r.kind} endpoint`} disabled={busy} onClick={() => void run(() => testEndpoint(r.cluster, r.kind, r.revision))}>Test</button>
                : r.testNote && <span style={hint}>Test: {r.testNote}</span>}
            </div>
          </div>
        );
      })}
    </div>
  );
}
