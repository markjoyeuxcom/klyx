import { useEffect, useMemo, useRef, useState } from "react";
import { IconShieldCheck } from "@tabler/icons-react";
import type { RuleDTO } from "../store/fleet";
import { checkAdmissionParams, type AdmissionBindingDTO, type AdmissionConfigDTO, type AdmissionParamsDTO, type AdmissionPolicyDTO, type AdmissionRuleDTO, type AdmissionWebhookDTO } from "../bridge/admission";
import { useAdmissionPolicies, useAdmissionWebhooks, type AdmissionPartRows } from "../health/useAdmissionRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { Aspect, aspectOfVerdict } from "../chrome/primitives/Aspect";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { LensParts } from "../chrome/LensParts";
import { useNavIntent } from "../chrome/useNavIntent";
import { expectPin } from "../nav/inspectorPin";

// AdmissionView - the Admission lens (coverage S3a §3): webhook
// configurations judged by their backends, and Validating and Mutating
// AdmissionPolicies with their bindings, from the Observer's shared rows.
// Read-only. It says "matching requests", never which objects a webhook
// blocks: rules alone omit selectors, match conditions and exclusions.

type Tab = "webhooks" | "policies" | "bindings";
type RoleKind = "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
const RANK: Record<string, number> = { fault: 0, degraded: 1, unknown: 2, progressing: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleKind = (role: string): RoleKind => (role in RANK ? role : "unknown") as RoleKind;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const toneOf = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const why = (reason: string | undefined, label: string) => (reason ? (reason.startsWith(label + ": ") ? reason.slice(label.length + 2) : reason) : "");
const kindOfGVR = (gvr: string) => (gvr.includes("/mutating") ? "Mutating" : "Validating");

const COLUMNS: Record<Tab, (ColumnDef & { label: string })[]> = {
  webhooks: [
    ROW_MARK,
    { id: "name", label: "Name", width: 240, priority: 0, flex: true },
    { id: "status", label: "Status", width: 260, priority: 0 },
    { id: "kind", label: "Kind", width: 100, priority: 1 },
    { id: "hooks", label: "Webhooks", width: 90, priority: 1 },
    { id: "backends", label: "Backends", width: 170, priority: 2 },
  ],
  policies: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "status", label: "Status", width: 240, priority: 0 },
    { id: "kind", label: "Kind", width: 100, priority: 1 },
    { id: "failure", label: "Failure policy", width: 110, priority: 2 },
    { id: "bindings", label: "Bindings", width: 90, priority: 1 },
    { id: "param", label: "paramKind", width: 160, priority: 2 },
  ],
  bindings: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "policy", label: "Policy", width: 220, priority: 0 },
    { id: "kind", label: "Kind", width: 100, priority: 1 },
    { id: "actions", label: "Actions", width: 140, priority: 1 },
    { id: "param", label: "paramRef", width: 180, priority: 2 },
  ],
};

const PARTS: Record<Tab, { noun: [string, string]; lens: string; part: string }> = {
  webhooks: { noun: ["webhook configuration", "webhook configurations"], lens: "AdmissionWebhooks", part: "webhooks" },
  policies: { noun: ["admission policy", "admission policies"], lens: "AdmissionPolicies", part: "policies" },
  bindings: { noun: ["binding", "bindings"], lens: "AdmissionBindings", part: "policies" },
};
const KEY_KIND: Record<Tab, string> = { webhooks: "Config", policies: "Policy", bindings: "Binding" };
const TAB_OF_KEY: Record<string, Tab> = { Config: "webhooks", Policy: "policies", Binding: "bindings" };
const keyOf = (tab: Tab, kind: string, name: string) => `${KEY_KIND[tab]}/${kind}/${name}`;

// The evidence limits, said every time (S3a §3).
export const LIMITS = [
  "Ready endpoints do not prove the API server reaches them, trusts their TLS, or that admission succeeds.",
  "Klyx cannot see the API server's routing mode (ClusterIP or endpoint routing), so some states depend on it and say so.",
  "Admission configured from the API server's own manifest files, or by a managed platform, is not visible through the API.",
];

type Item = { key: string; uid: string; name: string; kind: string };

export function AdmissionView({ cluster }: { cluster: string }) {
  const hooks = useAdmissionWebhooks(cluster);
  const pols = useAdmissionPolicies(cluster);
  const [tab, setTab] = useState<Tab>("webhooks");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const listRef = useRef<VirtualListHandle>(null);

  const configs = useMemo(() => hooks.rows.map((c) => ({ ...c, key: keyOf("webhooks", c.kind, c.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.name.localeCompare(b.name)), [hooks.rows]);
  const policies = useMemo(() => pols.rows.filter((p) => p.entity === "policy" && p.rule).map((p) => ({ ...p, key: keyOf("policies", p.kind, p.name) }))
    .sort((a, b) => rankOf(a.rule!) - rankOf(b.rule!) || a.name.localeCompare(b.name)), [pols.rows]);
  const bindings = useMemo(() => pols.rows.filter((p) => p.entity === "binding" && p.binding).map((p) => ({ ...p, key: keyOf("bindings", p.kind, p.name) }))
    .sort((a, b) => a.name.localeCompare(b.name)), [pols.rows]);
  const part: Record<Tab, AdmissionPartRows<unknown>> = { webhooks: hooks, policies: pols, bindings: pols };
  const items: Record<Tab, Item[]> = { webhooks: configs, policies, bindings };
  const KIND: Record<Tab, string> = { webhooks: "ValidatingWebhookConfiguration", policies: "ValidatingAdmissionPolicy", bindings: "ValidatingAdmissionPolicyBinding" };
  const cur = part[tab];
  const list = items[tab];
  const ev = kindEvidence(!cur.loading, cur.coverage, KIND[tab], cur.watchError);
  const faults = (t: Tab) => (t === "bindings" ? 0 : part[t].counts ? part[t].counts!.fault : 0);

  const keys = list.map((r) => r.key);
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "admission") return "wait";
    const t: Tab = intent.part === "policies" ? (intent.ref.gvr.includes("bindings") ? "bindings" : "policies") : "webhooks";
    const key = keyOf(t, kindOfGVR(intent.ref.gvr), intent.ref.name);
    const found = items[t].some((r) => r.key === key);
    const tev = kindEvidence(!part[t].loading, part[t].coverage, KIND[t], part[t].watchError);
    if (intent.ref.uid) expectPin(cluster, PARTS[t].lens, key, intent.ref.uid);
    else if (!found) return tev === "complete" ? "missing" : "wait";
    setTab(t);
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [configs, policies, bindings]);

  const fitted = useMemo(() => fitColumns(COLUMNS[tab], bodyWidth), [tab, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selTab: Tab | null = TAB_OF_KEY[selKey?.split("/")[0] ?? ""] ?? null;
  const live = selTab ? items[selTab].find((r) => r.key === selKey) : undefined;
  const selEv = selTab ? kindEvidence(!part[selTab].loading, part[selTab].coverage, KIND[selTab], part[selTab].watchError) : "pending";
  const pin = usePinnedSubject(cluster, selTab ? selKey : null, live, selEv === "complete", selTab ? PARTS[selTab].lens : undefined);

  let state: DatasetState = "ready";
  if (ev === "failed" && list.length === 0) state = "unreadable";
  else if (ev === "pending" && list.length === 0) state = "loading";
  else if (list.length === 0) state = "empty";
  const icon = <IconShieldCheck size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${PARTS[tab].noun[1]}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${PARTS[tab].noun[1]}.`} hint={cur.watchError ?? undefined} />
    : <EmptyState icon={icon} title={`No ${PARTS[tab].noun[1]} on this cluster.`} hint={tab === "webhooks" ? "Admission configured outside the API is not visible here." : undefined} />;

  const notices: React.ReactNode[] = [];
  if (cur.watchError && list.length > 0) {
    const lg = lastGoodAt(cur.coverage, [KIND[tab]]);
    notices.push(<WatchErrorBanner key="watch" tool="Admission" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={cur.watchError} />);
  }
  if (tab === "bindings") notices.push(<div key="facts" className="t-secondary">Bindings are listed as facts: a policy's verdict covers them.</div>);

  const rowFor = (it: Item, cells: Record<string, React.ReactNode>) => (
    <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`admission-row-${it.name}`}
      aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(it.key); } }}>
      {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id} style={c.id === "mark" ? undefined : cell}>{cells[c.id]}</span>)}
    </div>
  );
  const status = (r: RuleDTO, reason?: string) => {
    const k = roleKind(r.role);
    const w = k === "fault" || k === "degraded" ? why(reason, r.label) : "";
    return <span style={{ ...cell, color: ROLE_TEXT[k] }}>{r.label}{w ? ` · ${w}` : ""}</span>;
  };
  const muted = (s: React.ReactNode) => <span style={{ color: "var(--color-text-secondary)" }}>{s}</span>;
  const render = (it: Item) => {
    if (tab === "webhooks") {
      const c = it as AdmissionConfigDTO & Item;
      const parts = [c.unavailable ? `${c.unavailable} unavailable` : "", c.unverified ? `${c.unverified} unverified` : ""].filter(Boolean);
      return rowFor(it, { mark: <VerdictMark health={{ kind: roleKind(c.rule.role), label: c.rule.label }} />, name: <span className="lens-cell-name">{c.name}</span>,
        status: status(c.rule, c.reason), kind: muted(c.kind), hooks: muted(`${c.webhookCount}${c.cut ? " (cut)" : ""}`),
        backends: muted(parts.length ? parts.join(" · ") : "ready") });
    }
    if (tab === "policies") {
      const p = it as AdmissionPolicyDTO & Item;
      return rowFor(it, { mark: <VerdictMark health={{ kind: roleKind(p.rule!.role), label: p.rule!.label }} />, name: <span className="lens-cell-name">{p.name}</span>,
        status: status(p.rule!, p.reason), kind: muted(p.kind), failure: muted(p.failClosed ? "Fail" : "Ignore"), bindings: muted(String(p.bindings.length)),
        param: muted(p.paramKind ? `${p.paramKind.kind} · ${p.paramKind.state}` : "-") });
    }
    const b = (it as AdmissionPolicyDTO & Item).binding!;
    return rowFor(it, { mark: <span />, name: <span className="lens-cell-name">{b.name}</span>,
      policy: <span style={{ ...cell, color: b.policyState === "found" ? "var(--color-text-secondary)" : "var(--color-text-warning)" }}>{b.policyName}{b.policyState !== "found" ? ` · ${b.policyState === "not found" ? "not found: no effect" : "unverified"}` : ""}</span>,
      kind: muted((it as Item).kind), actions: muted(b.actions.join(", ") || "-"), param: muted(paramRefText(b)) });
  };

  const tabChip = (t: Tab, label: string) => {
    const n = faults(t);
    return <button className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>{label}{n ? <span style={{ color: "var(--color-text-danger)" }}> {n}</span> : null}</button>;
  };
  const open = (k: string) => { const t = TAB_OF_KEY[k.split("/")[0]] ?? "policies"; setTab(t); setCursorKey(k); setSelKey(k); };
  const subject = pin.subject as (Item & (AdmissionConfigDTO | AdmissionPolicyDTO)) | null;
  const gone = pin.gone === "replaced" ? "removed" : pin.gone;
  const inspector = selTab && subject ? (
    selTab === "webhooks" ? <ConfigInspector cluster={cluster} c={subject as AdmissionConfigDTO} gone={gone} onClose={() => setSelKey(null)} />
      : selTab === "policies" ? <PolicyInspector cluster={cluster} p={subject as AdmissionPolicyDTO} gone={gone} onOpen={open} onClose={() => setSelKey(null)} />
      : <BindingInspector cluster={cluster} p={subject as AdmissionPolicyDTO} gone={gone} onOpen={open} onClose={() => setSelKey(null)} />
  ) : selTab && selKey && pin.expected && pin.gone
    ? <PinnedGone cluster={cluster} kind={KIND[selTab]} name={selKey.split("/").slice(2).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
    : undefined;

  const total = tab === "bindings" ? list.length : cur.counts ? cur.counts.total : list.length;
  return (
    <LensLayout
      label="Admission"
      title="Admission"
      part="admission"
      summary={(
        <>
          <CountSentence total={total} marker={ev === "complete" ? "" : "+"} noun={PARTS[tab].noun} />
          {tab === "bindings" ? <span className="t-secondary">facts, no verdict</span>
            : faults(tab) > 0 ? <MarkedPhrase fault>{`${faults(tab)} ${tab === "webhooks" ? "with an unavailable backend" : "denying or misconfigured"}`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
        </>
      )}
      controls={(
        <>
          <LensParts cluster={cluster} groupId="admission" />
          <div role="group" aria-label="Admission objects" className="l-seg">
            {tabChip("webhooks", "Webhooks")}
            {tabChip("policies", "Policies")}
            {tabChip("bindings", "Bindings")}
          </div>
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={list} rowHeight={TABLE_ROW_HEIGHT} rowKey={(r) => r.uid || r.key} style={{ flex: 1, minHeight: 0 }} render={render} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">{tab === "bindings" ? "By name" : "Broken first"} · read-only · "matching requests", never which objects</span>}
      inspector={inspector}
      inspectorLabel="Admission object details"
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

export function paramRefText(b: AdmissionBindingDTO): string {
  const r = b.paramRef;
  if (!r) return "none";
  const target = r.name ? r.name : r.selector ? "a selector" : "-";
  return `${r.namespace ? `${r.namespace}/` : ""}${target}${r.notFoundAction ? ` · ${r.notFoundAction} if missing` : ""}`;
}

const ruleText = (r: AdmissionRuleDTO) =>
  `${r.operations.join(",") || "*"} ${r.groups.map((g) => g || "core").join(",") || "*"}/${r.resources.join(",") || "*"}${r.scope && r.scope !== "*" ? ` (${r.scope})` : ""}`;

type Gone = "removed" | "unverified" | null;

function goneVerdict(kind: string, gone: Gone) {
  return <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${kind} removed.` : `Not in the current ${kind} read.`}</span> The details below are its last evidence.</div>;
}

// BackendChain is a webhook's drawing (S3a §3): configuration → webhook →
// Service:port → endpoints, each link with its state.
export function BackendChain({ config, w }: { config: string; w: AdmissionWebhookDTO }) {
  const b = w.backend;
  const svcKnown = !w.url && w.serviceName;
  const endpointsState = b.state === "ready" ? "none" : b.state === "terminating" ? "attention" : b.state === "unavailable" ? (w.failClosed ? "fault" : "attention") : "checking";
  const svcState = b.state === "unavailable" && /not found|no Service port|not routable/.test(b.reason ?? "") ? (w.failClosed ? "fault" : "attention")
    : b.state === "unknown" && /headless|ExternalName|Services could not|last good read|is (UDP|SCTP)/.test(b.reason ?? "") ? "checking" : "none";
  const stations: { label: string; detail: string; aspect: string }[] = [
    { label: "configuration", detail: config, aspect: "none" },
    { label: "webhook", detail: w.name, aspect: "none" },
    w.url ? { label: "URL", detail: w.urlOrigin ?? "(url)", aspect: "checking" }
      : { label: "Service", detail: svcKnown ? `${w.serviceNamespace}/${w.serviceName}:${w.servicePort}` : "-", aspect: svcState },
  ];
  if (!w.url) stations.push({ label: "endpoints", detail: b.state === "ready" ? `${b.clusterRouting} usable` : b.state === "terminating" ? `${b.terminating} terminating` : b.state === "unavailable" ? "none usable" : "unverified", aspect: svcState === "none" ? endpointsState : "checking" });
  return (
    <div data-testid="admission-chain" role="list" aria-label={`Backend of ${w.name}`} style={{ display: "flex", alignItems: "flex-start", overflowX: "auto", padding: "4px 0 2px" }}>
      {stations.map((s, i) => (
        <div key={s.label} role="listitem" style={{ display: "grid", justifyItems: "center", gap: 2, minWidth: 120, padding: "0 4px" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: 14, width: "100%",
            backgroundImage: "linear-gradient(var(--color-border-secondary), var(--color-border-secondary))", backgroundSize: i === stations.length - 1 ? "50% 1px" : i === 0 ? "50% 1px" : "100% 1px",
            backgroundRepeat: "no-repeat", backgroundPosition: i === 0 ? "right center" : i === stations.length - 1 ? "left center" : "center" }}>
            {s.aspect === "none" ? <span role="img" aria-label={`${s.label}: ok`} style={{ width: 2, height: 10, background: "var(--color-text-secondary)" }} />
              : <Aspect kind={s.aspect as never} size={10} label={`${s.label}: ${s.aspect}`} />}
          </div>
          <div className="t-legend" style={{ color: "var(--color-text-tertiary)" }}>{s.label}</div>
          <div className="t-legend" style={{ maxWidth: 160, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={s.detail}>{s.detail}</div>
        </div>
      ))}
    </div>
  );
}

function ConfigInspector({ cluster, c, gone, onClose }: { cluster: string; c: AdmissionConfigDTO; gone: Gone; onClose: () => void }) {
  const kind = `${c.kind}WebhookConfiguration`;
  const ro = roleKind(c.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind, namespace: "", name: c.name, uid: c.uid } : null);
  const ai = useInspectorAI(c.uid ? { kind: "row" as const, task: "admission", part: "webhooks", uid: c.uid } : null, `${kind}/${c.name}`, !gone, cluster);
  return (
    <div data-testid="admission-config-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: c.uid || undefined }} kind={kind} name={c.name} meta="cluster-scoped" onClose={onClose}
        verdict={gone ? goneVerdict(kind, gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={c.rule.label} reason={why(c.reason, c.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="What Klyx cannot see">
          <ul className="t-secondary" data-testid="admission-limits" style={{ margin: 0, paddingLeft: 16 }}>{LIMITS.map((l) => <li key={l}>{l}</li>)}</ul>
        </InspectorBlock>
        {c.cut && <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{c.webhookCount - c.webhooks.length} webhooks were not read (too many to hold): the verdict never reads them as ready.</div>}
        {c.webhooks.map((w) => {
          const k = roleKind(w.role);
          return (
            <InspectorBlock key={w.name} title={w.name}>
              <div className="t-body" style={{ display: "flex", alignItems: "center", gap: 6 }} data-testid={`admission-webhook-${w.name}`}>
                {aspectOfVerdict(k) === "none" ? null : <Aspect kind={aspectOfVerdict(k)} size={10} label={k} />}
                <span style={{ color: ROLE_TEXT[k] }}>{w.backend.label}</span>
                {w.reason && <span className="t-secondary">· {w.reason}</span>}
              </div>
              <BackendChain config={c.name} w={w} />
              <div className="insp-kv" style={{ marginTop: 4 }}>
                <span>failure policy</span><span>{w.failClosed ? "Fail (fail-closed)" : "Ignore (fail-open)"}{w.failurePolicy ? "" : " · the default"}</span>
                <span>timeout</span><span>{w.timeoutSeconds}s</span>
                {w.sideEffects && <><span>side effects</span><span>{w.sideEffects}</span></>}
                {w.reinvocationPolicy && <><span>reinvocation</span><span>{w.reinvocationPolicy}</span></>}
                {!w.url && <><span>CA bundle</span><span>{w.caBundle ? "set (not shown)" : "not set: the API server trusts only its own roots"}</span></>}
                {w.pathSet && <><span>path</span><span>set (not shown)</span></>}
                {w.backend.selectorless && <><span>endpoints</span><span>the Service has no selector: its slices are managed by something other than the Service controller</span></>}
              </div>
              <div className="t-secondary" style={{ marginTop: 6 }}>What it matches: rules alone do not say which objects it blocks; selectors, match conditions and exclusions narrow it.</div>
              <div className="insp-kv">
                <span>rules</span><span style={{ overflowWrap: "anywhere" }}>{w.rules.map(ruleText).join("; ") || "-"}{w.ruleCount > w.rules.length ? ` +${w.ruleCount - w.rules.length}` : ""}</span>
                <span>selectors</span><span>{[w.namespaceSelector ? `namespace (${w.namespaceExprs ?? 0})` : "", w.objectSelector ? `object (${w.objectExprs ?? 0})` : ""].filter(Boolean).join(" · ") || "none"} (values not shown)</span>
                {w.matchConditions ? <><span>conditions</span><span>{w.matchConditions} match conditions (expressions not shown)</span></> : null}
                {w.matchPolicy && <><span>match policy</span><span>{w.matchPolicy}</span></>}
              </div>
            </InspectorBlock>
          );
        })}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

// useParamCheck reads a policy's params when its inspector opens; a reply
// lands only for this cluster and this policy (S3a §6).
function useParamCheck(cluster: string, uid: string, active: boolean): AdmissionParamsDTO | null {
  const [res, setRes] = useState<AdmissionParamsDTO | null>(null);
  useEffect(() => {
    setRes(null);
    if (!active || !uid) return;
    let live = true;
    void checkAdmissionParams(cluster, uid).then((r) => { if (live && r.cluster === cluster && r.policyUid === uid) setRes(r); });
    return () => { live = false; };
  }, [cluster, uid, active]);
  return res;
}

function PolicyInspector({ cluster, p, gone, onOpen, onClose }: { cluster: string; p: AdmissionPolicyDTO; gone: Gone; onOpen: (k: string) => void; onClose: () => void }) {
  const kind = `${p.kind}AdmissionPolicy`;
  const rule = p.rule!;
  const ro = roleKind(rule.role);
  const describe = useDescribe(cluster, !gone ? { kind, namespace: "", name: p.name, uid: p.uid } : null);
  const ai = useInspectorAI(p.uid ? { kind: "row" as const, task: "admission", part: "policies", uid: p.uid } : null, `${kind}/${p.name}`, !gone, cluster);
  const params = useParamCheck(cluster, p.uid, !gone && !!p.paramKind);
  const counts = [p.validations ? `${p.validations} validations` : "", p.mutations ? `${p.mutations} mutations` : "", p.variables ? `${p.variables} variables` : "",
    p.matchConditions ? `${p.matchConditions} match conditions` : "", p.auditAnnotations ? `${p.auditAnnotations} audit annotations` : ""].filter(Boolean);
  return (
    <div data-testid="admission-policy-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: p.uid || undefined }} kind={kind} name={p.name} meta="cluster-scoped" onClose={onClose}
        verdict={gone ? goneVerdict(kind, gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={rule.label} reason={why(p.reason, rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Policy">
          <div className="insp-kv">
            <span>failure policy</span><span>{p.failClosed ? "Fail (fail-closed)" : "Ignore"}{p.failurePolicy ? "" : " · the default"}</span>
            <span>expressions</span><span>{counts.join(" · ") || "-"} (expressions not shown)</span>
            <span>matches</span><span style={{ overflowWrap: "anywhere" }}>{p.matchRules.map(ruleText).join("; ") || "-"}{p.matchRuleCount > p.matchRules.length ? ` +${p.matchRuleCount - p.matchRules.length}` : ""}</span>
            {p.reinvocationPolicy && <><span>reinvocation</span><span>{p.reinvocationPolicy}</span></>}
            {p.paramKind && <><span>paramKind</span><span>{p.paramKind.kind} ({p.paramKind.apiVersion}) · {p.paramKind.state === "served" ? `served${p.paramKind.namespaced ? ", namespaced" : ", cluster-scoped"}` : p.paramKind.state === "not-served" ? "not served" : "not yet verified"}{p.paramKind.at ? ` · as of discovery at ${clock(p.paramKind.at)}` : ""}</span></>}
          </div>
        </InspectorBlock>
        {(p.warningCount ?? 0) > 0 && <InspectorBlock title={`Type-checking warnings · ${p.warningCount}`}>
          <div data-testid="admission-warnings" className="t-secondary" style={{ color: p.behind ? "var(--color-text-tertiary)" : "var(--color-text-warning)" }}>
            {p.behind ? "Historical: the status describes an earlier generation. " : ""}On {p.warningFields.join(", ")}{(p.warningCount ?? 0) > p.warningFields.length ? ` +${(p.warningCount ?? 0) - p.warningFields.length}` : ""}. The warning text is not shown: it quotes the expression.
          </div>
        </InspectorBlock>}
        <InspectorBlock title={`Bindings · ${p.bindings.length}`}>
          {p.bindings.length === 0 ? <div className="t-secondary">No binding names it: it has no effect.</div>
            : p.bindings.map((b) => <button key={b.name} className="t-body" onClick={() => onOpen(keyOf("bindings", p.kind, b.name))}
              style={{ display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>
              {b.name} <span className="t-secondary">· {b.actions.join(", ") || "no actions"} · paramRef {paramRefText(b)}</span>
            </button>)}
        </InspectorBlock>
        {p.paramKind && <InspectorBlock title="Params">
          <div data-testid="admission-params" className="t-secondary">
            {!params ? "Reading the params…"
              : params.state !== "read" ? params.text
              : <>
                {params.checks.map((c) => <div key={c.binding}><span style={{ color: "var(--color-text-primary)" }}>{c.binding}</span>: {c.text}</div>)}
                {params.more ? <div>+{params.more} bindings not checked</div> : null}
                <div>Read {params.readAt ? `at ${clock(params.readAt)}` : "now"}; existence only, never a param's values. The verdict does not check params.</div>
              </>}
          </div>
        </InspectorBlock>}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function BindingInspector({ cluster, p, gone, onOpen, onClose }: { cluster: string; p: AdmissionPolicyDTO; gone: Gone; onOpen: (k: string) => void; onClose: () => void }) {
  const b = p.binding!;
  const kind = `${p.kind}AdmissionPolicyBinding`;
  const describe = useDescribe(cluster, !gone ? { kind, namespace: "", name: b.name, uid: b.uid } : null);
  return (
    <div data-testid="admission-binding-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: b.uid || undefined }} kind={kind} name={b.name} meta="cluster-scoped" onClose={onClose}
        verdict={gone ? goneVerdict(kind, gone) : <div className="t-secondary">A binding is a fact in Klyx: its policy's verdict covers it.</div>}
        more={[...describe.menu]}
      >
        <InspectorBlock title="Binding">
          <div className="insp-kv">
            <span>policy</span><span>{b.policyState === "found"
              ? <button onClick={() => onOpen(keyOf("policies", p.kind, b.policyName))} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)" }}>{b.policyName}</button>
              : <span style={{ color: "var(--color-text-warning)" }}>{b.policyName} · {b.policyState === "not found" ? "not found: this binding has no effect" : "unverified (the policies read is incomplete)"}</span>}</span>
            <span>actions</span><span>{b.actions.join(", ") || "-"}</span>
            <span>paramRef</span><span>{paramRefText(b)}{b.paramRef?.selector ? " (selector values not shown)" : ""}</span>
            {b.matchRules ? <><span>matches</span><span>{b.matchRules} resource rules</span></> : null}
          </div>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
