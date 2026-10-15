import { useEffect, useState } from "react";
import { IconAlertTriangle, IconX } from "@tabler/icons-react";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { OperationDialog } from "../chrome/OperationDialog";
import {
  approveGitRepo,
  clearGitCache,
  fetchGitRepo,
  getGitSettings,
  removeGitRepo,
  setGitRepoMatch,
  type ActionResult,
  type GitRepo,
  type GitSettings,
  type GitSource,
  type GitUnapproved,
} from "../bridge/gitSettings";

// GitSettingsPanel is Settings > Git (The Line slice 6c, slice 6 note §5.3):
// the repositories Klyx may fetch history from, approved one by one. What
// runs is always the approved URL exactly as written, through the system git
// with the owner's SSH agent or credential helper; a URL read from a cluster
// is only matched to an entry and shown scrubbed. Klyx stores no token.

const row: React.CSSProperties = { display: "flex", alignItems: "baseline", gap: 8, padding: "2px 0" };
const labelStyle: React.CSSProperties = { fontSize: 10, color: "var(--color-text-tertiary)", width: 70, flexShrink: 0 };
const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const mono: React.CSSProperties = { fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere" };

export function sizeText(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  return `${Math.max(1, Math.round(n / 1024))} KiB`;
}

function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// The repository's history line: what is cached, and how the last attempt
// went - a failed fetch never hides the last good history.
export function historyLine(r: GitRepo): { text: string; tone: "ok" | "warn" | "muted" } {
  if (r.needsRepair) return { text: "needs repair - Clear cache to rebuild it from scratch", tone: "warn" };
  if (r.fetching) return { text: "fetching…", tone: "muted" };
  const asOf = r.fetchedAt ? `history as of ${clock(r.fetchedAt)} · ${sizeText(r.size)}` : "";
  if (r.error) return { text: [asOf, `fetch failed: ${r.error}`].filter(Boolean).join("; "), tone: "warn" };
  if (asOf) return { text: asOf, tone: "ok" };
  return { text: "not fetched yet", tone: "muted" };
}

export function GitSettingsPanel() {
  const [state, setState] = useState<GitSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState<string | null>(null); // the URL an action runs on
  const [message, setMessage] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [approving, setApproving] = useState<GitUnapproved | null>(null);
  const [removing, setRemoving] = useState<GitRepo | null>(null);

  const load = async () => {
    const s = await getGitSettings();
    setLoadFailed(s === null);
    if (s) setState(s);
  };
  useEffect(() => {
    void load();
  }, []);
  // Until git has started, look again shortly.
  useEffect(() => {
    if (!state || state.binary.ready) return;
    const t = setTimeout(() => void load(), 500);
    return () => clearTimeout(t);
  }, [state]);

  const run = async (url: string, fn: () => Promise<ActionResult>, okText: string) => {
    setBusy(url);
    setMessage(null);
    const r = await fn();
    setBusy(null);
    setMessage(r.ok ? { kind: "ok", text: okText } : { kind: "error", text: r.error || "failed" });
    await load();
  };

  if (loadFailed && !state) {
    return (
      <div data-testid="git-load-failed" style={{ ...hint, color: "var(--color-text-danger)" }}>
        Could not read the Git settings. <button className="l-btn" onClick={() => void load()}>Retry</button>
      </div>
    );
  }
  if (!state) return <div style={hint}>Reading Git settings…</div>;
  const b = state.binary;

  return (
    <div data-testid="git-settings">
      <div data-testid="git-binary" role="status" style={{ fontSize: 12, marginBottom: 6, color: b.off ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>
        {!b.ready ? (
          <span className="t-secondary">Checking git…</span>
        ) : b.off ? (
          <>Git features are off: {b.off}</>
        ) : (
          <>
            <span className="t-secondary">Runs </span>
            <span style={mono}>{b.version?.replace(/^git version /, "git ")}</span>
            <span className="t-secondary"> at </span>
            <span style={mono}>{b.path}</span>
          </>
        )}
      </div>
      <div style={{ ...hint, marginBottom: 8, lineHeight: 1.5 }}>
        Read-only: Klyx fetches commits and trees for the line&apos;s tracked refs and never writes to a repository. It runs only the URLs
        approved here, exactly as written, with your SSH agent and ~/.ssh/config
        {state.helpers.length > 0 ? <> or your credential helper ({state.helpers.join(", ")})</> : null}. Klyx stores no token. A credential helper that
        opens its own window is outside Klyx&apos;s control.
      </div>
      {state.refusedHelpers > 0 && (
        <Notice testId="git-refused-helpers">
          {state.refusedHelpers === 1 ? "A credential helper given as a shell snippet (!…) is" : `${state.refusedHelpers} credential helpers given as shell snippets (!…) are`} not used.
        </Notice>
      )}
      {state.configError && <Notice testId="git-config-error">fleet.yaml cannot be read: {state.configError}</Notice>}
      {state.notes.map((n, i) => <Notice key={i} testId="git-note">{n}</Notice>)}
      {message && (
        <div data-testid="git-message" role="status" style={{ fontSize: 11, margin: "4px 0 8px", color: message.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
          {message.kind === "error" ? "▲ " : ""}{message.text}
        </div>
      )}

      <Subhead>Approved repositories</Subhead>
      {state.repos.length === 0 ? (
        <div style={hint}>None yet. Approve a repository the clusters use, below.</div>
      ) : (
        state.repos.map((r) => (
          <ApprovedRepo
            key={r.url}
            repo={r}
            busy={busy === r.url}
            gitOn={b.ready && !b.off}
            onFetch={() => void run(r.url, () => fetchGitRepo(r.url), "Fetched.")}
            onClear={() => void run(r.url, () => clearGitCache(r.url), "Cache cleared; the next fetch rebuilds it.")}
            onRemove={() => setRemoving(r)}
            onMatch={(key, on) => void run(r.url, () => setGitRepoMatch(r.url, key, on), on ? `${r.url} now also serves ${key}.` : `Removed ${key}.`)}
          />
        ))
      )}

      <Subhead>Used by clusters, not approved</Subhead>
      {state.unapproved.length === 0 ? (
        <div style={hint}>Every Git repository the clusters use is approved.</div>
      ) : (
        state.unapproved.map((u) => (
          <div key={u.key} data-testid="git-unapproved" style={{ padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span style={{ ...mono, flex: 1, minWidth: 0 }}>{u.key}</span>
              <button className="l-btn" onClick={() => setApproving(u)}>Approve…</button>
            </div>
            <Sources sources={u.sources} />
          </div>
        ))
      )}

      {approving && (
        <ApproveDialog
          item={approving}
          onCancel={() => setApproving(null)}
          onApprove={(url, web) => {
            const key = approving.key;
            setApproving(null);
            void run(url, () => approveGitRepo(url, web, key), `Approved ${url}. Fetch it now, or let the fleet screen fetch it.`);
          }}
        />
      )}
      {removing && (
        <ConfirmDialog
          title="Remove this approval?"
          cluster=""
          showScope={false}
          protected={false}
          detail={`Klyx stops fetching ${removing.url} and deletes its cache${removing.size ? ` (${sizeText(removing.size)})` : ""}. Every other line of fleet.yaml stays as it is.`}
          confirmLabel="Remove"
          onCancel={() => setRemoving(null)}
          onConfirm={() => {
            const url = removing.url;
            setRemoving(null);
            void run(url, () => removeGitRepo(url), `Removed ${url}.`);
          }}
        />
      )}
    </div>
  );
}

function ApprovedRepo({ repo: r, busy, gitOn, onFetch, onClear, onRemove, onMatch }: {
  repo: GitRepo;
  busy: boolean;
  gitOn: boolean;
  onFetch: () => void;
  onClear: () => void;
  onRemove: () => void;
  onMatch: (key: string, on: boolean) => void;
}) {
  const [newKey, setNewKey] = useState("");
  const h = historyLine(r);
  return (
    <div data-testid="git-repo" style={{ padding: "8px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span style={{ ...mono, fontSize: 12, fontWeight: 600, flex: 1, minWidth: 0 }} title="runs exactly as written">{r.url}</span>
        <button className="l-btn" disabled={busy || r.fetching || !gitOn || r.tracked.length === 0} onClick={onFetch}>{r.fetching || busy ? "Fetching…" : "Fetch now"}</button>
        <button className="l-btn" disabled={busy || !gitOn || (r.size === 0 && !r.needsRepair)} onClick={onClear}>Clear cache</button>
        <button className="l-btn" disabled={busy} onClick={onRemove}>Remove</button>
      </div>
      <div data-testid="git-history" style={{ fontSize: 11, padding: "3px 0", display: "flex", alignItems: "center", gap: 5, color: h.tone === "warn" ? "var(--color-text-warning)" : h.tone === "ok" ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>
        {h.tone === "warn" && <IconAlertTriangle size={11} stroke={1.7} aria-hidden />}
        {h.text}
      </div>
      <div style={row}>
        <span style={labelStyle}>tracks</span>
        <span style={{ minWidth: 0 }}>
          {r.tracked.map((t) => <span key={t} style={{ ...mono, marginRight: 8 }}>{t}</span>)}
          {r.trackNotes.map((n, i) => <span key={i} style={{ ...hint, display: "block" }}>{n}</span>)}
        </span>
      </div>
      <div style={row}>
        <span style={labelStyle}>serves</span>
        <span style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center", minWidth: 0 }}>
          <span style={mono}>{r.key}</span>
          {r.matches.map((m) => (
            <span key={m} data-testid="git-match" style={{ ...mono, display: "inline-flex", alignItems: "center", gap: 3, padding: "0 4px", borderRadius: 3, boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
              {m}
              <button aria-label={`stop serving ${m}`} disabled={busy} onClick={() => onMatch(m, false)} style={{ border: 0, background: "none", padding: 0, cursor: "pointer", color: "var(--color-text-tertiary)", display: "inline-flex" }}>
                <IconX size={10} stroke={1.6} />
              </button>
            </span>
          ))}
          <input
            aria-label={`another repository ${r.url} serves`}
            className="l-input"
            placeholder="host/org/repo (an SSH alias)"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            style={{ ...mono, width: 220 }}
          />
          <button
            className="l-btn"
            disabled={busy || newKey.trim() === ""}
            onClick={() => {
              onMatch(newKey.trim(), true);
              setNewKey("");
            }}
          >
            Add
          </button>
        </span>
      </div>
      {r.web && (
        <div style={row}>
          <span style={labelStyle}>web</span>
          <span style={mono}>{r.web}</span>
        </div>
      )}
      <div style={row}>
        <span style={labelStyle}>clusters</span>
        {r.sources.length === 0 ? <span style={hint}>no cluster uses it right now</span> : <Sources sources={r.sources} inline />}
      </div>
    </div>
  );
}

function Sources({ sources, inline = false }: { sources: GitSource[]; inline?: boolean }) {
  return (
    <div style={{ display: "grid", gap: 1, minWidth: 0, marginTop: inline ? 0 : 3 }}>
      {sources.map((s, i) => (
        <div key={i} style={{ fontSize: 11, color: "var(--color-text-tertiary)", minWidth: 0 }}>
          <span style={{ color: "var(--color-text-secondary)" }}>{s.cluster}</span> · {s.origin} {s.namespace}/{s.name} · <span style={mono}>{s.url}</span>
        </div>
      ))}
    </div>
  );
}

function ApproveDialog({ item, onCancel, onApprove }: { item: GitUnapproved; onCancel: () => void; onApprove: (url: string, web: string) => void }) {
  const [url, setUrl] = useState(item.suggest ?? "");
  const [web, setWeb] = useState("");
  return (
    <OperationDialog
      title="Approve a Git repository"
      subtitle={`For ${item.key}, which ${item.sources.length === 1 ? "one cluster object uses" : `${item.sources.length} cluster objects use`}.`}
      cluster=""
      protected={false}
      confirmLabel="Approve"
      confirmDisabled={url.trim() === ""}
      onCancel={onCancel}
      onConfirm={() => onApprove(url.trim(), web.trim())}
    >
      <label style={{ display: "grid", gap: 4 }}>
        <span style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>The URL that will run, exactly as written (https or ssh, no token)</span>
        <input aria-label="URL to approve" className="l-input" value={url} onChange={(e) => setUrl(e.target.value)} style={{ ...mono, fontSize: 12 }} />
      </label>
      {item.refused && !item.suggest && (
        <div style={{ ...hint, color: "var(--color-text-warning)" }}>The clusters&apos; URL cannot run as written: {item.refused}. Enter the https or ssh URL to fetch from.</div>
      )}
      <div style={{ ...hint, lineHeight: 1.5 }}>
        It runs through your system git, with your SSH agent and ~/.ssh/config or your credential helper. An SSH host alias resolves through your own
        ~/.ssh/config, as written. This entry will serve <span style={mono}>{item.key}</span>, the repository these clusters use.
      </div>
      <label style={{ display: "grid", gap: 4 }}>
        <span style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>&quot;Open in Git&quot; link (optional, https) for a host Klyx does not know</span>
        <input aria-label="Open in Git link" className="l-input" value={web} onChange={(e) => setWeb(e.target.value)} placeholder="https://git.example.com/org/repo" style={mono} />
      </label>
    </OperationDialog>
  );
}

function Subhead({ children }: { children: React.ReactNode }) {
  return <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", margin: "12px 0 2px" }}>{children}</div>;
}

function Notice({ testId, children }: { testId: string; children: React.ReactNode }) {
  return (
    <div data-testid={testId} style={{ fontSize: 11, color: "var(--color-text-warning)", padding: "2px 0 6px", display: "flex", alignItems: "center", gap: 6 }}>
      <IconAlertTriangle size={11} stroke={1.7} style={{ flexShrink: 0 }} aria-hidden /> <span>{children}</span>
    </div>
  );
}
