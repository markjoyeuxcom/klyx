import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavIntent } from "../chrome/useNavIntent";
import { kindOfRef } from "../nav/reveal";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO, ChainNodeDTO, CertChainDTO, RuleDTO } from "../store/fleet";
import { getCertificateChain } from "../bridge/certmanager";
import { useCertManagerRows } from "../health/useCertManagerRows";
import { useInspectorAI } from "../ai/AIExplain";
import { useListKeys } from "../chrome/useListKeys";
import { Aspect, aspectOfVerdict, mono, Rail, type AspectKind, type RailStep, type Tone } from "../chrome/primitives";
import { InspectorStatus } from "./ResourceInspector";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { IconCertificate, IconAlertTriangle } from "@tabler/icons-react";
import { SECRET_REF, wellKnownResourceRef } from "./resourceRefs";
import { SyncingChip, staleProps } from "./staleness";
import { ROW_MARK, VerdictLamp, VerdictMark } from "../health/VerdictView";
import { CountSentence } from "../layout/CountSentence";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { clock, combineEvidence, coverageGaps, joinCoverage, kindEvidence, lastGoodAt, type JoinCoverage, type KindEvidence } from "../layout/coverage";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

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
  return !issuer || issuer.rule.included;
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

// The verdict is the relocated rule computed in Go (1c addendum §2):
// rule.included is the attention filter, rule.role/label the presentation.
// A Ready cert merely in its renewal window is not attention - cert-manager
// renews it; the expiry cell carries it in amber text.
const roleOf = (x: { rule: RuleDTO }) => ({ kind: x.rule.role, label: x.rule.label });
const included = (x: { rule: RuleDTO }) => x.rule.included;
// faultsOf: the counted rows that are faults - the rows' reading, used only
// where no summary count is current (slice 11b review).
const faultsOf = (xs: { rule: RuleDTO }[]) => xs.filter((x) => x.rule.included && x.rule.role === "fault").length;

const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
};
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const issuerKey = (i: IssuerDTO) => `${i.kind}/${i.namespace}/${i.name}`;

const CERT_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "name", label: "Certificate", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 1 },
  { id: "expiry", label: "Expires", width: 72, priority: 1 },
  { id: "issuer", label: "Issuer", width: 150, priority: 2, grow: 0.6 },
  { id: "secret", label: "Secret", width: 140, priority: 3, grow: 0.5 },
];
const ISSUER_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
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
  const [issuerSel, setIssuerSel] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const absent = tier === "Absent";

  // The data are the Observer's shared rows (1c slice 3): the same capture
  // as the cluster summary's certs task, never a watch of the lens's own.
  useCertManagerRows(absent ? null : cluster);

  const mine = cm.cluster === cluster;
  const certs = useMemo(() => (mine ? cm.certificates : []), [mine, cm.certificates]);
  const issuers = useMemo(() => (mine ? cm.issuers : []), [mine, cm.issuers]);
  const watchError = mine ? cm.watchError : null;
  // A4 (slice 3 §4): past the deadline the rows are evidence as of their
  // capture, and the counts are the summary's until a re-read lands.
  const rowsRefreshing = mine && !!cm.rowsState?.refreshing;
  const rowsAsOf = cm.rowsState?.evaluatedAt ? clock(cm.rowsState.evaluatedAt) : "the last read";
  // The global counts are the one count selector's (slice 3 §4a): the rows'
  // own, or the summary's past the A4 deadline. Headlines and attention chips
  // read them; filtered views and breakdowns stay row-scoped ("as of").
  const globalCounts = mine ? cm.partCounts : undefined;
  // Evidence beside the rows: a stale kind's last good time and a failing
  // discovery re-check (a capability gap, never a downgrade of live rows).
  const lastGood = mine ? lastGoodAt(cm.coverage, ["Certificate", "Issuer", "ClusterIssuer"]) : null;
  const discoveryGap = mine ? coverageGaps(cm.coverage ?? []) : null;
  const loaded = mine && !cm.loading;
  // Evidence per kind (layout/coverage.ts): every dataset, claim, auto-select
  // and removal reads ITS OWN kinds - a synced Certificate watch never vouches
  // for a pending or forbidden Issuer/ClusterIssuer watch, or the reverse.
  const evidenceOf = useCallback((kind: string): KindEvidence => kindEvidence(loaded, cm.coverage, kind, watchError), [loaded, cm.coverage, watchError]);
  const issuerCovOf = useCallback((kind: string): JoinCoverage => joinCoverage(evidenceOf(kind)), [evidenceOf]);
  const certsEv = evidenceOf("Certificate");
  const issuersEv = combineEvidence([evidenceOf("Issuer"), evidenceOf("ClusterIssuer")]);
  const certsCov = joinCoverage(certsEv);

  // Re-read the chain when the selection changes (and on demand).
  const selKey = mine ? cm.selectedKey : null;
  const resolveChain = useCallback((key: string) => {
    const slash = key.indexOf("/");
    void getCertificateChain(cluster, key.slice(0, slash), key.slice(slash + 1)).catch((e) => console.error("getCertificateChain", e));
  }, [cluster]);
  useEffect(() => { if (selKey) resolveChain(selKey); }, [selKey, resolveChain]);


  const q = query.trim().toLowerCase();
  const orderedCerts = useMemo(
    () => [...certs].sort((a, b) => certRank(a) - certRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [certs],
  );
  const namespaces = useMemo(() => Array.from(new Set(certs.map((c) => c.namespace))).sort(), [certs]);
  const passesStatus = useCallback((c: CertificateDTO) => {
    if (status === "attention") return included(c);
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
    if (status === "attention" && !included(i)) return false;
    return !q || [i.kind, i.namespace, i.name, i.reason, i.message].some((v) => (v ?? "").toLowerCase().includes(q));
  }), [orderedIssuers, namespace, status, q]);
  const certsFor = useCallback((i: IssuerDTO) => certs.filter((c) => resolveIssuer(c, [i]) === i), [certs]);

  // Selection lifecycle: certificates use the store's selectedKey (the chain
  // reader keys on it); issuers are local. Filter exclusion closes; absent
  // from a complete read = removed (last evidence, read-only).
  const selectedCert = selKey ? certs.find((c) => keyOf(c) === selKey) : undefined;
  // The selection's last evidence, for this cluster and key only, pinned to
  // its certificate's UID (The Line slice 4 §6.3): a same-name certificate
  // with a new UID on a complete read is "replaced", never silently
  // followed. Missing without a complete read of its own kind it is kept
  // with its last row, unverified - never removed (slice 3 §5a).
  const certPin = usePinnedSubject(cluster, selKey, selectedCert, certsEv === "complete", "Certificate");
  const certReplaced = certPin.gone === "replaced";
  const certRemoved = certPin.gone === "removed" || certReplaced; // the inspector gates on removed
  const certUnverified = certPin.gone === "unverified";
  const subject = certPin.subject;
  // A chain is the selected row's only when it is the same object: with a
  // known UID the chain must carry that UID, so a same-name recreation's chain
  // never becomes the old row's detail, nor the old chain the new row's (§5b).
  const forSelection = mine && cm.chain && cm.chainCluster === cluster && keyOf(cm.chain.certificate) === selKey ? cm.chain : null;
  // A failed read is the request's outcome, keyed by namespace/name: shown
  // apart from the chain evidence, never as an empty chain.
  const chainError = forSelection?.error || null;
  const sameObject = (chain: CertChainDTO) => !subject?.uid || chain.certificate.uid === subject.uid;
  const liveChain = forSelection && !forSelection.error && sameObject(forSelection) ? forSelection : null;
  // the last chain read for an object, by UID: kept for a removed row or a
  // failed re-read of the same object, never for a recreation
  const lastChain = useRef<{ cluster: string; uid: string; chain: CertChainDTO } | null>(null);
  if (liveChain?.certificate.uid) lastChain.current = { cluster, uid: liveChain.certificate.uid, chain: liveChain };
  const chainOfOther = !!forSelection && !forSelection.error && !liveChain;
  const priorChain = !liveChain && (certRemoved || chainError) && subject?.uid && lastChain.current?.cluster === cluster && lastChain.current.uid === subject.uid ? lastChain.current.chain : null;
  const shownChain = liveChain ?? priorChain;
  // an opened same-name recreation is another object: read its chain as
  // well - never while it is only "replaced", which keeps the pinned chain
  const selUID = certPin.gone ? undefined : selectedCert?.uid;
  const resolvedObject = useRef<{ key: string; uid: string } | null>(null);
  useEffect(() => {
    if (!selKey || !selUID) return;
    const prev = resolvedObject.current;
    resolvedObject.current = { key: selKey, uid: selUID };
    if (prev && prev.key === selKey && prev.uid !== selUID) resolveChain(selKey);
  }, [selKey, selUID, resolveChain]);
  const selectedIssuer = issuerSel ? issuers.find((i) => issuerKey(i) === issuerSel) : undefined;
  // an issuer is removed (or replaced) only when ITS kind's read is complete
  const issuerPin = usePinnedSubject(cluster, issuerSel, selectedIssuer, !!issuerSel && evidenceOf(issuerSel.split("/")[0]) === "complete", "Issuer");
  const issuerReplaced = issuerPin.gone === "replaced";
  const issuerRemoved = issuerPin.gone === "removed" || issuerReplaced;
  const issuerUnverified = issuerPin.gone === "unverified";
  const issuerSubject = issuerPin.subject;
  useEffect(() => { if (selectedCert && view === "certificates" && !visibleCerts.includes(selectedCert)) selectCert(null); }, [selectedCert, visibleCerts, view, selectCert]);
  useEffect(() => { if (selectedIssuer && view === "issuers" && !visibleIssuers.includes(selectedIssuer)) setIssuerSel(null); }, [selectedIssuer, visibleIssuers, view, setIssuerSel]);
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    setIssuerSel(null); setCursorKey(null);
    autoSelect.current = "pending";
    entryCerts.current = useFleet.getState().certmanager.certificates;
  }, [cluster, setIssuerSel]);

  // One-shot auto-select of the first certificate needing attention.
  const autoSelect = useRef<"pending" | "done">("pending");
  const entryCerts = useRef<CertificateDTO[] | null>(null);
  if (entryCerts.current === null) entryCerts.current = cm.certificates;
  // auto-select waits for the Certificate kind's first complete list
  const freshRead = certsEv === "complete" && cm.certificates !== entryCerts.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead || view !== "certificates") return;
    autoSelect.current = "done";
    if (useFleet.getState().certmanager.selectedKey) return;
    const c = visibleCerts.find(included);
    if (c) { setCursorKey(keyOf(c)); selectCert(keyOf(c)); }
  }, [freshRead, visibleCerts, view, selectCert]);

  // A summary Top link (health 1c slice 4 §5.4): open the part's view and
  // select the object past the filters - selection only, never an action.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "certs") return "wait";
    const issuer = intent.part === "issuers";
    const key = issuer ? `${kindOfRef(intent.ref)}/${intent.ref.namespace ?? ""}/${intent.ref.name}` : `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const row = issuer ? issuers.find((i) => issuerKey(i) === key) : certs.find((c) => keyOf(c) === key);
    // An opening by UID (slice 9 note §4.2): pinned to that UID at once; a
    // different or missing row is said, never substituted.
    if (intent.ref.uid) expectPin(cluster, issuer ? "Issuer" : "Certificate", key, intent.ref.uid);
    else if (!row) return (issuer ? issuersEv : certsEv) === "complete" ? "missing" : "wait";
    autoSelect.current = "done";
    setView(issuer ? "issuers" : "certificates"); setStatus("all"); setQuery(""); setNamespace("");
    setCursorKey(key);
    if (issuer) setIssuerSel(key); else selectCert(key);
    return "consumed";
  }, [certs, issuers, certsEv, issuersEv]);

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

  // global: the count selector's; row-scoped: the breakdown and filters
  const certTotal = globalCounts?.certificates?.total ?? certs.length;
  const attention = globalCounts?.certificates?.included ?? certs.filter(included).length;
  const issuerTotal = globalCounts?.issuers?.total ?? issuers.length;
  const issuersNotReady = globalCounts?.issuers?.included ?? issuers.filter(included).length;
  const failedOrExpired = certs.filter((c) => c.ready === "Failed" || expired(c)).length;
  // each header's mark from the reading that gave its count
  const certFault = globalCounts?.certificates ? globalCounts.certificates.fault : faultsOf(certs);
  const issuerFault = globalCounts?.issuers ? globalCounts.issuers.fault : faultsOf(issuers);
  const issuing = certs.filter((c) => c.ready === "Issuing").length;
  const renewing = certs.filter((c) => expiringSoon(c)).length;
  const issuerProblems = certs.filter((c) => issuerCovOf(c.issuerKind) !== "unknown" && issuerProblem(c, issuers)).length;
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";

  const clearFilters = () => { setStatus("all"); setQuery(""); setNamespace(""); };
  // total: the rows in hand (dataset state, filters, footer); the headline
  // reads the global count
  const total = view === "issuers" ? issuers.length : certs.length;
  const globalTotal = view === "issuers" ? issuerTotal : certTotal;
  const shown = view === "issuers" ? visibleIssuers.length : visibleCerts.length;
  const viewEv = view === "issuers" ? issuersEv : certsEv;
  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (viewEv === "failed" && total === 0) state = "unreadable";
  else if (viewEv === "pending" && total === 0) state = "loading";
  else if (total === 0) state = "empty";
  else if (shown === 0) state = "no-match";
  const noun = view === "issuers" ? "issuers" : "certificates";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={7} label="loading certificate state" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="cert-manager state could not be read." hint={watchError ?? undefined} />
    : state === "empty" ? (absent
      ? <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title="cert-manager is not installed on this cluster." hint="Klyx detected no cert-manager.io resources here." />
      : rowsRefreshing && globalTotal > 0 ? <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title={`No ${noun} as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${globalTotal}.`} />
      : <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title={view === "issuers" ? "No issuers on this cluster." : "No Certificates on this cluster."} hint={view === "issuers" ? undefined : "cert-manager is installed but no Certificate objects exist yet."} />)
    : <EmptyState icon={<IconCertificate size={28} stroke={1.2} />} title={`No ${noun} match the current filters.`} hint={`${total} ${noun} on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;
  // pending/failed = a lower bound ("+"); stale = the last observed set ("*",
  // not necessarily a lower bound); never an unqualified count either way
  const partial = viewEv === "complete" ? "" : viewEv === "stale" ? "*" : "+";

  // Notices stack: each is its own evidence, none hides another (Codex #1).
  const statusLine = { minHeight: 28, display: "flex", alignItems: "center" } as const;
  const notices: React.ReactNode[] = [];
  if (watchError && total > 0) notices.push(<WatchErrorBanner key="watch" tool="cert-manager" failure={`watch failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={watchError} />);
  else if (viewEv === "pending" && total > 0) notices.push(<div key="pending" role="status" className="t-secondary" style={statusLine}>Still reading - this {view === "issuers" ? "issuer" : "Certificate"} list is incomplete.</div>);
  if (rowsRefreshing && !absent) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{cm.rowsState?.refreshError ? ` Last re-read failed: ${cm.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap && !absent) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  return (
    <LensLayout
      label="cert-manager"
      title="cert-manager"
      summary={state === "loading" || state === "unreadable" || absent ? null : view === "issuers" ? (
        <>
          <CountSentence total={issuerTotal} marker={partial} noun={["issuer", "issuers"]}
            attention={{ count: issuersNotReady, phrase: "not ready", fault: issuerFault > 0 }}
            clear={issuersEv === "complete" ? "all issuers ready" : issuersEv === "stale" ? "none known not ready (last observed)" : "issuer list incomplete"} />
        </>
      ) : (
        <>
          <CountSentence total={certTotal} marker={partial} noun={["certificate", "certificates"]}
            attention={{ count: attention, phrase: "not ready or expired", fault: certFault > 0 }}
            clear={certsEv === "complete" ? "all ready" : certsEv === "stale" ? "none known not ready (last observed)" : "Certificate list incomplete"} />
          <span className="t-secondary" data-testid="cert-breakdown">{[failedOrExpired ? `${failedOrExpired} failed or expired` : "", issuing ? `${issuing} issuing` : "", renewing ? `${renewing} in renewal window` : "", issuerProblems ? `${issuerProblems} with issuer problem` : ""].filter(Boolean).join(" · ")}{(failedOrExpired || issuing || renewing || issuerProblems) ? asOf : ""}</span>
        </>
      )}
      controls={absent ? undefined : (
        <>
          <div role="group" aria-label="cert-manager view" className="l-seg">
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
          <div role="group" aria-label="Status" className="l-seg">
            <button className="l-chip" aria-pressed={status === "all"} onClick={() => setStatus("all")}>All</button>
            <button className="l-chip" aria-pressed={status === "attention"} onClick={() => setStatus("attention")}>Not ready {(view === "issuers" ? issuersNotReady : attention) > 0 && <span style={{ color: "var(--color-text-warning)" }}>{view === "issuers" ? issuersNotReady : attention}</span>}</button>
            {view === "certificates" && (
              <>
                <button className="l-chip" aria-pressed={status === "expiring"} onClick={() => setStatus("expiring")}>Expiring <span style={{ color: "var(--color-text-tertiary)" }}>{certs.filter((c) => expiringSoon(c) || expired(c)).length}</span></button>
                {issuing > 0 && <button className="l-chip" aria-pressed={status === "issuing"} onClick={() => setStatus("issuing")}>Issuing <span style={{ color: "var(--color-text-tertiary)" }}>{issuing}</span></button>}
                {issuerProblems > 0 && <button className="l-chip" aria-pressed={status === "issuerProblem"} onClick={() => setStatus("issuerProblem")}>Issuer problem <span style={{ color: "var(--color-text-warning)" }}>{issuerProblems}</span></button>}
              </>
            )}
          </div>
          <label className="lens-search">
            <span className="sr-only">Search cert-manager</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={view === "issuers" ? "Search issuers" : "Search name, issuer, DNS, secret"} />
          </label>
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
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
                const role = roleOf(i);
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === issuerSel ? "true" : undefined}
                    onClick={() => { setCursorKey(k); setIssuerSel(k === issuerSel ? null : k); }}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setIssuerSel(k); } }}>
                    {show("mark") && <VerdictMark health={role} />}
                    {show("name") && <span className="lens-cell-name" title={`${i.namespace ? `${i.namespace}/` : ""}${i.name}`}>{i.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{i.namespace} / </span>}{i.name}</span>}
                    {show("kind") && <span style={{ color: "var(--color-text-tertiary)" }}>{i.kind}</span>}
                    {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}>{role.label}{included(i) && i.reason ? ` · ${i.reason}` : ""}</span>}
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
                const role = roleOf(c);
                const soon = expiringSoon(c);
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" {...staleProps(c.stale && !expired(c))} style={gridStyle}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                    onClick={() => { setCursorKey(k); autoSelect.current = "done"; selectCert(k === selKey ? null : k); }}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selectCert(k); } }}>
                    {show("mark") && <VerdictMark health={role} stale={c.stale && !expired(c)} />}
                    {show("name") && (
                      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                        <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{c.namespace} / </span><span style={{ fontWeight: k === selKey ? 600 : 400 }}>{c.name}</span></span>
                        <CopyButton text={c.name} label={`copy certificate name ${c.name}`} appearOnRowHover />
                      </span>
                    )}
                    {show("status") && (
                      <span style={cellStyle}>
                        {c.stale && !expired(c) ? <SyncingChip generation={c.generation} observedGeneration={c.observedGeneration} />
                          : <span style={{ color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}>{role.label}{included(c) && c.reason ? ` · ${c.reason}` : ""}</span>}
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
      footerEnd={<span className="t-secondary">{shown} of {total}{partial} {noun}{asOf}{view === "certificates" ? " · broken first, then soonest expiry" : ""}</span>}
      inspector={view === "certificates" && selKey && subject ? (
        <CertInspector
          key={`${cluster}/${selKey}`}
          c={subject}
          cluster={cluster}
          removed={certRemoved}
          unverified={certUnverified}
          replaced={certReplaced ? certPin.adopt : undefined}
          issuer={issuerCovOf(subject.issuerKind) !== "unknown" ? resolveIssuer(subject, issuers) : undefined}
          issuersKnown={issuerCovOf(subject.issuerKind) !== "unknown"}
          issuerCov={issuerCovOf(subject.issuerKind)}
          chain={shownChain}
          chainPrior={shownChain !== null && (certRemoved || shownChain === priorChain)}
          chainError={chainError}
          chainOfOther={chainOfOther}
          chainLoading={!shownChain && !chainError && mine && cm.chainLoading}
          onClose={() => { autoSelect.current = "done"; selectCert(null); }}
          onResolve={() => selKey && resolveChain(selKey)}
          onOpenIssuer={(i) => { setView("issuers"); setStatus("all"); setIssuerSel(issuerKey(i)); setCursorKey(issuerKey(i)); }}
        />
      ) : view === "certificates" && selKey && certPin.expected && certPin.gone ? (
        <PinnedGone cluster={cluster} kind="Certificate" namespace={selKey.split("/")[0]} name={selKey.split("/").slice(1).join("/")} uid={certPin.expected} gone={certPin.gone}
          onAdopt={certPin.next ? certPin.adopt : undefined} onClose={() => { autoSelect.current = "done"; selectCert(null); }} />
      ) : view === "issuers" && issuerSel && issuerSubject ? (
        <IssuerInspector
          cluster={cluster}
          key={`${cluster}/${issuerSel}`}
          issuer={issuerSubject}
          removed={issuerRemoved}
          unverified={issuerUnverified}
          replaced={issuerReplaced ? issuerPin.adopt : undefined}
          certs={certsFor(issuerSubject)}
          certsCov={certsCov}
          onClose={() => setIssuerSel(null)}
          onOpenCert={(c) => { setView("certificates"); setStatus("all"); setNamespace(""); setQuery(""); setCursorKey(keyOf(c)); autoSelect.current = "done"; selectCert(keyOf(c)); }}
        />
      ) : view === "issuers" && issuerSel && issuerPin.expected && issuerPin.gone ? (
        <PinnedGone cluster={cluster} kind={issuerSel.split("/")[0]} namespace={issuerSel.split("/")[1] || undefined} name={issuerSel.split("/").slice(2).join("/")} uid={issuerPin.expected} gone={issuerPin.gone}
          onAdopt={issuerPin.next ? issuerPin.adopt : undefined} onClose={() => setIssuerSel(null)} />
      ) : undefined}
      inspectorLabel={view === "issuers" ? "Issuer details" : "Certificate details"}
      onCloseInspector={closeSelected}
    />
  );
}

// UnverifiedNotice: the selection is missing from a read that is not live
// and complete for its kind - kept with its last row, never called removed.
function UnverifiedNotice({ kind }: { kind: string }) {
  return (
    <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
      <span className="t-status">Not in the current {kind} read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
    </div>
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
function CertInspector({ c, cluster, removed, unverified, replaced, issuer, issuersKnown, issuerCov, chain, chainPrior, chainError, chainOfOther, chainLoading, onClose, onResolve, onOpenIssuer }: {
  c: CertificateDTO;
  cluster: string;
  removed: boolean;
  unverified: boolean;
  replaced?: () => void;
  issuer: IssuerDTO | undefined;
  issuersKnown: boolean;
  issuerCov: JoinCoverage;
  chain: CertChainDTO | null;
  // chainPrior: the chain is this object's last good read, not the current one
  chainPrior: boolean;
  chainError: string | null;
  // chainOfOther: the last chain read was of an earlier object with this name
  chainOfOther: boolean;
  chainLoading: boolean;
  onClose: () => void;
  onResolve: () => void;
  onOpenIssuer: (i: IssuerDTO) => void;
}) {
  // Prefer the server-resolved bound issuer (chain.issuer); fall back to the
  // watched issuer list before the chain lands.
  const boundIssuer = chain?.issuer ?? issuer;
  const issuerRole = boundIssuer ? roleOf(boundIssuer) : null;
  // only CONFIRMED adverse issuer health is a diagnosed root cause; Unknown is
  // unverified evidence, shown muted
  const issuerBroken = !!issuerRole && (issuerRole.kind === "fault" || issuerRole.kind === "degraded");
  const failing = chain?.failingLink ?? null;
  const failKey = failing ? `${failing.kind}/${failing.namespace}/${failing.name}` : null;
  const requests = arrayOf(chain?.requests);
  const warnings = arrayOf(chain?.warnings);
  const dnsNames = arrayOf(c.dnsNames);
  // Only CONFIRMED issuer evidence may become a cause: a broken issuer is the
  // headline card; an Unknown issuer is unverified evidence (no issuer-derived
  // cause); a missing issuer is a cause only with that kind's coverage. Chain
  // failures and other independent facts still provide a cause.
  const issuerEvidence: IssuerEvidence = !boundIssuer
    ? (issuersKnown ? { kind: "missing", lastObserved: issuerCov === "stale" } : { kind: "no-claim" })
    : issuerRole!.kind === "unknown" ? { kind: "no-claim" } : { kind: "found", issuer: boundIssuer };
  const cause = issuerBroken ? "" : failureHint(c, issuerEvidence, failing, warnings);
  const live = !removed;
  const role = roleOf(c);

  // Explain anchors on Klyx's row verdict and adds the issuance chain
  // (spec 2026-10-13 §4.1), fenced by the certificate's UID.
  const ai = useInspectorAI(c.uid ? { kind: "row", task: "certs", part: "certificates", uid: c.uid } : null, `Certificate/${c.namespace}/${c.name}`, live, cluster)
  const manifestRef = wellKnownResourceRef("cert-manager.io", "Certificate");
  const more: MenuItem[] = [
    ...(manifestRef ? [{ label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(c.namespace, c.name); } }] : []),
    ...(c.secretName ? [{ label: `Open Secret ${c.secretName}`, onSelect: () => { const nav = useFleet.getState(); nav.openResource(SECRET_REF); nav.openInstance(c.namespace, c.secretName); } }] : []),
    ...(ai.readiness.ready ? [] : ai.menu),
  ];

  return (
    <div data-testid="cert-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: c.uid || undefined }} replaced={replaced}
        kind="Certificate"
        namespace={c.namespace}
        name={c.name}
        meta={[`issuer ${c.issuerKind}/${c.issuerName}`, c.secretName ? `secret ${c.secretName}` : ""].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="Certificate" /> : unverified ? <UnverifiedNotice kind="Certificate" /> : (
          <div>
            {c.stale && !expired(c) ? (
              <InspectorStatus tone="muted" kind="progressing" label="Syncing…" reason={`generation ${c.generation} not yet observed (observed ${c.observedGeneration})`} />
            ) : (
              <InspectorStatus
                tone={roleTone(role.kind)}
                kind={role.kind}
                label={role.label}
                reason={included(c) || c.ready === "Issuing" ? c.reason || undefined : expiringSoon(c) ? "renewal window" : undefined}
                detail={c.notAfterUnix > 0 && !expired(c) ? `expires ${expiryText(c)}` : undefined}
              />
            )}
            {c.stale && expired(c) && (
              <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>
                Spec generation {c.generation} not yet observed (observed {c.observedGeneration}) - the expiry above is current regardless.
              </div>
            )}
            {c.message && included(c) && <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: c.stale && expired(c) ? 2 : -4 }}>{c.message}</div>}
          </div>
        )}
        drawing={(
          <>
          {/* The bound issuer's health heads the chain (M11): a sunk evidence
              panel led by its mark - no triangle, no danger wall (slice 13c.7). */}
          {issuerBroken && (
            <div className="evidence-panel" data-tone={issuerRole!.kind === "fault" ? "danger" : "warning"}>
              <div className="evidence-panel-head">
                <Aspect kind={aspectOfVerdict(issuerRole!.kind)} size={10} />
                <span>{issuerRole!.kind === "fault" ? "Issuer failed" : "Issuer not ready"}{issuerCov === "stale" ? " (last observed - the issuer watch is failing)" : ""}: {boundIssuer!.reason || "unknown"}</span>
              </div>
              <button onClick={() => onOpenIssuer(boundIssuer!)} className="t-identifier evidence-panel-link">{boundIssuer!.kind}/{boundIssuer!.name}</button>
              {boundIssuer!.message && <div className="evidence-panel-text">{boundIssuer!.message}</div>}
            </div>
          )}
          <InspectorBlock title={failing ? "Issuance chain - deepest stuck node flagged" : "Issuance chain"} count={chain ? requests.length : undefined}>
            {chainError && !removed && (
              <div role="alert" data-testid="chain-error" className="t-body" style={{ display: "flex", alignItems: "flex-start", gap: 5, color: "var(--color-text-danger)", marginBottom: 4, overflowWrap: "anywhere" }}>
                <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
                <span>Chain read failed: {chainError}{chain ? "" : " - the chain is unverified, not empty."}</span>
              </div>
            )}
            {chain && chainPrior && <div className="t-secondary" style={{ marginBottom: 3 }}>{chain.asOfUnix ? `Last resolved ${new Date(chain.asOfUnix * 1000).toLocaleTimeString()}` : "Last resolved chain"}{removed ? " - before it was removed." : "."}</div>}
            {chainLoading ? <span className="t-secondary">Resolving chain…</span>
              : !chain ? (chainError && !removed ? null : <span className="t-secondary" data-testid="chain-unresolved">{removed ? "No chain was resolved before it was removed." : chainOfOther ? "Chain not resolved for this object - the last read was of an earlier object with this name." : "Chain not resolved."}</span>)
              : requests.length > 0 ? <ChainRail nodes={requests} failKey={failKey} />
              : <span className="t-secondary">No active issuance request.</span>}
            {warnings.map((w) => <div key={w} className="t-body" style={{ color: "var(--color-text-warning)", marginTop: 3, overflowWrap: "anywhere" }}>{w}</div>)}
          </InspectorBlock>
          </>
        )}
        primary={live ? { label: chainLoading ? "Resolving chain…" : "Re-resolve chain", onSelect: onResolve } : undefined}
        secondary={live && ai.readiness.ready && c.uid ? { label: ai.state.status === "loading" ? "Explaining…" : "Explain", onSelect: () => { if (ai.state.status !== "loading") ai.run(); } } : undefined}
        more={more}
      >
        {cause && !issuerBroken && (
          <div className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <div style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>Likely cause</div>
            <div style={{ color: "var(--color-text-secondary)", marginTop: 3, overflowWrap: "anywhere" }}>{cause}</div>
          </div>
        )}
        {ai.block}
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
        {c.message && !included(c) && (
          <InspectorBlock title="Message" defaultOpen={false}>
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{c.message}</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

function IssuerInspector({ cluster, issuer, removed, unverified, replaced, certs, certsCov, onClose, onOpenCert }: { cluster: string; issuer: IssuerDTO; removed: boolean; unverified: boolean; replaced?: () => void; certs: CertificateDTO[]; certsCov: JoinCoverage; onClose: () => void; onOpenCert: (c: CertificateDTO) => void }) {
  const ai = useInspectorAI(issuer.uid ? { kind: "row" as const, task: "certs", part: "issuers", uid: issuer.uid } : null, `${issuer.kind}/${issuer.namespace ? `${issuer.namespace}/` : ""}${issuer.name}`, !removed && !unverified, cluster);
  const role = roleOf(issuer);
  const ref = wellKnownResourceRef("cert-manager.io", issuer.kind);
  const view = ref ? () => { const nav = useFleet.getState(); nav.openResource(ref); nav.openInstance(issuer.kind === "ClusterIssuer" ? "" : issuer.namespace, issuer.name); } : null;
  const blocked = certs.filter(included);
  return (
    <div data-testid="issuer-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: issuer.uid || undefined }} replaced={replaced}
        kind={issuer.kind}
        namespace={issuer.namespace || undefined}
        name={issuer.name}
        meta={issuer.kind === "ClusterIssuer" ? "cluster-scoped - any namespace may use it" : `namespaced - serves Certificates in ${issuer.namespace}`}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind={issuer.kind} /> : unverified ? <UnverifiedNotice kind={issuer.kind} /> : (
          <div>
            <InspectorStatus tone={role.kind === "fault" ? "danger" : role.kind === "degraded" ? "warning" : role.kind === "ok" ? "success" : "muted"} kind={role.kind} label={role.label} reason={issuer.ready !== "Ready" ? issuer.reason || undefined : undefined} />
            {issuer.message && issuer.ready !== "Ready" && <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{issuer.message}</div>}
          </div>
        )}
        primary={view ? { label: "View manifest in Resources", onSelect: view } : undefined}
        more={ai.menu}
      >
        {ai.block}
        <InspectorBlock title="Certificates using it" count={certsCov === "unknown" ? undefined : certs.length} extra={blocked.length > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{blocked.length} not ready or expired</span> : undefined}>
          {certsCov === "stale" && <span className="t-secondary">From the last observed Certificate list; the watch is currently failing.</span>}
          {certsCov === "unknown" ? <span className="t-secondary">Certificate read unavailable - usage unverified, not zero.</span>
            : certs.length === 0 ? <span className="t-secondary">No Certificate references this issuer.</span> : (
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {[...blocked, ...certs.filter((c) => !blocked.includes(c))].map((c) => (
                <button key={keyOf(c)} onClick={() => onOpenCert(c)} className="t-identifier"
                  style={{ display: "flex", gap: 8, alignItems: "center", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>
                  <VerdictLamp health={roleOf(c)} size={7} />
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

// IssuerEvidence says what the issuer side may claim in a cause: a found
// issuer (its own health speaks), a confirmed absence from a covered list, or
// no claim at all (Unknown health, or no coverage for that issuer kind).
type IssuerEvidence =
  | { kind: "found"; issuer: IssuerDTO }
  | { kind: "missing"; lastObserved: boolean }
  | { kind: "no-claim" };

function failureHint(c: CertificateDTO, ev: IssuerEvidence, failing: ChainNodeDTO | null, warnings: string[]): string {
  if (ev.kind === "found" && ev.issuer.rule.included) return `${ev.issuer.kind}/${ev.issuer.name} is not ready${ev.issuer.reason ? `: ${ev.issuer.reason}` : ""}.`;
  if (ev.kind === "missing") return `${c.issuerKind}/${c.issuerName} was not found in the ${ev.lastObserved ? "last observed" : "watched"} ${c.issuerKind} list.`;
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

// ChainRail: the issuance chain on the shared Rail (The Line slice 8a), one
// step per node in processing order. Only the deepest stuck node gets loud -
// "stuck here" and its message in a red-ringed box; healthy links stay quiet.
const chainAspect: Record<string, AspectKind> = { Ready: "none", Failed: "fault", Issuing: "syncing" };

function ChainRail({ nodes, failKey }: { nodes: ChainNodeDTO[]; failKey: string | null }) {
  const steps: RailStep[] = flattenChain(nodes).map((node) => {
    const key = `${node.kind}/${node.namespace}/${node.name}`;
    const isFail = failKey === key;
    const bad = node.ready === "Failed";
    const issuing = node.ready === "Issuing";
    const kind = node.kind || "Unknown";
    const stuckText = node.message || node.reason;
    const kindWord = chainKindLabel[kind] ?? kind.toLowerCase();
    return {
      key,
      segment: "chain",
      name: `${kindWord} ${node.name}`,
      aspect: chainAspect[node.ready] ?? "checking",
      tags: isFail ? ["stuck here"] : undefined,
      label: (
        <span style={{ display: "inline-flex", alignItems: "baseline", gap: 7, fontSize: 11, minWidth: 0 }}>
          <span style={{ color: "var(--color-text-tertiary)", width: 60, flexShrink: 0 }}>{kindWord}</span>
          <span style={{ ...mono, overflowWrap: "anywhere", minWidth: 0, color: isFail ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>{node.name}</span>
          <span className="sr-only">, {node.ready || "Unknown"}</span>
          {(bad || issuing) && node.reason && !isFail && (
            <span style={{ ...mono, fontSize: 10, color: bad ? "var(--color-text-danger)" : "var(--color-text-warning)", flexShrink: 0 }} title={node.message}>{node.reason}</span>
          )}
        </span>
      ),
      body: isFail && stuckText ? (
        <div style={{ marginTop: 1, padding: "6px 9px", borderRadius: 4, background: "var(--color-background-danger)", boxShadow: "inset 0 0 0 1px var(--color-border-danger)", fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>
          {stuckText}
        </div>
      ) : undefined,
    };
  });
  return <Rail label="Issuance chain" steps={steps} />;
}

