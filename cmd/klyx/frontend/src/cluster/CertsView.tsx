import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO, ChainNodeDTO, CertChainDTO } from "../store/fleet";
import { openCertManager, closeCertManager, getCertificateChain } from "../bridge/certmanager";
import { explainCertificate } from "../bridge/ai";
import { useAIAvailability } from "./useAI";
import type { AIResultDTO } from "../store/fleet";
import { useListKeys } from "../chrome/useListKeys";
import { FKeysBar } from "../chrome/FKeysBar";
import { inputWell, MetersStrip, well, Badge, FilterButton, StatusDot, frame, ellipsis, mono, readyColor, toneColor, type Tone } from "../chrome/primitives";
import { InspectorFacts, InspectorSection, InspectorStatus } from "./ResourceInspector";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { IconCertificate, IconSparkles, IconAlertTriangle } from "@tabler/icons-react";
import { SECRET_REF } from "./resourceRefs";
import { ManifestButton } from "./ManifestButton";
import { SyncingChip, staleStyle } from "./staleness";

type CertFilter = "all" | "attention" | "expiring" | "issuing" | "issuerProblem" | "issuers";

const keyOf = (c: { namespace: string; name: string }) => `${c.namespace}/${c.name}`;
const arrayOf = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);

const nowSec = () => Math.floor(Date.now() / 1000);

function expired(c: CertificateDTO): boolean {
  return c.notAfterUnix > 0 && c.notAfterUnix <= nowSec();
}

// expiringSoon: cert-manager's renewalTime already encodes renewBefore, so
// now >= renewalTime is the truthful "in the renewal window" signal.
function expiringSoon(c: CertificateDTO): boolean {
  return !expired(c) && c.renewalUnix > 0 && nowSec() >= c.renewalUnix;
}

function expiryText(c: CertificateDTO): string {
  if (c.notAfterUnix === 0) return "—";
  const secs = c.notAfterUnix - nowSec();
  if (secs <= 0) return "expired";
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
}

// Render in LOCAL time (en-CA gives ISO-like YYYY-MM-DD) to match the app's
// Age.tsx absolute hovers - a UTC slice off-by-ones a day near midnight.
function dateText(unix?: number): string {
  return unix && unix > 0 ? new Date(unix * 1000).toLocaleDateString("en-CA") : "—";
}

// A Ready cert that is merely in the renewal window is NOT attention (cert-manager
// renews it automatically); an expired or not-Ready cert is.
function needsAttention(c: CertificateDTO): boolean {
  return c.ready !== "Ready" || expired(c);
}

function certRank(c: CertificateDTO): number {
  if (c.ready === "Failed" || expired(c)) return 0;
  if (c.ready === "Issuing") return 1;
  if (expiringSoon(c)) return 2;
  if (c.ready !== "Ready") return 3;
  return 4;
}

function issuerRank(i: IssuerDTO): number {
  if (i.ready === "Failed") return 0;
  if (i.ready !== "Ready") return 1;
  return 2;
}

function statusText(c: CertificateDTO): string {
  if (expired(c)) return "expired";
  return c.ready.toLowerCase();
}

// resolveIssuer finds the bound Issuer/ClusterIssuer for a certificate. A
// ClusterIssuer is cluster-scoped; an Issuer is matched within the cert's namespace.
function resolveIssuer(c: CertificateDTO, issuers: IssuerDTO[]): IssuerDTO | undefined {
  return issuers.find(
    (i) => i.kind === c.issuerKind && i.name === c.issuerName && (i.kind === "ClusterIssuer" || i.namespace === c.namespace),
  );
}

function issuerProblem(c: CertificateDTO, issuers: IssuerDTO[]): boolean {
  const issuer = resolveIssuer(c, issuers);
  return !issuer || issuer.ready !== "Ready";
}

function matchesFilter(c: CertificateDTO, filter: CertFilter, issuers: IssuerDTO[]): boolean {
  if (filter === "attention") return needsAttention(c);
  if (filter === "expiring") return expiringSoon(c) || expired(c);
  if (filter === "issuing") return c.ready === "Issuing";
  if (filter === "issuerProblem") return issuerProblem(c, issuers);
  return true;
}

function matchesQuery(c: CertificateDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [c.namespace, c.name, c.issuerName, c.secretName, c.reason, ...arrayOf(c.dnsNames)].some((v) => v.toLowerCase().includes(q));
}

export function CertsView({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.certManagerTier ?? "Unknown");
  const cm = useFleet((s) => s.certmanager);
  const select = useFleet((s) => s.selectCertificate);
  const [filter, setFilter] = useState<CertFilter>("all");
  const [query, setQuery] = useState("");
  const [namespace, setNamespace] = useState("");
  const [kbKey, setKbKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const absent = tier === "Absent";

  useEffect(() => {
    if (absent) return;
    // If cleanup runs before openCertManager resolves, invoke the late-arriving
    // unsubscriber immediately - otherwise its listener leaks for the app's
    // lifetime and duplicates store writes on every reopen.
    let cancelled = false;
    let off = () => {};
    openCertManager(cluster).then((u) => (cancelled ? u() : (off = u))).catch((e) => console.error("openCertManager", e));
    return () => {
      cancelled = true;
      off();
      void closeCertManager(cluster);
    };
  }, [cluster, absent]);

  // Resolve the issuance chain on demand when a certificate is selected.
  useEffect(() => {
    if (!cm.selectedKey || cm.cluster !== cluster) return;
    const slash = cm.selectedKey.indexOf("/");
    const ns = cm.selectedKey.slice(0, slash);
    const name = cm.selectedKey.slice(slash + 1);
    void getCertificateChain(cluster, ns, name).catch((e) => console.error("getCertificateChain", e));
  }, [cluster, cm.selectedKey, cm.cluster]);

  // AI-assist availability + the per-cluster egress decision (shared hook).
  const ai = useAIAvailability(cluster, !absent);

  const certs = cm.cluster === cluster ? cm.certificates : [];
  const issuers = cm.cluster === cluster ? cm.issuers : [];
  const orderedCerts = useMemo(
    () => [...certs].sort((a, b) => certRank(a) - certRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [certs],
  );
  const namespaces = useMemo(() => Array.from(new Set(certs.map((c) => c.namespace))).sort(), [certs]);
  const visibleCerts = useMemo(
    () => orderedCerts.filter((c) => (!namespace || c.namespace === namespace) && matchesFilter(c, filter, issuers) && matchesQuery(c, query)),
    [orderedCerts, filter, issuers, namespace, query],
  );
  const orderedIssuers = useMemo(
    () => [...issuers].sort((a, b) => issuerRank(a) - issuerRank(b) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)),
    [issuers],
  );

  // Keyboard nav (j/k/Enter/Esc, "/" focuses the filter) over the visible
  // certificates. Selection is keyed by keyOf: when the selected certificate
  // leaves the visible set (filter change) this resolves to -1 — no selection
  // — instead of silently pointing at whatever row took its index.
  const kbIdx = kbKey === null ? -1 : visibleCerts.findIndex((c) => keyOf(c) === kbKey);

  const handleSelect = useCallback((idx: number) => {
    const c = visibleCerts[idx];
    if (c) setKbKey(keyOf(c));
  }, [visibleCerts]);

  const handleActivate = useCallback((idx: number) => {
    const c = visibleCerts[idx];
    if (!c) return;
    const k = keyOf(c);
    select(cm.selectedKey === k ? null : k);
  }, [visibleCerts, cm.selectedKey, select]);

  const handleEscape = useCallback(() => {
    if (cm.selectedKey) select(null);
  }, [cm.selectedKey, select]);

  useListKeys({
    // The issuers filter renders a different list; suspend row nav there.
    count: filter === "issuers" ? 0 : visibleCerts.length,
    selected: kbIdx,
    onSelect: handleSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
    searchRef,
  });

  if (absent) {
    return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>cert-manager not detected on this cluster.</div>;
  }

  const attention = certs.filter(needsAttention).length;
  const issuing = certs.filter((c) => c.ready === "Issuing").length;
  const expiring = certs.filter((c) => expiringSoon(c) || expired(c)).length;
  const issuerProblems = certs.filter((c) => issuerProblem(c, issuers)).length;
  const ready = certs.filter((c) => c.ready === "Ready" && !expired(c)).length;
  const selected = visibleCerts.find((c) => keyOf(c) === cm.selectedKey) ?? null;
  const chain = cm.chain && keyOf(cm.chain.certificate) === cm.selectedKey ? cm.chain : null;

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16 }}>
        <div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>cert-manager</div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Certificates</div>
        </div>
        <Badge tone={attention > 0 ? "warning" : "success"}>{attention > 0 ? `${attention} need attention` : "ready"}</Badge>
      </div>

      {cm.cluster === cluster && cm.watchError && (
        <WatchErrorBanner tool="cert-manager" error={cm.watchError} />
      )}

      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(130px, 1fr))", overflow: "hidden" }}>
        <MetersStrip
          note="broken first · then soonest expiry"
          meters={[
            { label: "certificates", value: certs.length },
            attention > 0 && { label: "failing", value: attention, tone: "danger" as const },
            issuing > 0 && { label: "issuing", value: issuing, tone: "info" as const },
            expiring > 0 && { label: "expiring", value: expiring, tone: "warning" as const },
            { label: "issuers", value: issuers.length },
            { label: "ready", value: ready, tone: "success" as const },
          ]}
        />
      </div>

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
        <FilterButton label="all" active={filter === "all"} count={certs.length} onClick={() => setFilter("all")} />
        <FilterButton label="needs attention" active={filter === "attention"} count={attention} onClick={() => setFilter("attention")} />
        <FilterButton label="expiring" active={filter === "expiring"} count={expiring} onClick={() => setFilter("expiring")} />
        <FilterButton label="issuing" active={filter === "issuing"} count={issuing} onClick={() => setFilter("issuing")} />
        <FilterButton label="issuer problem" active={filter === "issuerProblem"} count={issuerProblems} onClick={() => setFilter("issuerProblem")} />
        <FilterButton label="issuers" active={filter === "issuers"} count={issuers.length} onClick={() => setFilter("issuers")} />
        <select
          value={namespace}
          onChange={(e) => setNamespace(e.target.value)}
          aria-label="filter certificate namespace"
          style={{ fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)" }}
        >
          <option value="">all namespaces</option>
          {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
        </select>
        <input
          ref={searchRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter certificates"
          aria-label="filter certificates"
          style={{ ...inputWell, marginLeft: "auto", width: 180 }}
        />
        {(visibleCerts.length !== certs.length || namespace) && filter !== "issuers" && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleCerts.length} of {certs.length}</span>}
      </div>

      {cm.loading && certs.length === 0 && issuers.length === 0 ? (
        <SkeletonRows rows={7} label="loading certificate state" />
      ) : filter === "issuers" ? (
        <IssuersPanel issuers={orderedIssuers} />
      ) : certs.length === 0 ? (
        <EmptyState
          icon={<IconCertificate size={28} stroke={1.2} />}
          title="No Certificates on this cluster."
          hint="cert-manager is installed but no Certificate objects exist yet."
        />
      ) : (
        <div style={{ flex: 1, minHeight: 0, display: "grid", gridTemplateColumns: "minmax(440px, 1.25fr) minmax(320px, 0.75fr)", gap: 12, alignItems: "stretch", overflow: "hidden" }}>
          <div style={{ ...well, overflow: "hidden", minHeight: 0, display: "flex", flexDirection: "column" }}>
            <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.3fr) minmax(120px,1fr) 96px 64px 84px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
              <span />
              <span>certificate</span>
              <span>issuer</span>
              <span>secret</span>
              <span>expiry</span>
              <span>status</span>
            </div>
            <div data-testid="cert-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleCerts.length === 0 ? (
                <EmptyState
                  title="No certificates match the current filter."
                  action={{ label: "Clear filters", onClick: () => { setFilter("all"); setQuery(""); setNamespace(""); } }}
                />
              ) : visibleCerts.map((c, i) => {
                const k = keyOf(c);
                return <CertRow key={k} c={c} open={cm.selectedKey === k} kbSelected={i === kbIdx} onClick={() => (cm.selectedKey === k ? select(null) : select(k))} />;
              })}
            </div>
            <FKeysBar
              keys={[["j k", "nav"], ["\u21B5", "inspect"], ["/", "filter"], ["esc", "close"]]}
              status={`${visibleCerts.length} of ${certs.length} \u00B7 broken first`}
            />
          </div>

          <div data-testid="cert-inspector" style={{ minHeight: 0, overflowY: "auto" }}>
            {selected ? <CertInspector c={selected} cluster={cluster} issuer={resolveIssuer(selected, issuers)} chain={chain} chainLoading={cm.chainLoading} aiReady={ai.ready} aiEgress={ai.egress} /> : <EmptyInspector attention={attention} />}
          </div>
        </div>
      )}
    </div>
  );
}

function CertRow({ c, open, kbSelected, onClick }: { c: CertificateDTO; open: boolean; kbSelected: boolean; onClick: () => void }) {
  const bad = c.ready === "Failed" || expired(c);
  const soon = expiringSoon(c);
  // Keep the kb-selected row visible - the list scrolls, there is no
  // VirtualList handle here. Optional call: jsdom has no scrollIntoView.
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (kbSelected) ref.current?.scrollIntoView?.({ block: "nearest" });
  }, [kbSelected]);
  return (
    <button
      ref={ref}
      aria-selected={kbSelected}
      onClick={onClick}
      style={{
        width: "100%", textAlign: "left", display: "grid",
        gridTemplateColumns: "3px minmax(180px,1.3fr) minmax(120px,1fr) 96px 64px 84px",
        gap: 10, alignItems: "center", padding: "7px 12px", cursor: "pointer",
        border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)",
        background: kbSelected || open ? "var(--color-background-secondary)" : "transparent", color: "var(--color-text-primary)",
        boxShadow: kbSelected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
        ...staleStyle(c.stale),
      }}
    >
      <span style={{ alignSelf: "stretch", background: bad ? "var(--color-text-danger)" : c.ready === "Issuing" ? "var(--color-text-info)" : soon ? "var(--color-text-warning)" : "transparent" }} />
      <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${c.namespace}/${c.name}`}>
        <span style={{ color: "var(--color-text-tertiary)" }}>{c.namespace}/</span>{c.name}
      </span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={`${c.issuerKind}/${c.issuerName}`}>{c.issuerName}</span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={c.secretName}>{c.secretName}</span>
      <span style={{ ...mono, fontSize: 11, color: expired(c) ? "var(--color-text-danger)" : soon ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{expiryText(c)}</span>
      <span style={{ display: "flex", alignItems: "center", gap: 5, minWidth: 0 }}>
        {c.stale ? (
          <SyncingChip generation={c.generation} observedGeneration={c.observedGeneration} />
        ) : (
          <>
            <StatusDot size={6} color={readyColor[c.ready] ?? readyColor.Unknown} />
            <span style={{ ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }}>{statusText(c)}</span>
          </>
        )}
      </span>
    </button>
  );
}

function CertInspector({ c, cluster, issuer, chain, chainLoading, aiReady, aiEgress }: { c: CertificateDTO; cluster: string; issuer: IssuerDTO | undefined; chain: CertChainDTO | null; chainLoading: boolean; aiReady: boolean; aiEgress: boolean }) {
  // Prefer the server-resolved bound issuer (chain.issuer); fall back to the one
  // resolved from the watched issuers list before the chain lands.
  const boundIssuer = chain?.issuer ?? issuer;
  const issuerBroken = boundIssuer && boundIssuer.ready !== "Ready";
  const failing = chain?.failingLink ?? null;
  const failKey = failing ? `${failing.kind}/${failing.namespace}/${failing.name}` : null;
  const requests = arrayOf(chain?.requests);
  const warnings = arrayOf(chain?.warnings);
  const dnsNames = arrayOf(c.dnsNames);
  const cause = failureHint(c, boundIssuer, failing, warnings);

  // AI "Explain this" — on-demand, read-only, reset when the selection changes.
  const [explain, setExplain] = useState<{ loading: boolean; result: AIResultDTO | null }>({ loading: false, result: null });
  // Fence against a mid-flight cert change: CertInspector is reused across
  // selections, so a slow explain from the previous cert must not land under the
  // new one. The token bumps whenever the selected cert changes.
  const explainTokenRef = useRef(0);
  useEffect(() => { explainTokenRef.current++; setExplain({ loading: false, result: null }); }, [c.namespace, c.name]);
  const runExplain = useCallback(() => {
    const token = explainTokenRef.current;
    setExplain({ loading: true, result: null });
    explainCertificate(cluster, c.namespace, c.name)
      .then((r) => {
        if (token !== explainTokenRef.current) return; // selection changed mid-flight
        setExplain({ loading: false, result: r });
        if (r.error) {
          useFleet.getState().setActionStatus({ kind: "error", message: `Explain failed: ${r.error}` });
        } else {
          // Audit the egress: a record of what was sent, to which backend.
          useFleet.getState().setActionStatus({ kind: "success", message: `AI explain — Certificate/${c.namespace}/${c.name} via ${r.backend}` });
        }
      })
      .catch((e) => {
        if (token !== explainTokenRef.current) return;
        setExplain({ loading: false, result: null });
        useFleet.getState().setActionStatus({ kind: "error", message: `Explain failed: ${String(e)}` });
      });
  }, [cluster, c.namespace, c.name]);

  return (
    <div style={{ ...well, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
      <div>
        <div style={{ display: "flex", alignItems: "center", gap: 2, marginBottom: 8 }}>
          <div style={{ ...mono, fontSize: 13, fontWeight: 500 }}>{c.namespace}/{c.name}</div>
          <CopyButton text={c.name} label={`copy certificate name ${c.name}`} />
          <ManifestButton group="cert-manager.io" kind="Certificate" namespace={c.namespace} name={c.name} />
          {aiReady && (
            <button
              onClick={runExplain}
              disabled={!aiEgress || explain.loading}
              title={aiEgress ? "Explain why this certificate is stuck (AI, read-only)" : "AI is disabled for this protected cluster — use a local backend or set ai.allowProtectedEgress"}
              style={{ marginLeft: "auto", display: "inline-flex", alignItems: "center", gap: 4, fontSize: 11, padding: "3px 8px", borderRadius: 4, cursor: aiEgress && !explain.loading ? "pointer" : "not-allowed", border: "0.5px solid var(--color-border-info)", background: "var(--color-background-info)", color: "var(--color-text-info)", opacity: aiEgress ? 1 : 0.5 }}
            >
              <IconSparkles size={12} stroke={1.5} />
              {explain.loading ? "explaining…" : "Explain"}
            </button>
          )}
        </div>
        <InspectorStatus
          tone={certTone(c)}
          label={expired(c) ? "Expired" : c.ready}
          reason={needsAttention(c) || c.ready === "Issuing" ? c.reason || undefined : expiringSoon(c) ? "renewal window" : undefined}
          detail={c.notAfterUnix > 0 && !expired(c) ? `expires ${expiryText(c)}` : undefined}
        />
      </div>

      {cause && (
        <div style={{ border: "0.5px solid var(--color-text-warning)", borderRadius: "var(--border-radius-md)", padding: "8px 10px", background: "var(--color-background-secondary)" }}>
          <div style={{ fontSize: 11, color: "var(--color-text-warning)", fontWeight: 600 }}>likely cause</div>
          <div style={{ fontSize: 11, color: "var(--color-text-secondary)", marginTop: 3 }}>{cause}</div>
        </div>
      )}

      {(explain.loading || explain.result) && (
        <InspectorSection title="Explain (AI)">
          {explain.loading ? (
            <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>asking the model…</div>
          ) : explain.result?.error ? (
            <div style={{ display: "flex", alignItems: "flex-start", gap: 5, fontSize: 11, color: "var(--color-text-danger)" }}>
              <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>{explain.result.error}</span>
            </div>
          ) : explain.result ? (
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              <div style={{ fontSize: 11, color: "var(--color-text-secondary)", whiteSpace: "pre-wrap", lineHeight: 1.5 }}>{explain.result.explanation}</div>
              <div style={{ ...mono, fontSize: 9, color: "var(--color-text-tertiary)" }}>Grounded on: {explain.result.groundedOn} · {explain.result.model}</div>
              <div style={{ fontSize: 9, color: "var(--color-text-tertiary)", fontStyle: "italic" }}>AI-generated from the resolved chain — verify before acting.</div>
            </div>
          ) : null}
        </InspectorSection>
      )}

      {/* A failing bound issuer is the headline for a stuck cert (M10 source pattern). */}
      {issuerBroken && (
        <div style={{ border: "0.5px solid var(--color-text-danger)", borderRadius: "var(--border-radius-md)", padding: "8px 10px", background: "var(--color-background-secondary)" }}>
          <div style={{ fontSize: 11, color: "var(--color-text-danger)", fontWeight: 500 }}>issuer not ready: {boundIssuer!.reason || "unknown"}</div>
          <div style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)", marginTop: 2 }}>{boundIssuer!.kind}/{boundIssuer!.name}</div>
          {boundIssuer!.message && <div style={{ fontSize: 11, color: "var(--color-text-secondary)", marginTop: 3 }}>{boundIssuer!.message}</div>}
        </div>
      )}

      <InspectorSection title={failing ? "Issuance chain — deepest stuck node flagged" : "Issuance chain"}>
        <div style={{ marginBottom: 6 }}>
          <InspectorFacts
            facts={[
              { label: "requests", value: requests.length },
              { label: "warnings", value: warnings.length, tone: warnings.length > 0 ? "warning" : undefined },
              { label: "stuck", value: failing ? chainKindLabel[failing.kind] ?? failing.kind : "—", tone: failing ? "danger" : undefined },
            ]}
          />
        </div>
        {chainLoading ? (
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>resolving chain…</div>
        ) : requests.length > 0 ? (
          <ChainRail nodes={requests} failKey={failKey} />
        ) : (
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>no active issuance request.</div>
        )}
        {warnings.length > 0 && warnings.map((w) => (
          <div key={w} style={{ fontSize: 10, color: "var(--color-text-warning)", marginTop: 3 }}>{w}</div>
        ))}
      </InspectorSection>

      <InspectorSection title="Issuer">
        <Kv label="ref" value={`${c.issuerKind}/${c.issuerName}`} />
        <Kv label="health" value={boundIssuer ? boundIssuer.ready : "not found"} tone={boundIssuer ? (boundIssuer.ready === "Ready" ? "success" : "danger") : "muted"} />
        {boundIssuer?.message && <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{boundIssuer.message}</div>}
      </InspectorSection>

      <InspectorSection title="Secret">
        <Kv
          label="name"
          value={c.secretName ? (
            <button
              onClick={() => {
                const nav = useFleet.getState();
                nav.openResource(SECRET_REF);
                nav.openInstance(c.namespace, c.secretName);
              }}
              title={`open Secret ${c.namespace}/${c.secretName}`}
              style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere", textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2 }}
            >{c.secretName}</button>
          ) : "—"}
        />
      </InspectorSection>

      <InspectorSection title="Validity">
        <Kv label="valid from" value={dateText(c.notBeforeUnix)} />
        <Kv label="expires" value={c.notAfterUnix ? `${expiryText(c)} (${dateText(c.notAfterUnix)})` : "not issued"} tone={expired(c) ? "danger" : expiringSoon(c) ? "warning" : undefined} />
        <Kv label="renews" value={dateText(c.renewalUnix)} tone={expiringSoon(c) ? "warning" : undefined} />
      </InspectorSection>

      {dnsNames.length > 0 && (
        <InspectorSection title="DNS names">
          {dnsNames.map((d) => <div key={d} style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)" }}>{d}</div>)}
        </InspectorSection>
      )}

      {c.message && (
        <InspectorSection title="Message">
          <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{c.message}</div>
        </InspectorSection>
      )}
    </div>
  );
}

// certTone maps the triage semantics onto the verdict tone: failed/expired are
// faults, issuing is activity, the renewal window is a caution, ready is quiet.
function certTone(c: CertificateDTO): Tone {
  if (expired(c) || c.ready === "Failed") return "danger";
  if (c.ready === "Issuing") return "info";
  if (expiringSoon(c)) return "warning";
  if (c.ready === "Ready") return "success";
  return "muted";
}

// Kv: label + left-adjacent value on the shared 88px grid. The Field primitive
// right-aligns its value across the full panel width, which breaks scanning.
function Kv({ label, value, tone }: { label: string; value: React.ReactNode; tone?: Tone }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "88px 1fr", gap: 8, fontSize: 11, alignItems: "baseline" }}>
      <span style={{ color: "var(--color-text-tertiary)" }}>{label}</span>
      <span style={{ ...mono, fontSize: 11, color: tone ? toneColor[tone] : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{value}</span>
    </div>
  );
}

const chainKindLabel: Record<string, string> = {
  CertificateRequest: "request",
  Order: "order",
  Challenge: "challenge",
};

function failureHint(c: CertificateDTO, issuer: IssuerDTO | undefined, failing: ChainNodeDTO | null, warnings: string[]): string {
  if (issuer && issuer.ready !== "Ready") return `${issuer.kind}/${issuer.name} is not ready${issuer.reason ? `: ${issuer.reason}` : ""}.`;
  if (!issuer) return `${c.issuerKind}/${c.issuerName} was not found in the watched issuer list.`;
  if (failing) {
    const kind = chainKindLabel[failing.kind] ?? failing.kind.toLowerCase();
    return `${kind} ${failing.name} is ${failing.ready.toLowerCase()}${failing.reason ? `: ${failing.reason}` : ""}.`;
  }
  if (c.reason === "DoesNotExist") return `The target secret ${c.secretName || "(unset)"} does not exist yet or cert-manager has not issued the certificate.`;
  if (c.ready === "Issuing") return "cert-manager is actively issuing or renewing this certificate.";
  if (warnings.length > 0) return warnings[0];
  if (expired(c)) return "The certificate is past its notAfter time.";
  return "";
}

// flattenChain walks the request tree depth-first onto one rail: the chain is
// linear in practice (request → order → challenge) and the rail reads
// top-to-bottom in the order the controllers process it.
function flattenChain(nodes: ChainNodeDTO[]): ChainNodeDTO[] {
  const out: ChainNodeDTO[] = [];
  const walk = (n: ChainNodeDTO) => {
    out.push(n);
    arrayOf(n.children).forEach(walk);
  };
  nodes.forEach(walk);
  return out;
}

// ChainRail: one vertical rail, a lamp per node. Only the deepest stuck node
// gets loud - its message in a red-ringed box; healthy links stay quiet.
function ChainRail({ nodes, failKey }: { nodes: ChainNodeDTO[]; failKey: string | null }) {
  const flat = flattenChain(nodes);
  return (
    <div style={{ position: "relative" }}>
      <span aria-hidden style={{ position: "absolute", left: 3, top: 6, bottom: 6, width: 1, background: "var(--color-border-secondary)" }} />
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        {flat.map((node) => {
          const key = `${node.kind}/${node.namespace}/${node.name}`;
          const isFail = failKey === key;
          const bad = node.ready === "Failed";
          const issuing = node.ready === "Issuing";
          const kind = node.kind || "Unknown";
          const stuckText = node.message || node.reason;
          return (
            <div key={key} style={{ position: "relative" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 7, fontSize: 11 }}>
                <StatusDot size={7} color={readyColor[node.ready] ?? readyColor.Unknown} />
                <span style={{ color: "var(--color-text-tertiary)", width: 60, flexShrink: 0 }}>{chainKindLabel[kind] ?? kind.toLowerCase()}</span>
                <span style={{ ...mono, ...ellipsis, color: isFail ? "var(--color-text-danger)" : "var(--color-text-secondary)" }} title={node.name}>{node.name}</span>
                {(bad || issuing) && node.reason && !isFail && (
                  <span style={{ ...mono, fontSize: 10, color: bad ? "var(--color-text-danger)" : "var(--color-text-warning)", flexShrink: 0 }} title={node.message}>{node.reason}</span>
                )}
                {isFail && <span style={{ fontSize: 9, padding: "0 5px", borderRadius: 3, background: "var(--color-text-danger)", color: "var(--color-background-primary)", flexShrink: 0 }}>stuck here</span>}
              </div>
              {isFail && stuckText && (
                <div style={{ marginLeft: 14, marginTop: 4, padding: "6px 9px", borderRadius: 4, background: "var(--color-background-danger)", boxShadow: "inset 0 0 0 1px var(--color-border-danger)", fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>
                  {stuckText}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function IssuersPanel({ issuers }: { issuers: IssuerDTO[] }) {
  if (issuers.length === 0) {
    return <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No issuers found.</div>;
  }
  return (
    <div style={{ ...frame, overflow: "hidden", minHeight: 0, flex: 1, display: "flex", flexDirection: "column" }}>
      <div data-testid="issuer-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {issuers.map((i) => (
          <div key={`${i.kind}/${i.namespace}/${i.name}`} style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.4fr) 110px minmax(120px,1fr)", gap: 10, alignItems: "center", padding: "7px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <span style={{ alignSelf: "stretch", background: i.ready === "Failed" ? "var(--color-text-danger)" : i.ready !== "Ready" ? "var(--color-text-warning)" : "transparent" }} />
            <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${i.kind}/${i.namespace}/${i.name}`}>
              {i.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{i.namespace}/</span>}{i.name}
            </span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{i.kind}</span>
            <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
              <StatusDot size={6} color={readyColor[i.ready] ?? readyColor.Unknown} />
              <span style={{ ...ellipsis, fontSize: 11, color: i.ready === "Failed" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }} title={i.message}>{i.ready !== "Ready" && i.reason ? i.reason : i.ready.toLowerCase()}</span>
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function EmptyInspector({ attention }: { attention: number }) {
  return (
    <div style={{ ...well, padding: 18, color: "var(--color-text-secondary)", fontSize: 12, height: "100%", boxSizing: "border-box" }}>
      {attention > 0 ? "Select a certificate to inspect its issuer and validity." : "All certificates are ready. Select one to inspect it."}
    </div>
  );
}
