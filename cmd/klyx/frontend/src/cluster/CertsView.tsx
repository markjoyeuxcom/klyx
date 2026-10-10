import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO, ChainNodeDTO, CertChainDTO } from "../store/fleet";
import { openCertManager, closeCertManager, getCertificateChain } from "../bridge/certmanager";
import { explainCertificate } from "../bridge/ai";
import { useAIAvailability } from "./useAI";
import type { AIResultDTO } from "../store/fleet";
import { useListKeys } from "../chrome/useListKeys";
import { StatusDot, mono, readyColor, type Tone } from "../chrome/primitives";
import { InspectorStatus } from "./ResourceInspector";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { IconCertificate, IconAlertTriangle } from "@tabler/icons-react";
import { SECRET_REF, wellKnownResourceRef } from "./resourceRefs";
import { SyncingChip, staleStyle } from "./staleness";
import { VerdictLamp } from "../health/VerdictView";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";

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

function matchesQuery(c: CertificateDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [c.namespace, c.name, c.issuerName, c.secretName, c.reason, ...arrayOf(c.dnsNames)].some((v) => v.toLowerCase().includes(q));
}

// CertsView - the cert-manager lens on the workspace template (spec §6 step
// 4c). Two views of one lens: Certificates (broken first, then soonest
// expiry) and Issuers. Read-only diagnosis, so the documented domain variant
// of the action band is: Re-resolve chain (primary - re-reads the issuance
// chain), Explain (secondary, AI, only when usable here), More = manifest /
// Secret. The bound issuer's health is the headline for a stuck certificate.

type CertView = "certificates" | "issuers";
type CertStatus = "all" | "attention" | "expiring" | "issuing" | "issuerProblem";

// certHealth maps cert-manager state onto the shared verdict roles (only
// faults glow): Failed or expired = fault; Issuing or an unobserved spec =
// progressing; Unknown = unknown; other not-Ready = degraded; Ready = ok. The
// renewal window is cert-manager's normal automatic renewal - not attention;
// the expiry cell carries it in amber text.
function certHealth(c: CertificateDTO): { kind: string; label: string } {
  // absolute expiry outranks an unobserved spec: an expired certificate is a
  // fault whatever generation the controller has caught up to
  if (expired(c)) return { kind: "fault", label: "Expired" };
  if (c.stale) return { kind: "progressing", label: "Syncing…" };
  if (c.ready === "Failed") return { kind: "fault", label: "Failed" };
  if (c.ready === "Issuing") return { kind: "progressing", label: "Issuing" };
  if (c.ready === "Ready") return { kind: "ok", label: "Ready" };
  if (c.ready === "Unknown" || !c.ready) return { kind: "unknown", label: "Unknown" };
  return { kind: "degraded", label: c.ready };
}
function issuerHealth(i: IssuerDTO): { kind: string; label: string } {
  if (i.ready === "Ready") return { kind: "ok", label: "Ready" };
  if (i.ready === "Failed") return { kind: "fault", label: "Failed" };
  if (i.ready === "Unknown" || !i.ready) return { kind: "unknown", label: "Unknown" };
  return { kind: "degraded", label: i.ready };
}
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
};
function Lamp({ role }: { role: { kind: string; label: string } }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const issuerKey = (i: IssuerDTO) => `${i.kind}/${i.namespace}/${i.name}`;
type JoinCoverage = "complete" | "stale" | "unknown";

const CERT_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Certificate", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 1 },
  { id: "expiry", label: "Expires", width: 72, priority: 1 },
  { id: "issuer", label: "Issuer", width: 150, priority: 2, grow: 0.6 },
  { id: "secret", label: "Secret", width: 140, priority: 3, grow: 0.5 },
];
const ISSUER_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Issuer", width: 240, priority: 0, flex: true },
  { id: "kind", label: "Kind", width: 100, priority: 2 },
  { id: "status", label: "Status", width: 220, priority: 1, grow: 0.8 },
  { id: "certs", label: "Certificates", width: 92, priority: 3 },
];

export function CertsView({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.certManagerTier ?? "Unknown");
  const cm = useFleet((s) => s.certmanager);
  const selectCert = useFleet((s) => s.selectCertificate);
  const [view, setView] = useState<CertView>("certificates");
  const [status, setStatus] = useState<CertStatus>("all");
  const [query, setQuery] = useState("");
  const [namespace, setNamespace] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [issuerSel, setIssuerSel] = useState<string | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const absent = tier === "Absent";

  useEffect(() => {
    if (absent) return;
    // If cleanup runs before openCertManager resolves, invoke the late-arriving
    // unsubscriber immediately - otherwise its listener leaks.
    let cancelled = false;
    let off = () => {};
    openCertManager(cluster).then((u) => (cancelled ? u() : (off = u))).catch((e) => console.error("openCertManager", e));
    return () => {
      cancelled = true;
      off();
      void closeCertManager(cluster);
    };
  }, [cluster, absent]);

  const mine = cm.cluster === cluster;
  const certs = useMemo(() => (mine ? cm.certificates : []), [mine, cm.certificates]);
  const issuers = useMemo(() => (mine ? cm.issuers : []), [mine, cm.issuers]);
  const watchError = mine ? cm.watchError : null;
  const loaded = mine && !cm.loading;
  const complete = loaded && !watchError;
  // Evidence per kind: a synced Certificate watch never vouches for a pending
  // or forbidden Issuer/ClusterIssuer watch (or the reverse). Older payloads
  // without coverage fall back to the aggregate state.
  const kindCov = useCallback((kind: string): JoinCoverage => {
    if (!loaded) return "unknown";
    const k = (cm.coverage ?? []).find((x) => x.kind === kind);
    if (!k) return watchError ? "stale" : "complete";
    if (!k.served) return "complete"; // not installed: definitively none
    if (!k.synced) return "unknown";
    return k.error ? "stale" : "complete";
  }, [loaded, cm.coverage, watchError]);
  const certsCov = kindCov("Certificate");
  const issuerCovOf = useCallback((kind: string) => kindCov(kind), [kindCov]);

  // Re-read the chain when the selection changes (and on demand).
  const selKey = mine ? cm.selectedKey : null;
  const resolveChain = useCallback((key: string) => {
    const slash = key.indexOf("/");
    void getCertificateChain(cluster, key.slice(0, slash), key.slice(slash + 1)).catch((e) => console.error("getCertificateChain", e));
  }, [cluster]);
  useEffect(() => { if (selKey) resolveChain(selKey); }, [selKey, resolveChain]);

  const ai = useAIAvailability(cluster, !absent);

  const q = query.trim().toLowerCase();
  const orderedCerts = useMemo(
    () => [...certs].sort((a, b) => certRank(a) - certRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [certs],
  );
  const namespaces = useMemo(() => Array.from(new Set(certs.map((c) => c.namespace))).sort(), [certs]);
  const passesStatus = useCallback((c: CertificateDTO) => {
    if (status === "attention") return needsAttention(c);
    if (status === "expiring") return expiringSoon(c) || expired(c);
    if (status === "issuing") return c.ready === "Issuing";
    if (status === "issuerProblem") return issuerCovOf(c.issuerKind) !== "unknown" && issuerProblem(c, issuers);
    return true;
  }, [status, issuers, issuerCovOf]);
  const visibleCerts = useMemo(
    () => orderedCerts.filter((c) => (!namespace || c.namespace === namespace) && passesStatus(c) && matchesQuery(c, q)),
    [orderedCerts, namespace, passesStatus, q],
  );
  const orderedIssuers = useMemo(
    () => [...issuers].sort((a, b) => issuerRank(a) - issuerRank(b) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)),
    [issuers],
  );
  const visibleIssuers = useMemo(() => orderedIssuers.filter((i) => {
    if (namespace && i.kind === "Issuer" && i.namespace !== namespace) return false;
    if (status === "attention" && i.ready === "Ready") return false;
    return !q || [i.kind, i.namespace, i.name, i.reason, i.message].some((v) => (v ?? "").toLowerCase().includes(q));
  }), [orderedIssuers, namespace, status, q]);
  const certsFor = useCallback((i: IssuerDTO) => certs.filter((c) => resolveIssuer(c, [i]) === i), [certs]);

  // Selection lifecycle: certificates use the store's selectedKey (the chain
  // reader keys on it); issuers are local. Filter exclusion closes; absent
  // from a complete read = removed (last evidence, read-only).
  const selectedCert = selKey ? certs.find((c) => keyOf(c) === selKey) : undefined;
  const lastCert = useRef<CertificateDTO | null>(null);
  if (selectedCert) lastCert.current = selectedCert;
  const certRemoved = !!selKey && complete && !selectedCert;
  const liveChain = mine && cm.chain && cm.chainCluster === cluster && keyOf(cm.chain.certificate) === selKey ? cm.chain : null;
  const lastChain = useRef<{ key: string; chain: CertChainDTO } | null>(null);
  if (liveChain && selKey) lastChain.current = { key: `${cluster}/${selKey}`, chain: liveChain };
  const shownChain = liveChain ?? (certRemoved && lastChain.current?.key === `${cluster}/${selKey}` ? lastChain.current.chain : null);
  const selectedIssuer = issuerSel ? issuers.find((i) => issuerKey(i) === issuerSel) : undefined;
  const lastIssuer = useRef<IssuerDTO | null>(null);
  if (selectedIssuer) lastIssuer.current = selectedIssuer;
  const issuerRemoved = !!issuerSel && complete && !selectedIssuer;
  useEffect(() => { if (selectedCert && view === "certificates" && !visibleCerts.includes(selectedCert)) selectCert(null); }, [selectedCert, visibleCerts, view, selectCert]);
  useEffect(() => { if (selectedIssuer && view === "issuers" && !visibleIssuers.includes(selectedIssuer)) setIssuerSel(null); }, [selectedIssuer, visibleIssuers, view]);
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    setIssuerSel(null); setCursorKey(null);
    autoSelect.current = "pending";
    entryCerts.current = useFleet.getState().certmanager.certificates;
  }, [cluster]);

  // One-shot auto-select of the first certificate needing attention.
  const autoSelect = useRef<"pending" | "done">("pending");
  const entryCerts = useRef<CertificateDTO[] | null>(null);
  if (entryCerts.current === null) entryCerts.current = cm.certificates;
  const freshRead = complete && cm.certificates !== entryCerts.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead || view !== "certificates") return;
    autoSelect.current = "done";
    if (useFleet.getState().certmanager.selectedKey) return;
    const c = visibleCerts.find(needsAttention);
    if (c) { setCursorKey(keyOf(c)); selectCert(keyOf(c)); }
  }, [freshRead, visibleCerts, view, selectCert]);

  // Keyboard: j/k/Enter/Esc in both views.
  const nav: { key: string; open: () => void }[] = view === "issuers"
    ? visibleIssuers.map((i) => ({ key: issuerKey(i), open: () => setIssuerSel(issuerKey(i)) }))
    : visibleCerts.map((c) => ({ key: keyOf(c), open: () => { autoSelect.current = "done"; selectCert(keyOf(c)); } }));
  const cursorIdx = cursorKey === null ? -1 : nav.findIndex((n) => n.key === cursorKey);
  const closeSelected = () => { autoSelect.current = "done"; if (view === "issuers") setIssuerSel(null); else selectCert(null); };
  useListKeys({
    count: nav.length,
    selected: cursorIdx,
    onSelect: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); n.open(); } },
    onEscape: closeSelected,
    searchRef,
  });

  const columns = view === "issuers" ? ISSUER_COLUMNS : CERT_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const attention = certs.filter(needsAttention).length;
  const failedOrExpired = certs.filter((c) => c.ready === "Failed" || expired(c)).length;
  const issuing = certs.filter((c) => c.ready === "Issuing").length;
  const renewing = certs.filter((c) => expiringSoon(c)).length;
  const issuerProblems = certs.filter((c) => issuerCovOf(c.issuerKind) !== "unknown" && issuerProblem(c, issuers)).length;
  const issuersNotReady = issuers.filter((i) => i.ready !== "Ready").length;

  const clearFilters = () => { setStatus("all"); setQuery(""); setNamespace(""); };
  const total = view === "issuers" ? issuers.length : certs.length;
  const shown = view === "issuers" ? visibleIssuers.length : visibleCerts.length;
  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (!loaded && total === 0) state = "loading";
  else if (watchError && total === 0) state = "unreadable";
  else if (total === 0) state = "empty";
  else if (shown === 0) state = "no-match";
  const noun = view === "issuers" ? "issuers" : "certificates";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={7} label="loading certificate state" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="cert-manager state could not be read." hint={watchError ?? undefined} />
    : state === "empty" ? (absent
      ? <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title="cert-manager is not installed on this cluster." hint="Klyx detected no cert-manager.io resources here." />
      : <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title={view === "issuers" ? "No issuers on this cluster." : "No Certificates on this cluster."} hint={view === "issuers" ? undefined : "cert-manager is installed but no Certificate objects exist yet."} />)
    : <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title={`No ${noun} match the current filters.`} hint={`${total} ${noun} on this cluster.`} action={{ label: "Clear filters", onClick: clearFilters }} />;
  const partial = watchError ? "+" : "";

  return (
    <LensLayout
      label="cert-manager"
      title={view === "issuers" ? "cert-manager issuers" : "Certificates"}
      summary={state === "loading" || state === "unreadable" || absent ? null : view === "issuers" ? (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{issuers.length}{partial}</span>
          {issuersNotReady > 0 ? <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{issuersNotReady} not ready</span> : <span className="t-secondary">all issuers ready</span>}
        </>
      ) : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{certs.length}{partial}</span>
          {attention > 0
            ? <span className="t-status" style={{ color: failedOrExpired > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{failedOrExpired > 0 ? "▲ " : ""}{attention} not ready or expired</span>
            : <span className="t-secondary">{watchError ? "none known not ready" : "all ready"}</span>}
          <span className="t-secondary">{[failedOrExpired ? `${failedOrExpired} failed or expired` : "", issuing ? `${issuing} issuing` : "", renewing ? `${renewing} in renewal window` : "", issuerProblems ? `${issuerProblems} with issuer problem` : ""].filter(Boolean).join(" · ")}</span>
        </>
      )}
      controls={absent ? undefined : (
        <>
          <div role="group" aria-label="cert-manager view" style={{ display: "inline-flex", gap: 4 }}>
            <button className="l-chip" aria-pressed={view === "certificates"} onClick={() => { setView("certificates"); setStatus("all"); }}>Certificates</button>
            <button className="l-chip" aria-pressed={view === "issuers"} onClick={() => { setView("issuers"); setStatus("all"); }}>
              Issuers {issuersNotReady > 0 && <span style={{ color: "var(--color-text-warning)" }}>{issuersNotReady}</span>}
            </button>
          </div>
          <label style={{ display: "flex" }}>
            <span className="sr-only">Namespace</span>
            <select className="l-input" value={namespace} onChange={(e) => setNamespace(e.target.value)}>
              <option value="">All namespaces</option>
              {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
            </select>
          </label>
          <button className="l-chip" aria-pressed={status === "all"} onClick={() => setStatus("all")}>All</button>
          <button className="l-chip" aria-pressed={status === "attention"} onClick={() => setStatus("attention")}>Not ready {(view === "issuers" ? issuersNotReady : attention) > 0 && <span style={{ color: "var(--color-text-warning)" }}>{view === "issuers" ? issuersNotReady : attention}</span>}</button>
          {view === "certificates" && (
            <>
              <button className="l-chip" aria-pressed={status === "expiring"} onClick={() => setStatus("expiring")}>Expiring <span style={{ color: "var(--color-text-tertiary)" }}>{certs.filter((c) => expiringSoon(c) || expired(c)).length}</span></button>
              {issuing > 0 && <button className="l-chip" aria-pressed={status === "issuing"} onClick={() => setStatus("issuing")}>Issuing <span style={{ color: "var(--color-text-tertiary)" }}>{issuing}</span></button>}
              {issuerProblems > 0 && <button className="l-chip" aria-pressed={status === "issuerProblem"} onClick={() => setStatus("issuerProblem")}>Issuer problem <span style={{ color: "var(--color-text-warning)" }}>{issuerProblems}</span></button>}
            </>
          )}
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search cert-manager</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={view === "issuers" ? "Search issuers" : "Search name, issuer, DNS, secret"} />
          </label>
        </>
      )}
      notice={watchError && total > 0 ? <WatchErrorBanner tool="cert-manager" failure="watch failed - showing the last good state" error={watchError} /> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          {view === "issuers" ? (
            <VirtualList
              ref={listRef}
              items={visibleIssuers}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(i) => issuerKey(i)}
              style={{ flex: 1, minHeight: 0 }}
              render={(i) => {
                const k = issuerKey(i);
                const role = issuerHealth(i);
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === issuerSel ? "true" : undefined}
                    onClick={() => { setCursorKey(k); setIssuerSel(k === issuerSel ? null : k); }}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setIssuerSel(k); } }}>
                    {show("lamp") && <Lamp role={role} />}
                    {show("name") && <span className="lens-cell-name" title={`${i.namespace ? `${i.namespace}/` : ""}${i.name}`}>{i.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{i.namespace} / </span>}{i.name}</span>}
                    {show("kind") && <span style={{ color: "var(--color-text-tertiary)" }}>{i.kind}</span>}
                    {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind] }}>{role.kind === "fault" ? "▲ " : ""}{i.ready}{i.ready !== "Ready" && i.reason ? ` · ${i.reason}` : ""}</span>}
                    {show("certs") && <span title={certsCov === "unknown" ? "Certificate read unavailable" : certsCov === "stale" ? "from the last observed Certificate list" : undefined}>{certsCov === "unknown" ? "?" : `${certsFor(i).length}${certsCov === "stale" ? "*" : ""}`}</span>}
                  </div>
                );
              }}
            />
          ) : (
            <VirtualList
              ref={listRef}
              items={visibleCerts}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(c) => keyOf(c)}
              style={{ flex: 1, minHeight: 0 }}
              render={(c) => {
                const k = keyOf(c);
                const role = certHealth(c);
                const soon = expiringSoon(c);
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" style={{ ...gridStyle, ...staleStyle(c.stale && !expired(c)) }}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                    onClick={() => { setCursorKey(k); autoSelect.current = "done"; selectCert(k === selKey ? null : k); }}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selectCert(k); } }}>
                    {show("lamp") && <Lamp role={role} />}
                    {show("name") && (
                      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                        <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{c.namespace} / </span><span style={{ fontWeight: k === selKey ? 600 : 400 }}>{c.name}</span></span>
                        <CopyButton text={c.name} label={`copy certificate name ${c.name}`} appearOnRowHover />
                      </span>
                    )}
                    {show("status") && (
                      <span style={cellStyle}>
                        {c.stale && !expired(c) ? <SyncingChip generation={c.generation} observedGeneration={c.observedGeneration} />
                          : <span style={{ color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{needsAttention(c) && c.reason ? ` · ${c.reason}` : ""}</span>}
                      </span>
                    )}
                    {show("expiry") && <span style={{ color: expired(c) ? "var(--color-text-danger)" : soon ? "var(--color-text-warning)" : "var(--color-text-secondary)" }} title={c.notAfterUnix ? dateText(c.notAfterUnix) : undefined}>{expiryText(c)}</span>}
                    {show("issuer") && <span style={{ ...cellStyle, color: issuerCovOf(c.issuerKind) !== "unknown" && issuerProblem(c, issuers) ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{c.issuerName}</span>}
                    {show("secret") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{c.secretName || "-"}</span>}
                  </div>
                );
              }}
            />
          )}
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{shown} of {total}{partial} {noun}{view === "certificates" ? " · broken first, then soonest expiry" : ""}</span>}
      inspector={view === "certificates" && selKey && (selectedCert || certRemoved) && lastCert.current ? (
        <CertInspector
          key={`${cluster}/${selKey}`}
          c={selectedCert ?? lastCert.current}
          cluster={cluster}
          removed={certRemoved}
          issuer={issuerCovOf((selectedCert ?? lastCert.current).issuerKind) !== "unknown" ? resolveIssuer(selectedCert ?? lastCert.current, issuers) : undefined}
          issuersKnown={issuerCovOf((selectedCert ?? lastCert.current).issuerKind) !== "unknown"}
          issuerCov={issuerCovOf((selectedCert ?? lastCert.current).issuerKind)}
          chain={shownChain}
          chainLoading={!shownChain && mine && cm.chainLoading}
          aiReady={ai.ready}
          aiEgress={ai.egress}
          onClose={() => { autoSelect.current = "done"; selectCert(null); }}
          onResolve={() => selKey && resolveChain(selKey)}
          onOpenIssuer={(i) => { setView("issuers"); setStatus("all"); setIssuerSel(issuerKey(i)); setCursorKey(issuerKey(i)); }}
        />
      ) : view === "issuers" && issuerSel && (selectedIssuer || issuerRemoved) && lastIssuer.current ? (
        <IssuerInspector
          key={`${cluster}/${issuerSel}`}
          issuer={selectedIssuer ?? lastIssuer.current}
          removed={issuerRemoved}
          certs={certsFor(selectedIssuer ?? lastIssuer.current)}
          certsCov={certsCov}
          onClose={() => setIssuerSel(null)}
          onOpenCert={(c) => { setView("certificates"); setStatus("all"); setNamespace(""); setQuery(""); setCursorKey(keyOf(c)); autoSelect.current = "done"; selectCert(keyOf(c)); }}
        />
      ) : undefined}
      inspectorLabel={view === "issuers" ? "Issuer details" : "Certificate details"}
      onCloseInspector={closeSelected}
    />
  );
}

function RemovedNotice({ kind }: { kind: string }) {
  return (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">{kind} removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
    </div>
  );
}

// CertInspector - investigation order: the cert verdict; the bound issuer
// (headline when not ready), the likely cause, the issuance chain with the
// deepest stuck node flagged; then secret, validity, DNS names, message.
function CertInspector({ c, cluster, removed, issuer, issuersKnown, issuerCov, chain, chainLoading, aiReady, aiEgress, onClose, onResolve, onOpenIssuer }: {
  c: CertificateDTO;
  cluster: string;
  removed: boolean;
  issuer: IssuerDTO | undefined;
  issuersKnown: boolean;
  issuerCov: JoinCoverage;
  chain: CertChainDTO | null;
  chainLoading: boolean;
  aiReady: boolean;
  aiEgress: boolean;
  onClose: () => void;
  onResolve: () => void;
  onOpenIssuer: (i: IssuerDTO) => void;
}) {
  // Prefer the server-resolved bound issuer (chain.issuer); fall back to the
  // watched issuer list before the chain lands.
  const boundIssuer = chain?.issuer ?? issuer;
  const issuerRole = boundIssuer ? issuerHealth(boundIssuer) : null;
  // only CONFIRMED adverse issuer health is a diagnosed root cause; Unknown is
  // unverified evidence, shown muted
  const issuerBroken = !!issuerRole && (issuerRole.kind === "fault" || issuerRole.kind === "degraded");
  const failing = chain?.failingLink ?? null;
  const failKey = failing ? `${failing.kind}/${failing.namespace}/${failing.name}` : null;
  const requests = arrayOf(chain?.requests);
  const warnings = arrayOf(chain?.warnings);
  const dnsNames = arrayOf(c.dnsNames);
  const cause = !boundIssuer && !issuersKnown ? "" : failureHint(c, boundIssuer, failing, warnings);
  const live = !removed;
  const role = certHealth(c);

  const [explain, setExplain] = useState<{ loading: boolean; result: AIResultDTO | null }>({ loading: false, result: null });
  const explainTokenRef = useRef(0);
  useEffect(() => { explainTokenRef.current++; setExplain({ loading: false, result: null }); }, [cluster, c.namespace, c.name]);
  const runExplain = () => {
    const token = explainTokenRef.current;
    setExplain({ loading: true, result: null });
    explainCertificate(cluster, c.namespace, c.name)
      .then((r) => {
        if (token !== explainTokenRef.current) return;
        setExplain({ loading: false, result: r });
        if (r.error) useFleet.getState().setActionStatus({ kind: "error", message: `Explain failed: ${r.error}` });
        else useFleet.getState().setActionStatus({ kind: "success", message: `AI explain - Certificate/${c.namespace}/${c.name} via ${r.backend}` });
      })
      .catch((e) => {
        if (token !== explainTokenRef.current) return;
        setExplain({ loading: false, result: null });
        useFleet.getState().setActionStatus({ kind: "error", message: `Explain failed: ${String(e)}` });
      });
  };
  const aiBlocked = "AI is disabled for this protected cluster - the AI endpoint is not on this machine; use a model on localhost or set ai.allowProtectedEgress";
  const manifestRef = wellKnownResourceRef("cert-manager.io", "Certificate");
  const more: MenuItem[] = [
    ...(manifestRef ? [{ label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(c.namespace, c.name); } }] : []),
    ...(c.secretName ? [{ label: `Open Secret ${c.secretName}`, onSelect: () => { const nav = useFleet.getState(); nav.openResource(SECRET_REF); nav.openInstance(c.namespace, c.secretName); } }] : []),
    ...(live && aiReady && !aiEgress ? [{ label: "Explain (AI)", disabled: true, hint: aiBlocked, onSelect: () => {} }] : []),
  ];

  return (
    <div data-testid="cert-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind="Certificate"
        namespace={c.namespace}
        name={c.name}
        meta={[`issuer ${c.issuerKind}/${c.issuerName}`, c.secretName ? `secret ${c.secretName}` : ""].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="Certificate" /> : (
          <div>
            {c.stale && !expired(c) ? (
              <InspectorStatus tone="muted" label="Syncing…" reason={`generation ${c.generation} not yet observed (observed ${c.observedGeneration})`} />
            ) : (
              <InspectorStatus
                tone={roleTone(role.kind)}
                label={role.label}
                reason={needsAttention(c) || c.ready === "Issuing" ? c.reason || undefined : expiringSoon(c) ? "renewal window" : undefined}
                detail={c.notAfterUnix > 0 && !expired(c) ? `expires ${expiryText(c)}` : undefined}
              />
            )}
            {c.stale && expired(c) && (
              <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>
                Spec generation {c.generation} not yet observed (observed {c.observedGeneration}) - the expiry above is current regardless.
              </div>
            )}
            {c.message && needsAttention(c) && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: c.stale && expired(c) ? 2 : -4 }}>{c.message}</div>}
          </div>
        )}
        primary={live ? { label: chainLoading ? "Resolving chain…" : "Re-resolve chain", onSelect: onResolve } : undefined}
        secondary={live && aiReady && aiEgress ? { label: explain.loading ? "Explaining…" : "Explain", onSelect: () => { if (!explain.loading) runExplain(); } } : undefined}
        more={more}
      >
        {issuerBroken && (
          <div className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: issuerRole!.kind === "fault" ? "var(--color-background-danger)" : "var(--color-background-warning)", boxShadow: `inset 0 0 0 1px ${issuerRole!.kind === "fault" ? "var(--color-border-danger)" : "var(--color-border-warning)"}` }}>
            <div style={{ color: ROLE_TEXT[issuerRole!.kind], fontWeight: 600 }}>{issuerRole!.kind === "fault" ? "Issuer failed" : "Issuer not ready"}: {boundIssuer!.reason || "unknown"}</div>
            <button onClick={() => onOpenIssuer(boundIssuer!)} className="t-identifier" style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", marginTop: 2 }}>{boundIssuer!.kind}/{boundIssuer!.name}</button>
            {boundIssuer!.message && <div style={{ color: "var(--color-text-secondary)", marginTop: 3, overflowWrap: "anywhere" }}>{boundIssuer!.message}</div>}
          </div>
        )}
        {cause && !issuerBroken && (
          <div className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <div style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>Likely cause</div>
            <div style={{ color: "var(--color-text-secondary)", marginTop: 3, overflowWrap: "anywhere" }}>{cause}</div>
          </div>
        )}
        {(explain.loading || explain.result) && (
          <InspectorBlock title="Explain (AI)">
            {explain.loading ? <span className="t-secondary">Asking the model…</span> : explain.result?.error ? (
              <div className="t-body" style={{ display: "flex", alignItems: "flex-start", gap: 5, color: "var(--color-text-danger)" }}>
                <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
                <span>{explain.result.error}</span>
              </div>
            ) : explain.result ? (
              <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                <div className="t-body" style={{ color: "var(--color-text-secondary)", whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{explain.result.explanation}</div>
                <span className="t-secondary">Grounded on: {explain.result.groundedOn} · {explain.result.model}</span>
                <span className="t-secondary" style={{ fontStyle: "italic" }}>AI-generated from the resolved chain - verify before acting.</span>
              </div>
            ) : null}
          </InspectorBlock>
        )}
        <InspectorBlock title={failing ? "Issuance chain - deepest stuck node flagged" : "Issuance chain"} count={chain ? requests.length : undefined}>
          {chainLoading ? <span className="t-secondary">Resolving chain…</span>
            : !chain ? <span className="t-secondary">{removed ? "No chain was resolved before it was removed." : "Chain not resolved."}</span>
            : requests.length > 0 ? <ChainRail nodes={requests} failKey={failKey} />
            : <span className="t-secondary">No active issuance request.</span>}
          {warnings.map((w) => <div key={w} className="t-body" style={{ color: "var(--color-text-warning)", marginTop: 3, overflowWrap: "anywhere" }}>{w}</div>)}
        </InspectorBlock>
        <InspectorBlock title="Issuer">
          <div className="insp-kv">
            <span>ref</span><span className="t-identifier">{c.issuerKind}/{c.issuerName}</span>
            <span>health</span>
            <span style={{ color: issuerRole ? ROLE_TEXT[issuerRole.kind] : "var(--color-text-tertiary)" }}>
              {boundIssuer
                ? `${issuerRole!.label}${issuerRole!.kind === "unknown" ? " (unverified)" : ""}${issuerCov === "stale" ? " (last observed)" : ""}`
                : issuerCov === "unknown" ? `unverified (${c.issuerKind} read unavailable)` : issuerCov === "stale" ? `not found in the last observed ${c.issuerKind} list` : `not found in the watched ${c.issuerKind} list`}
            </span>
          </div>
        </InspectorBlock>
        <InspectorBlock title="Validity">
          <div className="insp-kv">
            <span>valid from</span><span>{dateText(c.notBeforeUnix)}</span>
            <span>expires</span><span style={{ color: expired(c) ? "var(--color-text-danger)" : expiringSoon(c) ? "var(--color-text-warning)" : undefined }}>{c.notAfterUnix ? `${expiryText(c)} (${dateText(c.notAfterUnix)})` : "not issued"}</span>
            <span>renews</span><span style={{ color: expiringSoon(c) ? "var(--color-text-warning)" : undefined }}>{dateText(c.renewalUnix)}</span>
          </div>
        </InspectorBlock>
        {dnsNames.length > 0 && (
          <InspectorBlock title="DNS names" count={dnsNames.length}>
            {dnsNames.map((d) => <div key={d} className="t-identifier" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{d}</div>)}
          </InspectorBlock>
        )}
        {c.message && !needsAttention(c) && (
          <InspectorBlock title="Message" defaultOpen={false}>
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{c.message}</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

function IssuerInspector({ issuer, removed, certs, certsCov, onClose, onOpenCert }: { issuer: IssuerDTO; removed: boolean; certs: CertificateDTO[]; certsCov: JoinCoverage; onClose: () => void; onOpenCert: (c: CertificateDTO) => void }) {
  const role = issuerHealth(issuer);
  const ref = wellKnownResourceRef("cert-manager.io", issuer.kind);
  const view = ref ? () => { const nav = useFleet.getState(); nav.openResource(ref); nav.openInstance(issuer.kind === "ClusterIssuer" ? "" : issuer.namespace, issuer.name); } : null;
  const blocked = certs.filter(needsAttention);
  return (
    <div data-testid="issuer-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind={issuer.kind}
        namespace={issuer.namespace || undefined}
        name={issuer.name}
        meta={issuer.kind === "ClusterIssuer" ? "cluster-scoped - any namespace may use it" : `namespaced - serves Certificates in ${issuer.namespace}`}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind={issuer.kind} /> : (
          <div>
            <InspectorStatus tone={role.kind === "fault" ? "danger" : role.kind === "degraded" ? "warning" : role.kind === "ok" ? "success" : "muted"} label={role.label} reason={issuer.ready !== "Ready" ? issuer.reason || undefined : undefined} />
            {issuer.message && issuer.ready !== "Ready" && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{issuer.message}</div>}
          </div>
        )}
        primary={view ? { label: "View manifest in Resources", onSelect: view } : undefined}
      >
        <InspectorBlock title="Certificates using it" count={certsCov === "unknown" ? undefined : certs.length} extra={blocked.length > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{blocked.length} not ready or expired</span> : undefined}>
          {certsCov === "stale" && <span className="t-secondary">From the last observed Certificate list; the watch is currently failing.</span>}
          {certsCov === "unknown" ? <span className="t-secondary">Certificate read unavailable - usage unverified, not zero.</span>
            : certs.length === 0 ? <span className="t-secondary">No Certificate references this issuer.</span> : (
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {[...blocked, ...certs.filter((c) => !blocked.includes(c))].map((c) => (
                <button key={keyOf(c)} onClick={() => onOpenCert(c)} className="t-identifier"
                  style={{ display: "flex", gap: 8, alignItems: "center", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>
                  <VerdictLamp health={certHealth(c)} size={7} />
                  <span style={{ overflowWrap: "anywhere" }}>{c.namespace}/{c.name}</span>
                </button>
              ))}
            </div>
          )}
        </InspectorBlock>
        {issuer.message && issuer.ready === "Ready" && (
          <InspectorBlock title="Message" defaultOpen={false}>
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{issuer.message}</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

// roleTone: the verdict tone from the shared role - one mapping for rows,
// headlines and messages (the renewal window stays a separate expiry fact).
function roleTone(kind: string): Tone {
  return kind === "fault" ? "danger" : kind === "degraded" ? "warning" : kind === "ok" ? "success" : "muted";
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
                <span style={{ ...mono, overflowWrap: "anywhere", minWidth: 0, color: isFail ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>{node.name}</span>
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

