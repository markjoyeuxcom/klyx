import { useState } from "react";
import { Browser } from "@wailsio/runtime";
import { IconExternalLink, IconCopy, IconCheck } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { ForwardDTO } from "../store/fleet";
import { stopForward, stopAllForwards } from "../bridge/forwards";
import { EmptyState, btn, ellipsis } from "../chrome/primitives";
import { LensLayout } from "../layout/LensLayout";
import { IconArrowsLeftRight } from "@tabler/icons-react";

// ForwardsView is the fleet-level port-forwards section: every active tunnel
// across every cluster, in working detail (the TopBar popover stays the
// glance/kill surface). The forwards slice is push-fed by forwards:changed,
// so this view renders live state with no fetching of its own.

const gridCols = "12px minmax(0,0.8fr) 60px minmax(0,1.2fr) 170px 70px 150px";

function ago(unix: number): string {
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix);
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`;
}

const kindShort: Record<string, string> = { Pod: "pod", Service: "svc" };

export function ForwardsView() {
  const forwards = useFleet((s) => s.forwards);
  const broken = forwards.filter((f) => f.status === "broken").length;

  // Step 7d: an app route on the workspace template. Active tunnels are
  // neutral; only a broken one is marked.
  return (
    <LensLayout
      label="Port-forwards"
      title="Port-forwards"
      summary={forwards.length === 0 ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{forwards.length}</span>
          <span className="t-secondary">active across the fleet</span>
          {broken > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>▲ {broken} broken</span>}
        </>
      )}
      headerEnd={forwards.length > 0 ? (
        <button className="l-btn" onClick={() => void stopAllForwards()} data-testid="forwards-view-stop-all">Stop all</button>
      ) : undefined}
      state={forwards.length === 0 ? "empty" : "ready"}
      stateView={
        <EmptyState
          icon={<IconArrowsLeftRight size={28} stroke={1.2} />}
          title="No active port-forwards."
          hint={'Start one from a pod\'s detail panel ("forward") or a service\'s port buttons.'}
        />
      }
      body={
        <div className="ov-page" style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>
          <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", borderBottom: "1px solid var(--color-border-tertiary)" }}>
            <span /><span className="t-section-label">Cluster</span><span className="t-section-label">Kind</span><span className="t-section-label">Target</span><span className="t-section-label">Tunnel</span><span className="t-section-label">Started</span><span />
          </div>
          {forwards.map((f) => <ForwardRow key={f.id} f={f} />)}
        </div>
      }
      footerEnd={<span className="t-secondary">push-fed - tunnels update as they change</span>}
    />
  );
}

function ForwardRow({ f }: { f: ForwardDTO }) {
  const broken = f.status === "broken";
  const url = `http://localhost:${f.localPort}`;
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard unavailable: no feedback beats a lying checkmark
    }
  };

  return (
    <div
      data-testid={`forwards-view-row-${f.id}`}
      style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center", padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)" }}
    >
      <span
        title={broken ? "tunnel broken - stop and restart it" : "active"}
        aria-label={broken ? "broken" : "active"}
        style={{ color: broken ? "var(--color-text-warning)" : "var(--color-text-tertiary)", fontSize: 11 }}
      >{broken ? "▲" : "·"}</span>
      <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={f.cluster}>{f.cluster}</span>
      <span style={{ color: "var(--color-text-tertiary)" }}>{kindShort[f.targetKind] ?? f.targetKind.toLowerCase()}</span>
      <span style={ellipsis} title={`${f.namespace}/${f.targetName}`}>
        <span style={{ color: "var(--color-text-tertiary)" }}>{f.namespace}</span>/{f.targetName}
      </span>
      <span style={{ color: broken ? "var(--color-text-warning)" : undefined }}>
        :{f.targetPort} <span style={{ color: "var(--color-text-tertiary)" }}>→</span> localhost:{f.localPort}
      </span>
      <span style={{ color: "var(--color-text-tertiary)" }}>{ago(f.startedUnix)} ago</span>
      <span style={{ display: "flex", gap: 4, justifyContent: "flex-end" }}>
        <button
          onClick={() => void copy()}
          aria-label={`copy url for ${f.id}`}
          title={`Copy ${url}`}
          style={iconBtn}
        >
          {copied ? <IconCheck size={13} stroke={1.5} /> : <IconCopy size={13} stroke={1.5} />}
        </button>
        <button
          onClick={() => void Browser.OpenURL(url)}
          aria-label={`open ${f.id} in browser`}
          title={`Open ${url} in the browser`}
          style={iconBtn}
        >
          <IconExternalLink size={13} stroke={1.5} />
        </button>
        <button
          onClick={() => void stopForward(f.id)}
          aria-label={`stop forward ${f.id}`}
          style={{ ...btn, padding: "2px 8px" }}
        >stop</button>
      </span>
    </div>
  );
}

const iconBtn: React.CSSProperties = { ...btn, padding: "2px 6px", display: "flex", alignItems: "center" };
