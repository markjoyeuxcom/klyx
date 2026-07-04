import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ExternalSecretDTO, ExternalSecretStoreDTO } from "../store/fleet";
import { openExternalSecrets, closeExternalSecrets } from "../bridge/externalsecrets";
import { useListKeys } from "../chrome/useListKeys";
import { Badge, FilterButton, SummaryCell, Section, Field, StatusDot, frame, ellipsis, mono, readyColor, toneColor } from "../chrome/primitives";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { IconShieldLock } from "@tabler/icons-react";
import { SECRET_REF } from "./resourceRefs";

type EsoFilter = "all" | "attention" | "stale" | "stores";

const keyOf = (e: { namespace: string; name: string }) => `${e.namespace}/${e.name}`;

function fmtDuration(secs: number): string {
  if (secs <= 0) return "0s";
  if (secs < 60) return `${Math.floor(secs)}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
}

function refreshedText(e: ExternalSecretDTO): string {
  return e.lastRefreshAgeSeconds > 0 ? fmtDuration(e.lastRefreshAgeSeconds) : "—";
}

function staleTitle(e: ExternalSecretDTO): string {
  return `last synced ${fmtDuration(e.lastRefreshAgeSeconds)} ago - refresh interval ${fmtDuration(e.refreshIntervalSeconds)}; the controller has stopped refreshing`;
}

// Broken-first principle: anything not confidently Synced needs eyes - a
// Failed sync, an Unknown state, or a Synced state gone stale (the controller
// stopped refreshing, so Ready describes an old sync).
function needsAttention(e: ExternalSecretDTO): boolean {
  return e.state !== "Synced" || !!e.stale;
}

function secretRank(e: ExternalSecretDTO): number {
  if (e.state === "Failed") return 0;
  if (e.stale) return 1;
  if (e.state !== "Synced") return 2;
  return 3;
}

function storeRank(s: ExternalSecretStoreDTO): number {
  if (s.state === "Failed") return 0;
  if (s.state !== "Synced") return 1;
  return 2;
}

// resolveStore finds the bound (Cluster)SecretStore for an ExternalSecret. A
// ClusterSecretStore is cluster-scoped; a SecretStore is matched within the
// secret's namespace.
function resolveStore(e: ExternalSecretDTO, stores: ExternalSecretStoreDTO[]): ExternalSecretStoreDTO | undefined {
  return stores.find(
    (s) => s.kind === e.storeKind && s.name === e.storeName && (s.kind === "ClusterSecretStore" || s.namespace === e.namespace),
  );
}

function matchesFilter(e: ExternalSecretDTO, filter: EsoFilter): boolean {
  if (filter === "attention") return needsAttention(e);
  if (filter === "stale") return !!e.stale;
  return true;
}

function matchesQuery(e: ExternalSecretDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [e.namespace, e.name, e.storeName, e.targetSecret, e.reason].some((v) => v.toLowerCase().includes(q));
}

export function ExternalSecretsView({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.externalSecretsTier ?? "Unknown");
  const eso = useFleet((s) => s.externalsecrets);
  const select = useFleet((s) => s.selectExternalSecret);
  const [filter, setFilter] = useState<EsoFilter>("all");
  const [query, setQuery] = useState("");
  const [namespace, setNamespace] = useState("");
  const [kbKey, setKbKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const absent = tier === "Absent";

  useEffect(() => {
    if (absent) return;
    // If cleanup runs before openExternalSecrets resolves, invoke the
    // late-arriving unsubscriber immediately - otherwise its listener leaks for
    // the app's lifetime and duplicates store writes on every reopen.
    let cancelled = false;
    let off = () => {};
    openExternalSecrets(cluster).then((u) => (cancelled ? u() : (off = u))).catch((e) => console.error("openExternalSecrets", e));
    return () => {
      cancelled = true;
      off();
      void closeExternalSecrets(cluster);
    };
  }, [cluster, absent]);

  const secrets = eso.cluster === cluster ? eso.secrets : [];
  const stores = eso.cluster === cluster ? eso.stores : [];
  const orderedSecrets = useMemo(
    () => [...secrets].sort((a, b) => secretRank(a) - secretRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [secrets],
  );
  const namespaces = useMemo(() => Array.from(new Set(secrets.map((e) => e.namespace))).sort(), [secrets]);
  const visibleSecrets = useMemo(
    () => orderedSecrets.filter((e) => (!namespace || e.namespace === namespace) && matchesFilter(e, filter) && matchesQuery(e, query)),
    [orderedSecrets, filter, namespace, query],
  );
  const orderedStores = useMemo(
    () => [...stores].sort((a, b) => storeRank(a) - storeRank(b) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)),
    [stores],
  );

  // Keyboard parity with the Certs lens (j/k, Enter opens the inspector,
  // Esc closes it, / focuses the filter) - ESO is a near-clone of Certs and
  // was the one lens without it.
  const kbIdx = kbKey === null ? -1 : visibleSecrets.findIndex((e) => keyOf(e) === kbKey);
  const handleKbSelect = useCallback((idx: number) => {
    const e = visibleSecrets[idx];
    if (e) setKbKey(keyOf(e));
  }, [visibleSecrets]);
  const handleKbActivate = useCallback((idx: number) => {
    const e = visibleSecrets[idx];
    if (!e) return;
    const k = keyOf(e);
    select(useFleet.getState().externalsecrets.selectedKey === k ? null : k);
  }, [visibleSecrets, select]);
  const handleKbEscape = useCallback(() => {
    if (useFleet.getState().externalsecrets.selectedKey) select(null);
  }, [select]);
  useListKeys({
    // The stores filter renders a different list; suspend row nav there.
    count: filter === "stores" ? 0 : visibleSecrets.length,
    selected: kbIdx,
    onSelect: handleKbSelect,
    onActivate: handleKbActivate,
    onEscape: handleKbEscape,
    searchRef,
  });

  if (absent) {
    return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>External Secrets Operator not detected on this cluster.</div>;
  }

  const attention = secrets.filter(needsAttention).length;
  const failed = secrets.filter((e) => e.state === "Failed").length;
  const stale = secrets.filter((e) => e.stale).length;
  const synced = secrets.filter((e) => e.state === "Synced" && !e.stale).length;
  const selected = visibleSecrets.find((e) => keyOf(e) === eso.selectedKey) ?? null;

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16 }}>
        <div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>external-secrets</div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>External Secrets</div>
        </div>
        <Badge tone={attention > 0 ? "warning" : "success"}>{attention > 0 ? `${attention} need attention` : "synced"}</Badge>
      </div>

      {eso.cluster === cluster && eso.watchError && (
        <WatchErrorBanner tool="external-secrets" error={eso.watchError} />
      )}

      <div style={{ ...frame, display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(130px, 1fr))", overflow: "hidden" }}>
        <SummaryCell label="health" value={attention > 0 ? `${attention} attention` : "synced"} tone={attention > 0 ? "warning" : "success"} />
        <SummaryCell label="secrets" value={String(secrets.length)} />
        <SummaryCell label="failed" value={String(failed)} tone={failed > 0 ? "danger" : "muted"} />
        <SummaryCell label="stale" value={String(stale)} tone={stale > 0 ? "warning" : "muted"} />
        <SummaryCell label="stores" value={String(stores.length)} />
        <SummaryCell label="synced" value={String(synced)} tone="success" />
      </div>

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
        <FilterButton label="all" active={filter === "all"} count={secrets.length} onClick={() => setFilter("all")} />
        <FilterButton label="needs attention" active={filter === "attention"} count={attention} onClick={() => setFilter("attention")} />
        <FilterButton label="stale" active={filter === "stale"} count={stale} onClick={() => setFilter("stale")} />
        <FilterButton label="stores" active={filter === "stores"} count={stores.length} onClick={() => setFilter("stores")} />
        <select
          value={namespace}
          onChange={(e) => setNamespace(e.target.value)}
          aria-label="filter external secret namespace"
          style={{ fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)" }}
        >
          <option value="">all namespaces</option>
          {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
        </select>
        <input
          ref={searchRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter external secrets"
          aria-label="filter external secrets"
          style={{ marginLeft: "auto", fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 180 }}
        />
        {(visibleSecrets.length !== secrets.length || namespace) && filter !== "stores" && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleSecrets.length} of {secrets.length}</span>}
      </div>

      {eso.loading && secrets.length === 0 && stores.length === 0 ? (
        <SkeletonRows rows={7} label="loading external secret state" />
      ) : filter === "stores" ? (
        <StoresPanel stores={orderedStores} />
      ) : secrets.length === 0 ? (
        <EmptyState
          icon={<IconShieldLock size={28} stroke={1.2} />}
          title="No ExternalSecrets on this cluster."
          hint="The operator is installed but no ExternalSecret objects exist yet."
        />
      ) : (
        <div style={{ flex: 1, minHeight: 0, display: "grid", gridTemplateColumns: "minmax(440px, 1.25fr) minmax(320px, 0.75fr)", gap: 12, alignItems: "stretch", overflow: "hidden" }}>
          <div style={{ ...frame, overflow: "hidden", minHeight: 0, display: "flex", flexDirection: "column" }}>
            <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.3fr) minmax(120px,1fr) 96px 64px 84px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
              <span />
              <span>external secret</span>
              <span>store</span>
              <span>target</span>
              <span>refreshed</span>
              <span>status</span>
            </div>
            <div data-testid="eso-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleSecrets.length === 0 ? (
                <EmptyState
                  title="No external secrets match the current filter."
                  action={{ label: "Clear filters", onClick: () => { setFilter("all"); setQuery(""); setNamespace(""); } }}
                />
              ) : visibleSecrets.map((e) => {
                const k = keyOf(e);
                return (
                  <SecretRow
                    key={k}
                    e={e}
                    open={eso.selectedKey === k}
                    kbSelected={kbKey === k}
                    onClick={() => (eso.selectedKey === k ? select(null) : select(k))}
                  />
                );
              })}
            </div>
          </div>

          <div data-testid="eso-inspector" style={{ minHeight: 0, overflowY: "auto" }}>
            {selected ? <SecretInspector e={selected} store={resolveStore(selected, stores)} /> : <EmptyInspector attention={attention} />}
          </div>
        </div>
      )}
    </div>
  );
}

function SecretRow({ e, open, kbSelected, onClick }: { e: ExternalSecretDTO; open: boolean; kbSelected: boolean; onClick: () => void }) {
  const bad = e.state === "Failed";
  // Keep the keyboard selection visible as j/k moves it past the fold.
  // Optional call: jsdom has no scrollIntoView.
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
        boxShadow: kbSelected ? "inset 2px 0 0 var(--color-text-info)" : undefined,
      }}
    >
      <span style={{ alignSelf: "stretch", background: bad ? "var(--color-text-danger)" : e.stale ? "var(--color-text-warning)" : "transparent" }} />
      <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${e.namespace}/${e.name}`}>
        <span style={{ color: "var(--color-text-tertiary)" }}>{e.namespace}/</span>{e.name}
      </span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={`${e.storeKind}/${e.storeName}`}>{e.storeName}</span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={e.targetSecret}>{e.targetSecret}</span>
      <span style={{ ...mono, fontSize: 11, color: e.stale ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{refreshedText(e)}</span>
      <span style={{ display: "flex", alignItems: "center", gap: 5, minWidth: 0 }}>
        {e.stale ? (
          // A stale secret never renders as confidently synced: the amber chip
          // replaces the green state until the controller refreshes again.
          <StaleChip e={e} />
        ) : (
          <>
            <StatusDot size={6} color={readyColor[e.state] ?? readyColor.Unknown} />
            <span style={{ ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }}>{e.state.toLowerCase()}</span>
          </>
        )}
      </span>
    </button>
  );
}

function StaleChip({ e }: { e: ExternalSecretDTO }) {
  return (
    <span
      title={staleTitle(e)}
      style={{ ...mono, fontSize: 10, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, padding: "1px 6px", background: "var(--color-background-secondary)", color: "var(--color-text-warning)" }}
    >
      stale
    </span>
  );
}

function SecretInspector({ e, store }: { e: ExternalSecretDTO; store: ExternalSecretStoreDTO | undefined }) {
  const storeBroken = store && store.state !== "Synced";
  const cause = failureHint(e, store);
  return (
    <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
      <div>
        <div style={{ display: "flex", alignItems: "center", gap: 2 }}>
          <div style={{ ...mono, fontSize: 13, fontWeight: 500 }}>{e.namespace}/{e.name}</div>
          <CopyButton text={e.name} label={`copy external secret name ${e.name}`} />
        </div>
        <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 5, marginTop: 6 }}>
          <EsoBadge tone={e.state === "Synced" && !e.stale ? "success" : e.state === "Failed" ? "danger" : "warning"}>{e.stale ? "stale" : e.state.toLowerCase()}</EsoBadge>
          {e.stale && e.state === "Synced" && <EsoBadge tone="warning">last sync ok, refresh stopped</EsoBadge>}
          {storeBroken && <EsoBadge tone="danger">store problem</EsoBadge>}
          {e.reason && <EsoBadge tone={e.state === "Failed" ? "danger" : "muted"}>{e.reason}</EsoBadge>}
        </div>
      </div>

      {cause && (
        <div style={{ border: "0.5px solid var(--color-text-warning)", borderRadius: "var(--border-radius-md)", padding: "8px 10px", background: "var(--color-background-secondary)" }}>
          <div style={{ fontSize: 11, color: "var(--color-text-warning)", fontWeight: 600 }}>likely cause</div>
          <div style={{ fontSize: 11, color: "var(--color-text-secondary)", marginTop: 3 }}>{cause}</div>
        </div>
      )}

      {/* A failing bound store is the headline for a stuck secret (M10/M11 pattern). */}
      {storeBroken && (
        <div style={{ border: "0.5px solid var(--color-text-danger)", borderRadius: "var(--border-radius-md)", padding: "8px 10px", background: "var(--color-background-secondary)" }}>
          <div style={{ fontSize: 11, color: "var(--color-text-danger)", fontWeight: 500 }}>store not ready: {store!.reason || "unknown"}</div>
          <div style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)", marginTop: 2 }}>{store!.kind}/{store!.name}</div>
          {store!.message && <div style={{ fontSize: 11, color: "var(--color-text-secondary)", marginTop: 3 }}>{store!.message}</div>}
        </div>
      )}

      <Section title="store">
        <Field label="ref" value={`${e.storeKind}/${e.storeName}`} />
        <Field label="health" value={store ? store.state : "not found"} tone={store ? (store.state === "Synced" ? "success" : "danger") : "muted"} />
        {store?.provider && <Field label="provider" value={store.provider} />}
        {store?.message && <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{store.message}</div>}
      </Section>

      <Section title="target secret">
        {e.targetSecret ? (
          <div style={{ display: "flex", gap: 8, alignItems: "baseline", fontSize: 11 }}>
            <span style={{ color: "var(--color-text-tertiary)", width: 72, flexShrink: 0 }}>name</span>
            <button
              onClick={() => {
                const nav = useFleet.getState();
                nav.openResource(SECRET_REF);
                nav.openInstance(e.namespace, e.targetSecret);
              }}
              title={`open Secret ${e.namespace}/${e.targetSecret}`}
              style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere", textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2 }}
            >{e.targetSecret}</button>
          </div>
        ) : (
          <Field label="name" value="—" />
        )}
      </Section>

      <Section title="refresh">
        <Field label="interval" value={e.refreshIntervalSeconds > 0 ? fmtDuration(e.refreshIntervalSeconds) : "disabled"} />
        <Field
          label="last refresh"
          value={e.lastRefreshAgeSeconds > 0 ? `${fmtDuration(e.lastRefreshAgeSeconds)} ago` : "never synced"}
          tone={e.stale ? "warning" : e.lastRefreshAgeSeconds > 0 ? "muted" : "danger"}
        />
      </Section>

      {e.message && (
        <Section title="message">
          <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{e.message}</div>
        </Section>
      )}
    </div>
  );
}

function failureHint(e: ExternalSecretDTO, store: ExternalSecretStoreDTO | undefined): string {
  if (store && store.state !== "Synced") return `${store.kind}/${store.name} is not ready${store.reason ? `: ${store.reason}` : ""}.`;
  if (!store) return `${e.storeKind}/${e.storeName} was not found in the watched store list.`;
  if (e.state === "Failed") return `The last sync attempt failed${e.reason ? `: ${e.reason}` : ""}.`;
  if (e.stale) return `The controller has stopped refreshing: last successful sync was ${fmtDuration(e.lastRefreshAgeSeconds)} ago against a ${fmtDuration(e.refreshIntervalSeconds)} refresh interval.`;
  if (e.lastRefreshAgeSeconds === 0 && e.state !== "Synced") return "This secret has never synced successfully.";
  return "";
}

// EsoBadge is deliberately NOT the Badge primitive: it is a mono 10px chip on
// a neutral border/surface with only the text toned — none of Badge's variants
// (toned border, toned bg) renders that. It does share the primitive tone map.
function EsoBadge({ children, tone }: { children: React.ReactNode; tone: "success" | "info" | "warning" | "danger" | "muted" }) {
  return <span style={{ ...mono, fontSize: 10, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, padding: "1px 6px", background: "var(--color-background-secondary)", color: toneColor[tone] }}>{children}</span>;
}

function StoresPanel({ stores }: { stores: ExternalSecretStoreDTO[] }) {
  if (stores.length === 0) {
    return <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No secret stores found.</div>;
  }
  return (
    <div style={{ ...frame, overflow: "hidden", minHeight: 0, flex: 1, display: "flex", flexDirection: "column" }}>
      <div data-testid="store-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {stores.map((s) => (
          <div key={`${s.kind}/${s.namespace}/${s.name}`} style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.4fr) 130px 110px minmax(120px,1fr)", gap: 10, alignItems: "center", padding: "7px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <span style={{ alignSelf: "stretch", background: s.state === "Failed" ? "var(--color-text-danger)" : s.state !== "Synced" ? "var(--color-text-warning)" : "transparent" }} />
            <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${s.kind}/${s.namespace}/${s.name}`}>
              {s.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{s.namespace}/</span>}{s.name}
            </span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{s.kind}</span>
            <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={s.provider}>{s.provider || "—"}</span>
            <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
              <StatusDot size={6} color={readyColor[s.state] ?? readyColor.Unknown} />
              <span style={{ ...ellipsis, fontSize: 11, color: s.state === "Failed" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }} title={s.message}>{s.state !== "Synced" && s.reason ? s.reason : s.state.toLowerCase()}</span>
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function EmptyInspector({ attention }: { attention: number }) {
  return (
    <div style={{ ...frame, padding: 18, color: "var(--color-text-secondary)", fontSize: 12, height: "100%", boxSizing: "border-box" }}>
      {attention > 0 ? "Select an external secret to inspect its store and refresh state." : "All external secrets are synced. Select one to inspect it."}
    </div>
  );
}
