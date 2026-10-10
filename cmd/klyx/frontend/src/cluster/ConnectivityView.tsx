import { useEffect, useMemo, useRef, useState } from "react";
import { IconAlertTriangle, IconPlugConnected, IconPlugConnectedX, IconTopologyStar } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { ConnectivityReportDTO, DirectionVerdictDTO } from "../store/fleet";
import { canReach } from "../bridge/connectivity";
import { EmptyState } from "../chrome/EmptyState";
import { LensLayout } from "../layout/LensLayout";
import { InspectorBlock } from "../layout/InspectorLayout";

// ConnectivityView - "can pod A reach pod B on port P?" on the workspace
// template (spec §6 step 5), the network twin of the Access lens. A query
// tool: the controls row states the question, the body holds the form and
// the answer. Framed as a simulation of networking.k8s.io/v1 NetworkPolicy on
// live pod labels: Cilium policies in scope (or an uncountable set) make the
// verdict PARTIAL, said in words beside the verdict. Every answer is pinned to
// its exact question and cluster; Reachable/Blocked are words with a plug
// shape, never a lamp. Read-only throughout.

type Question = { srcNS: string; srcPod: string; dstNS: string; dstPod: string; port: number; protocol: string };
type Pinned = { cluster: string; key: string; q: Question; report: ConnectivityReportDTO };
const RAW = { autoCapitalize: "off", autoCorrect: "off", spellCheck: false } as const;

// parsePort: empty = any port; otherwise an integer 1..65535, never NaN.
export function parsePort(raw: string): number | null {
  const t = raw.trim();
  if (t === "") return 0;
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return n >= 1 && n <= 65535 ? n : null;
}
export function describeQuestion(q: Question): string {
  // the protocol is always part of the question: an any-port query asks
  // whether SOME port of that protocol is admitted both ways
  return `${q.srcNS}/${q.srcPod} → ${q.dstNS}/${q.dstPod} on ${q.port === 0 ? `any ${q.protocol} port` : `${q.protocol}/${q.port}`}`;
}

function openPolicy(ns: string, name: string) {
  useFleet.getState().openResource({ group: "networking.k8s.io", version: "v1", plural: "networkpolicies", kind: "NetworkPolicy", scope: "Namespaced" });
  useFleet.getState().openInstance(ns, name);
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 3, minWidth: 0 }}>
      <span className="t-secondary" aria-hidden="true">{label}</span>
      {children}
    </div>
  );
}

export function ConnectivityView({ cluster }: { cluster: string }) {
  const [srcNS, setSrcNS] = useState("");
  const [srcPod, setSrcPod] = useState("");
  const [dstNS, setDstNS] = useState("");
  const [dstPod, setDstPod] = useState("");
  const [port, setPort] = useState("");
  const [protocol, setProtocol] = useState("TCP");
  const [answer, setAnswer] = useState<Pinned | null>(null);
  const [busy, setBusy] = useState(false);
  const gen = useRef(0);

  // The component is reused across clusters: answers never cross over.
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    gen.current++;
    setAnswer(null); setBusy(false);
  }, [cluster]);

  const portNum = parsePort(port);
  const q: Question | null = useMemo(() => {
    if (portNum === null) return null;
    return { srcNS: srcNS.trim(), srcPod: srcPod.trim(), dstNS: dstNS.trim(), dstPod: dstPod.trim(), port: portNum, protocol };
  }, [srcNS, srcPod, dstNS, dstPod, portNum, protocol]);
  const complete = !!q && q.srcNS !== "" && q.srcPod !== "" && q.dstNS !== "" && q.dstPod !== "";
  const canRun = complete && !busy;

  const run = async () => {
    if (!q || !complete) return;
    const g = ++gen.current;
    const origin = cluster;
    const asked = q;
    setBusy(true);
    try {
      const r = await canReach(origin, asked.srcNS, asked.srcPod, asked.dstNS, asked.dstPod, asked.port, asked.protocol);
      if (g === gen.current) setAnswer({ cluster: origin, key: JSON.stringify(asked), q: asked, report: r });
    } finally {
      if (g === gen.current) setBusy(false);
    }
  };

  const now = answer && answer.cluster === cluster ? answer : null;
  const outdated = !!now && (!q || now.key !== JSON.stringify(q));

  const form = (
    <form aria-label="Connectivity question" onSubmit={(e) => { e.preventDefault(); if (canRun) void run(); }}
      style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))", gap: "8px 10px", alignItems: "end", padding: "10px 12px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
      <Field label="From namespace"><input {...RAW} className="l-input" value={srcNS} onChange={(e) => setSrcNS(e.target.value)} placeholder="e.g. team-a" aria-label="source namespace" /></Field>
      <Field label="From pod"><input {...RAW} className="l-input" value={srcPod} onChange={(e) => setSrcPod(e.target.value)} placeholder="e.g. web-7f9c4-xk2lp" aria-label="source pod" /></Field>
      <Field label="To namespace"><input {...RAW} className="l-input" value={dstNS} onChange={(e) => setDstNS(e.target.value)} placeholder="e.g. team-b" aria-label="destination namespace" /></Field>
      <Field label="To pod"><input {...RAW} className="l-input" value={dstPod} onChange={(e) => setDstPod(e.target.value)} placeholder="e.g. db-0" aria-label="destination pod" /></Field>
      <Field label="Port">
        <input {...RAW} className="l-input" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} placeholder="empty = any" aria-label="port" aria-invalid={portNum === null} />
      </Field>
      <Field label="Protocol">
        <select className="l-input" value={protocol} onChange={(e) => setProtocol(e.target.value)} aria-label="protocol">
          {["TCP", "UDP", "SCTP"].map((p) => <option key={p} value={p}>{p}</option>)}
        </select>
      </Field>
      {portNum === null && <div role="alert" className="t-secondary" style={{ gridColumn: "1 / -1", color: "var(--color-text-warning)" }}>Port must be a number from 1 to 65535, or empty for any port.</div>}
    </form>
  );

  return (
    <LensLayout
      label="Connectivity"
      title="Connectivity check"
      summary={<span className="t-secondary">static NetworkPolicy simulation between two live pods - read-only</span>}
      controls={
        <>
          <span className="t-secondary" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 }}>
            {complete && q ? describeQuestion(q) : "Name a source and destination pod below"}
          </span>
          <span style={{ marginLeft: "auto", flexShrink: 0 }}>
            <button className="l-btn l-btn-primary" disabled={!canRun} onClick={() => void run()}>{busy ? "Evaluating…" : "Can it reach?"}</button>
          </span>
        </>
      }
      state="ready"
      body={
        <div style={{ display: "flex", flexDirection: "column", gap: 12, minHeight: 0, flex: 1, overflowY: "auto" }}>
          {form}
          {!now && !busy && (
            <EmptyState
              icon={<IconTopologyStar size={28} stroke={1.2} />}
              title="Ask a connectivity question."
              hint="Klyx evaluates networking.k8s.io/v1 NetworkPolicies against the two pods' live labels, both directions. CNI-specific policies, L7 rules and node firewalls are outside the simulation."
            />
          )}
          {now && <Answer pinned={now} outdated={outdated} />}
        </div>
      }
    />
  );
}

function Answer({ pinned, outdated }: { pinned: Pinned; outdated: boolean }) {
  const r = pinned.report;
  const partial = r.ciliumPolicies > 0 || !!r.ciliumUnknown;
  return (
    <section data-testid="connectivity-verdict" aria-label="Answer" className="t-body" style={{ display: "grid", gap: 10, padding: "10px 12px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
      <div className="t-status">Can {describeQuestion(pinned.q)}?</div>
      {outdated && <div role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The form has changed since this answer - it answers the question above. Run Can it reach? again for the new one.</div>}
      {r.error ? (
        <div style={{ display: "flex", alignItems: "flex-start", gap: 6, color: "var(--color-text-danger)" }}>
          <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
          <span style={{ overflowWrap: "anywhere" }}>{r.error}</span>
        </div>
      ) : (
        <>
          <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
            {r.allowed
              ? <span className="t-status" style={{ display: "inline-flex", alignItems: "center", gap: 5, color: "var(--color-text-success)" }}><IconPlugConnected size={15} stroke={1.75} /> Reachable{partial ? " by NetworkPolicy" : ""}</span>
              : <span className="t-status" style={{ display: "inline-flex", alignItems: "center", gap: 5, color: "var(--color-text-danger)" }}><IconPlugConnectedX size={15} stroke={1.75} /> Blocked{partial ? " by NetworkPolicy" : ""}</span>}
            <span className="t-secondary">networking.k8s.io/v1 NetworkPolicy simulation</span>
          </div>
          {r.ciliumPolicies > 0 && <div data-testid="cilium-partial" style={{ color: "var(--color-text-warning)" }}>Partial verdict - {r.ciliumPolicies} Cilium polic{r.ciliumPolicies === 1 ? "y is" : "ies are"} in scope and not simulated; they can still block or admit this traffic.</div>}
          {/* a FAILED Cilium count is a missing input, not zero policies */}
          {r.ciliumUnknown && <div data-testid="cilium-unknown" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Partial verdict - Cilium policies could not be counted ({r.ciliumUnknown}), so whether any apply is unknown.</div>}
          {r.note && <div data-testid="connectivity-note" style={{ color: r.allowed ? "var(--color-text-secondary)" : "var(--color-text-danger)", overflowWrap: "anywhere" }}>{r.note}</div>}
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))", gap: 12 }}>
            <Direction title="Egress (source side)" d={r.egress} />
            <Direction title="Ingress (destination side)" d={r.ingress} />
          </div>
          {(Object.keys(r.srcLabels ?? {}).length > 0 || Object.keys(r.dstLabels ?? {}).length > 0) && (
            <InspectorBlock title="Resolved pod labels" defaultOpen={false}>
              <div className="insp-kv">
                <span>source</span><span style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>{Object.entries(r.srcLabels ?? {}).map(([k, v]) => `${k}=${v}`).join(", ") || "none"}</span>
                <span>destination</span><span style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>{Object.entries(r.dstLabels ?? {}).map(([k, v]) => `${k}=${v}`).join(", ") || "none"}</span>
              </div>
            </InspectorBlock>
          )}
        </>
      )}
      <div className="t-legend">simulation of NetworkPolicy semantics on live pod labels - CNI-specific policies, L7 rules and node firewalls are outside it; ipBlock-only rules never admit a pod-to-pod query</div>
    </section>
  );
}

function Direction({ title, d }: { title: string; d: DirectionVerdictDTO }) {
  return (
    <div style={{ display: "grid", gap: 4, alignContent: "start" }}>
      <span className="t-section-label">{title}</span>
      <span style={{ color: d.allowed ? "var(--color-text-secondary)" : "var(--color-text-danger)", overflowWrap: "anywhere" }}>{d.explain}</span>
      {d.policies.map((p) => (
        <button key={`${p.namespace}/${p.name}`} type="button" className="lens-row" onClick={() => openPolicy(p.namespace, p.name)} aria-label={`open NetworkPolicy ${p.namespace}/${p.name}`}
          style={{ display: "flex", gap: 8, alignItems: "baseline", textAlign: "left", padding: "4px 8px", borderRadius: 4, cursor: "pointer", border: 0, font: "inherit", color: "var(--color-text-primary)", background: "var(--bench-well-bg, var(--color-background-primary))", boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
          <span style={{ fontFamily: "var(--font-mono)", color: p.admits ? "var(--color-text-success)" : "var(--color-text-secondary)", flexShrink: 0 }}>{p.admits ? "admits" : "selects"}</span>
          <span style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>{p.namespace}/{p.name}</span>
        </button>
      ))}
    </div>
  );
}
