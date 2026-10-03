import { useState } from "react";
import { useFleet } from "../store/fleet";
import type { ConnectivityReportDTO, DirectionVerdictDTO } from "../store/fleet";
import { canReach } from "../bridge/connectivity";
import { btn, frame, well, inputWell, selectWell, mono } from "../chrome/primitives";
import { IconPlugConnected, IconPlugConnectedX, IconAlertTriangle } from "@tabler/icons-react";

// ConnectivityView is the network twin of the Access lens: "can pod A reach
// pod B on port P?", answered by statically evaluating NetworkPolicies with
// the API server's own semantics (netpol package). Framed as a simulation:
//   - it evaluates networking.k8s.io/v1 NetworkPolicy ONLY; when Cilium
//     policies exist in scope the verdict is labeled PARTIAL,
//   - pods are resolved by name to their live labels - real pods, not
//     hypothetical label sets.
// Read-only throughout.

const label: React.CSSProperties = { fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" };

function Col({ name, children, width }: { name: string; children: React.ReactNode; width?: number }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 3, width }}>
      <span style={label}>{name}</span>
      {children}
    </div>
  );
}

function DirectionWell({ title, d, onOpenPolicy }: { title: string; d: DirectionVerdictDTO; onOpenPolicy: (ns: string, name: string) => void }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      <span style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>{title}</span>
      <span style={{ fontSize: 11, color: d.allowed ? "var(--color-text-secondary)" : "var(--color-text-danger)" }}>{d.explain}</span>
      {d.policies.map((p) => (
        <button
          key={`${p.namespace}/${p.name}`}
          onClick={() => onOpenPolicy(p.namespace, p.name)}
          title={`open NetworkPolicy ${p.namespace}/${p.name}`}
          style={{
            display: "flex", gap: 8, alignItems: "baseline", textAlign: "left", padding: "4px 8px",
            border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4,
            background: "var(--color-background-primary)", cursor: "pointer", fontSize: 11,
          }}
        >
          <span style={{ ...mono, color: p.admits ? "var(--color-text-success)" : "var(--color-text-tertiary)", flexShrink: 0 }}>
            {p.admits ? "admits" : "selects"}
          </span>
          <span style={{ ...mono, color: "var(--color-text-primary)", overflowWrap: "anywhere" }}>{p.namespace}/{p.name}</span>
        </button>
      ))}
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
  const [report, setReport] = useState<ConnectivityReportDTO | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setBusy(true);
    try {
      setReport(await canReach(cluster, srcNS.trim(), srcPod.trim(), dstNS.trim(), dstPod.trim(), port.trim() === "" ? 0 : Number(port), protocol));
    } finally {
      setBusy(false);
    }
  };

  const openPolicy = (ns: string, name: string) => {
    useFleet.getState().openResource({ group: "networking.k8s.io", version: "v1", plural: "networkpolicies", kind: "NetworkPolicy", scope: "Namespaced" });
    useFleet.getState().openInstance(ns, name);
  };

  return (
    <div style={{ height: "100%", minHeight: 0, overflowY: "auto", boxSizing: "border-box", padding: 16 }}>
      <div style={{ maxWidth: 860, margin: "0 auto", display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
          <div style={{ display: "flex", alignItems: "baseline", gap: 10 }}>
            <span style={{ fontSize: 11, fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-secondary)" }}>connectivity check</span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>static NetworkPolicy simulation between two live pods — read-only</span>
          </div>

          <div style={{ display: "flex", alignItems: "flex-end", gap: 8, flexWrap: "wrap" }}>
            <Col name="from namespace" width={140}>
              <input value={srcNS} onChange={(e) => setSrcNS(e.target.value)} placeholder="team-a" aria-label="source namespace" style={inputWell} />
            </Col>
            <Col name="from pod" width={180}>
              <input value={srcPod} onChange={(e) => setSrcPod(e.target.value)} placeholder="web-7f9c4-xk2lp" aria-label="source pod" style={inputWell} />
            </Col>
            <Col name="to namespace" width={140}>
              <input value={dstNS} onChange={(e) => setDstNS(e.target.value)} placeholder="team-b" aria-label="destination namespace" style={inputWell} />
            </Col>
            <Col name="to pod" width={180}>
              <input value={dstPod} onChange={(e) => setDstPod(e.target.value)} placeholder="db-0" aria-label="destination pod" style={inputWell} />
            </Col>
            <Col name="port" width={80}>
              <input value={port} onChange={(e) => setPort(e.target.value)} placeholder="any" aria-label="port" style={inputWell} />
            </Col>
            <Col name="protocol" width={90}>
              <select value={protocol} onChange={(e) => setProtocol(e.target.value)} aria-label="protocol" style={selectWell}>
                {["TCP", "UDP", "SCTP"].map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
            </Col>
            <div style={{ flex: 1 }} />
            <button onClick={() => void run()} disabled={busy} style={{ ...btn, fontWeight: 600 }}>{busy ? "evaluating…" : "can it reach?"}</button>
          </div>
        </div>

        {report && (
          <div data-testid="connectivity-verdict" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 10 }}>
            {report.error ? (
              <div style={{ display: "flex", alignItems: "flex-start", gap: 6, fontSize: 11, color: "var(--color-text-danger)" }}>
                <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
                <span style={{ overflowWrap: "anywhere" }}>{report.error}</span>
              </div>
            ) : (
              <>
                <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
                  {report.allowed ? (
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 5, fontSize: 12, fontWeight: 700, color: "var(--color-text-success)" }}>
                      <IconPlugConnected size={15} stroke={1.75} /> REACHABLE
                    </span>
                  ) : (
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 5, fontSize: 12, fontWeight: 700, color: "var(--color-text-danger)" }}>
                      <IconPlugConnectedX size={15} stroke={1.75} /> BLOCKED
                    </span>
                  )}
                  <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>by networking.k8s.io/v1 NetworkPolicy simulation</span>
                  {report.ciliumPolicies > 0 && (
                    <span data-testid="cilium-partial" style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
                      PARTIAL — {report.ciliumPolicies} Cilium polic{report.ciliumPolicies === 1 ? "y" : "ies"} in scope are NOT simulated
                    </span>
                  )}
                  {/* A FAILED Cilium count is a missing input, not zero policies:
                      the verdict is partial (review 2026-10-03, K). */}
                  {report.ciliumUnknown && (
                    <span data-testid="cilium-unknown" title={report.ciliumUnknown} style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
                      PARTIAL — Cilium policies could not be counted, so whether any apply is unknown
                    </span>
                  )}
                </div>
                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
                  <DirectionWell title="egress (source side)" d={report.egress} onOpenPolicy={openPolicy} />
                  <DirectionWell title="ingress (destination side)" d={report.ingress} onOpenPolicy={openPolicy} />
                </div>
              </>
            )}
            <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
              simulation of NetworkPolicy semantics on live pod labels — CNI-specific policies, L7 rules, and node firewalls are outside it; ipBlock-only rules never admit a pod-to-pod query
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
