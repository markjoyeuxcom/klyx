import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ArgoApplicationSetDTO, ArgoAppDTO, ArgoProjectDTO, ArgoResourceDTO, ArgoResourceRuleDTO, ArgoViewMode, CRDGroupDTO, ResourceRef } from "../store/fleet";
import { listArgoApplicationSets, listArgoApps, listArgoProjects, refreshArgoApp, syncArgoApp } from "../bridge/argo";
import type { ArgoSyncOptions } from "../bridge/argo";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { EmptyState, SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { IconGitMerge } from "@tabler/icons-react";
import { InspectorEmpty, InspectorSection, InspectorTitle } from "./ResourceInspector";

// ArgoView is the Argo CD lens, speaking Argo's vocabulary (synced/degraded,
// never translated into Flux terms). Apps arrive broken-first from the
// backend. On-demand list + refresh; the two imperative verbs (refresh, sync)
// mirror the Flux reconcile pattern: ConfirmDialog + Protected gating, never
// authoring desired state, never pruning.

const keyOf = (a: ArgoAppDTO) => `${a.namespace}/${a.name}`;
const arrayOf = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);

const syncColor: Record<string, string> = {
  Synced: "var(--color-text-success)",
  OutOfSync: "var(--color-text-warning)",
};
const healthColor: Record<string, string> = {
  Healthy: "var(--color-text-success)",
  Progressing: "var(--color-text-info)",
  Degraded: "var(--color-text-danger)",
  Missing: "var(--color-text-danger)",
  Suspended: "var(--color-text-warning)",
};

function ago(unix: number): string {
  if (unix === 0) return "never";
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix);
  return s < 60 ? `${s}s ago` : s < 3600 ? `${Math.floor(s / 60)}m ago` : `${Math.floor(s / 3600)}h ago`;
}

const short = (rev: string) => (rev.length > 8 ? rev.slice(0, 8) : rev);

type Pending = { verb: "refresh"; app: ArgoAppDTO };

const gridCols = "12px minmax(0,1.2fr) 90px 110px minmax(0,1fr) 90px 90px 132px";

const argoResourceRefs: Record<string, ResourceRef> = {
  "apps/Deployment": { group: "apps", version: "v1", plural: "deployments", kind: "Deployment", scope: "Namespaced" },
  "apps/StatefulSet": { group: "apps", version: "v1", plural: "statefulsets", kind: "StatefulSet", scope: "Namespaced" },
  "apps/DaemonSet": { group: "apps", version: "v1", plural: "daemonsets", kind: "DaemonSet", scope: "Namespaced" },
  "apps/ReplicaSet": { group: "apps", version: "v1", plural: "replicasets", kind: "ReplicaSet", scope: "Namespaced" },
  "/Pod": { group: "", version: "v1", plural: "pods", kind: "Pod", scope: "Namespaced" },
  "/Service": { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" },
  "/ConfigMap": { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" },
  "/Secret": { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
  "/ServiceAccount": { group: "", version: "v1", plural: "serviceaccounts", kind: "ServiceAccount", scope: "Namespaced" },
  "/Namespace": { group: "", version: "v1", plural: "namespaces", kind: "Namespace", scope: "Cluster" },
  "networking.k8s.io/Ingress": { group: "networking.k8s.io", version: "v1", plural: "ingresses", kind: "Ingress", scope: "Namespaced" },
  "networking.k8s.io/NetworkPolicy": { group: "networking.k8s.io", version: "v1", plural: "networkpolicies", kind: "NetworkPolicy", scope: "Namespaced" },
  "batch/Job": { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" },
  "batch/CronJob": { group: "batch", version: "v1", plural: "cronjobs", kind: "CronJob", scope: "Namespaced" },
  "autoscaling/HorizontalPodAutoscaler": { group: "autoscaling", version: "v2", plural: "horizontalpodautoscalers", kind: "HorizontalPodAutoscaler", scope: "Namespaced" },
  "policy/PodDisruptionBudget": { group: "policy", version: "v1", plural: "poddisruptionbudgets", kind: "PodDisruptionBudget", scope: "Namespaced" },
  "gateway.networking.k8s.io/Gateway": { group: "gateway.networking.k8s.io", version: "v1", plural: "gateways", kind: "Gateway", scope: "Namespaced" },
  "gateway.networking.k8s.io/HTTPRoute": { group: "gateway.networking.k8s.io", version: "v1", plural: "httproutes", kind: "HTTPRoute", scope: "Namespaced" },
  "gateway.networking.k8s.io/GRPCRoute": { group: "gateway.networking.k8s.io", version: "v1", plural: "grpcroutes", kind: "GRPCRoute", scope: "Namespaced" },
  "cert-manager.io/Certificate": { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
  "cert-manager.io/CertificateRequest": { group: "cert-manager.io", version: "v1", plural: "certificaterequests", kind: "CertificateRequest", scope: "Namespaced" },
  "cert-manager.io/Issuer": { group: "cert-manager.io", version: "v1", plural: "issuers", kind: "Issuer", scope: "Namespaced" },
  "cert-manager.io/ClusterIssuer": { group: "cert-manager.io", version: "v1", plural: "clusterissuers", kind: "ClusterIssuer", scope: "Cluster" },
  "external-secrets.io/ExternalSecret": { group: "external-secrets.io", version: "v1beta1", plural: "externalsecrets", kind: "ExternalSecret", scope: "Namespaced" },
  "external-secrets.io/SecretStore": { group: "external-secrets.io", version: "v1beta1", plural: "secretstores", kind: "SecretStore", scope: "Namespaced" },
  "external-secrets.io/ClusterSecretStore": { group: "external-secrets.io", version: "v1beta1", plural: "clustersecretstores", kind: "ClusterSecretStore", scope: "Cluster" },
};

export function resolveArgoResourceRef(resource: ArgoResourceDTO, crdGroups: CRDGroupDTO[] = []): ResourceRef | null {
  const crdGroup = crdGroups.find((g) => g.group === resource.group);
  const crdKind = crdGroup?.kinds.find((k) => k.kind === resource.kind);
  if (crdGroup && crdKind) {
    return {
      group: crdGroup.group,
      version: crdKind.version,
      plural: crdKind.plural,
      kind: crdKind.kind,
      scope: crdKind.scope,
    };
  }
  return argoResourceRefs[`${resource.group || ""}/${resource.kind}`] ?? null;
}

export function ArgoView({ cluster, mode = "applications" }: { cluster: string; mode?: ArgoViewMode }) {
  const argo = useFleet((s) => s.argo);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [pending, setPending] = useState<Pending | null>(null);
  const [syncTarget, setSyncTarget] = useState<ArgoAppDTO | null>(null);

  // Palette verbs land here as one-shot intents once the app list is loaded.
  // Sync opens the composer (prune/selective options + prd arming); refresh
  // its confirm - the palette never bypasses either.
  useNavIntent(cluster, ["sync-argo", "refresh-argo"], (intent) => {
    if (argo.loading || argo.apps.length === 0) return "wait";
    if (!("namespace" in intent)) return "missing";
    const app = argo.apps.find((a) => a.namespace === intent.namespace && a.name === intent.name);
    if (!app) return "missing";
    if (intent.kind === "sync-argo") setSyncTarget(app);
    else setPending({ verb: "refresh", app });
    return "consumed";
  }, [argo.apps, argo.loading]);

  useEffect(() => {
    if (mode === "projects") {
      void listArgoProjects(cluster);
      void listArgoApps(cluster);
      void listArgoApplicationSets(cluster);
    } else if (mode === "applicationsets") {
      void listArgoApplicationSets(cluster);
      void listArgoApps(cluster);
      void listArgoProjects(cluster);
    } else {
      void listArgoApps(cluster);
    }
    return () => useFleet.getState().clearArgo();
  }, [cluster, mode]);

  const broken = argo.apps.filter((a) => a.broken).length;
  const brokenAppSets = argo.applicationSets.filter((a) => !a.healthy).length;
  const count = mode === "projects" ? argo.projects.length : mode === "applicationsets" ? argo.applicationSets.length : argo.apps.length;
  const noun = mode === "projects" ? "project" : mode === "applicationsets" ? "ApplicationSet" : "application";
  const refresh = () => {
    if (mode === "projects") void listArgoProjects(cluster);
    else if (mode === "applicationsets") void listArgoApplicationSets(cluster);
    else void listArgoApps(cluster);
  };

  return (
    <div style={{ padding: "16px 20px" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 10 }}>
        <span style={{ fontSize: 12, color: "var(--color-text-secondary)" }}>
          {count} {noun}{count === 1 ? "" : "s"}
          {mode === "applications" && broken > 0 && <span style={{ color: "var(--color-text-danger)" }}> · {broken} need attention</span>}
          {mode === "applicationsets" && brokenAppSets > 0 && <span style={{ color: "var(--color-text-danger)" }}> · {brokenAppSets} need attention</span>}
        </span>
        <button onClick={refresh} style={btn}>refresh</button>
      </div>
      <ArgoModeTabs mode={mode} />

      {mode === "projects" ? (
        <ProjectsView argo={argo} />
      ) : mode === "applicationsets" ? (
        <ApplicationSetsView argo={argo} />
      ) : argo.loading && argo.apps.length === 0 ? (
        <SkeletonRows rows={6} label="loading applications" />
      ) : !argo.available ? (
        <div style={emptyStyle}>{argo.message || "Argo CD is not available on this cluster."}</div>
      ) : argo.apps.length === 0 ? (
        <EmptyState
          icon={<IconGitMerge size={28} stroke={1.2} />}
          title="No Argo CD applications on this cluster."
          hint="Argo CD is installed but no Application objects exist yet."
        />
      ) : (
        <ApplicationsView
          cluster={cluster}
          argo={argo}
          onRefresh={(app) => setPending({ verb: "refresh", app })}
          onSync={(app) => setSyncTarget(app)}
        />
      )}

      {pending && (
        <ConfirmDialog
          title="refresh application"
          cluster={cluster}
          detail={`${keyOf(pending.app)} - re-compare against ${pending.app.targetRevision || "HEAD"}`}
          protected={isProtected}
          confirmLabel="Refresh"
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { app } = pending;
            setPending(null);
            void refreshArgoApp(cluster, app.namespace, app.name);
          }}
        />
      )}
      {syncTarget && (
        <SyncApplicationDialog
          cluster={cluster}
          app={syncTarget}
          isProtected={isProtected}
          onCancel={() => setSyncTarget(null)}
          onConfirm={(options) => {
            const app = syncTarget;
            setSyncTarget(null);
            void syncArgoApp(cluster, app.namespace, app.name, options);
          }}
        />
      )}
    </div>
  );
}

function ArgoModeTabs({ mode }: { mode: ArgoViewMode }) {
  const openArgo = useFleet((s) => s.openArgo);
  const tabs: { mode: ArgoViewMode; label: string }[] = [
    { mode: "applications", label: "Applications" },
    { mode: "projects", label: "Projects" },
    { mode: "applicationsets", label: "ApplicationSets" },
  ];
  return (
    <div role="tablist" aria-label="Argo CD views" style={{ display: "inline-flex", gap: 2, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, overflow: "hidden", marginBottom: 12 }}>
      {tabs.map((t) => (
        <button
          key={t.mode}
          role="tab"
          aria-selected={mode === t.mode}
          onClick={() => openArgo(t.mode)}
          style={{
            ...btn,
            border: "none",
            borderRadius: 0,
            background: mode === t.mode ? "var(--color-background-secondary)" : "transparent",
            color: mode === t.mode ? "var(--color-text-primary)" : "var(--color-text-secondary)",
          }}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

function ApplicationsView({ cluster, argo, onRefresh, onSync }: { cluster: string; argo: ReturnType<typeof useFleet.getState>["argo"]; onRefresh: (app: ArgoAppDTO) => void; onSync: (app: ArgoAppDTO) => void }) {
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [kbKey, setKbKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [attentionOnly, setAttentionOnly] = useState(false);
  const apps = useMemo(() => argo.apps.filter((a) => matchesAppQuery(a, query) && (!attentionOnly || a.broken)), [argo.apps, attentionOnly, query]);
  const selected = apps.find((a) => keyOf(a) === selectedKey) ?? apps[0] ?? null;

  // Keyboard nav (j/k/Enter/Esc) over the visible app rows - applications
  // mode only; projects and applicationsets stay mouse-driven. Selection is
  // keyed by keyOf: when the kb-selected app leaves the visible set (filter
  // change) this resolves to -1 — no selection — instead of silently pointing
  // at whatever row took its index.
  const kbIdx = kbKey === null ? -1 : apps.findIndex((a) => keyOf(a) === kbKey);

  const handleKbSelect = useCallback((idx: number) => {
    const a = apps[idx];
    if (a) setKbKey(keyOf(a));
  }, [apps]);

  const handleActivate = useCallback((idx: number) => {
    const a = apps[idx];
    if (a) setSelectedKey(keyOf(a));
  }, [apps]);

  const handleEscape = useCallback(() => {
    if (selectedKey) setSelectedKey(null);
  }, [selectedKey]);

  useListKeys({
    count: apps.length,
    selected: kbIdx,
    onSelect: handleKbSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
  });

  return (
    <div style={splitPaneWide}>
      <div style={{ minWidth: 0, fontFamily: "var(--font-mono)", fontSize: 12 }}>
        <FilterBar
          query={query}
          onQuery={setQuery}
          placeholder="filter applications"
          toggles={[{ label: "needs attention", active: attentionOnly, count: argo.apps.filter((a) => a.broken).length, onClick: () => setAttentionOnly((v) => !v) }]}
          visible={apps.length}
          total={argo.apps.length}
        />
        <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
          <span /><span>application</span><span>sync</span><span>health</span><span>destination</span><span>revision</span><span>reconciled</span><span>actions</span>
        </div>
        {apps.length === 0 ? <EmptyInline text="No applications match the current filter." /> : apps.map((a, i) => (
          <ApplicationRow
            key={keyOf(a)}
            app={a}
            selected={selected ? keyOf(selected) === keyOf(a) : false}
            kbSelected={i === kbIdx}
            onSelect={() => setSelectedKey(keyOf(a))}
            onRefresh={() => onRefresh(a)}
            onSync={() => onSync(a)}
          />
        ))}
      </div>
      <ApplicationInspector
        cluster={cluster}
        app={selected}
        project={selected ? argo.projects.find((p) => p.name === selected.project) ?? null : null}
        appSet={selected ? applicationSetForApp(argo.applicationSets, selected) : null}
        onRefresh={selected ? () => onRefresh(selected) : undefined}
        onSync={selected ? () => onSync(selected) : undefined}
      />
    </div>
  );
}

function ApplicationRow({ app: a, selected, kbSelected, onSelect, onRefresh, onSync }: { app: ArgoAppDTO; selected: boolean; kbSelected: boolean; onSelect: () => void; onRefresh: () => void; onSync: () => void }) {
  // Keep the kb-selected row visible while j/k walks the list. Optional call:
  // jsdom has no scrollIntoView.
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (kbSelected) ref.current?.scrollIntoView?.({ block: "nearest" });
  }, [kbSelected]);
  return (
    <div
      ref={ref}
      role="button"
      tabIndex={0}
      aria-selected={kbSelected}
      onClick={onSelect}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onSelect(); } }}
      style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center", width: "100%", padding: "7px 8px", border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)", background: kbSelected || selected ? "var(--color-background-secondary)" : "transparent", boxShadow: kbSelected ? "inset 2px 0 0 var(--color-text-info)" : undefined, color: "var(--color-text-primary)", textAlign: "left", cursor: "pointer" }}
    >
      <span style={{ width: 8, height: 8, borderRadius: "50%", background: a.broken ? "var(--color-text-danger)" : "var(--color-text-success)" }} />
      <span style={ellipsis} title={keyOf(a)}>
        <span style={{ fontWeight: 500 }}>{a.name}</span>
        {a.project && a.project !== "default" && <span style={{ color: "var(--color-text-tertiary)", marginLeft: 6, fontSize: 10 }}>{a.project}</span>}
        {a.autoSync && <span style={{ color: "var(--color-text-tertiary)", marginLeft: 6, fontSize: 9 }} title="automated sync policy">auto</span>}
      </span>
      <span style={{ color: syncColor[a.syncStatus] ?? "var(--color-text-tertiary)" }}>{a.syncStatus}</span>
      <span style={{ color: healthColor[a.healthStatus] ?? "var(--color-text-tertiary)" }}>{a.healthStatus}</span>
      <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={a.destNamespace}>{a.destNamespace || "-"}</span>
      <span style={{ color: "var(--color-text-tertiary)" }} title={a.revision}>{a.revision ? short(a.revision) : "-"}</span>
      <span style={{ color: "var(--color-text-tertiary)" }}>{ago(a.reconciledUnix)}</span>
      <span style={{ display: "flex", alignItems: "center", gap: 6 }}>
        <button onClick={(e) => { e.stopPropagation(); onRefresh(); }} style={smallBtn}>refresh</button>
        <button onClick={(e) => { e.stopPropagation(); onSync(); }} style={smallPrimaryBtn}>sync</button>
      </span>
    </div>
  );
}

function ProjectsView({ argo }: { argo: ReturnType<typeof useFleet.getState>["argo"] }) {
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [riskyOnly, setRiskyOnly] = useState(false);
  const [unusedOnly, setUnusedOnly] = useState(false);
  const projects = useMemo(() => [...argo.projects]
    .filter((p) => matchesProjectQuery(p, query))
    .filter((p) => !riskyOnly || projectRiskBadges(p, appsForProject(argo.apps, p.name)).some((r) => r.tone === "danger" || r.tone === "warning"))
    .filter((p) => !unusedOnly || appsForProject(argo.apps, p.name).length === 0)
    .sort((a, b) => projectRiskScore(b, argo.apps) - projectRiskScore(a, argo.apps) || a.name.localeCompare(b.name)), [argo.apps, argo.projects, query, riskyOnly, unusedOnly]);
  const selected = projects.find((p) => projectKey(p) === selectedKey) ?? projects[0] ?? null;
  if (argo.projectsLoading && argo.projects.length === 0) return <SkeletonRows rows={5} label="loading projects" />;
  if (!argo.projectsAvailable) return <div style={emptyStyle}>{argo.projectsMessage || "Argo CD projects are not available on this cluster."}</div>;
  if (argo.projects.length === 0) {
    return <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title="No Argo CD projects on this cluster." hint="Projects define allowed repos, destinations, and roles." />;
  }
  return (
    <div style={splitPane}>
      <div style={{ minWidth: 0, fontFamily: "var(--font-mono)", fontSize: 12 }}>
        <FilterBar
          query={query}
          onQuery={setQuery}
          placeholder="filter projects"
          toggles={[
            { label: "risky policy", active: riskyOnly, count: argo.projects.filter((p) => projectRiskBadges(p, appsForProject(argo.apps, p.name)).some((r) => r.tone === "danger" || r.tone === "warning")).length, onClick: () => setRiskyOnly((v) => !v) },
            { label: "unused", active: unusedOnly, count: argo.projects.filter((p) => appsForProject(argo.apps, p.name).length === 0).length, onClick: () => setUnusedOnly((v) => !v) },
          ]}
          visible={projects.length}
          total={argo.projects.length}
        />
        <div style={headerGrid("12px minmax(150px, 0.9fr) minmax(0, 1fr) 86px 92px")}>
          <span /><span>project</span><span>policy signal</span><span>apps</span><span>appsets</span>
        </div>
        {projects.length === 0 ? <EmptyInline text="No projects match the current filter." /> : projects.map((p) => (
          <ProjectRow
            key={projectKey(p)}
            project={p}
            selected={selected ? projectKey(selected) === projectKey(p) : false}
            apps={appsForProject(argo.apps, p.name)}
            appSets={appSetsForProject(argo.applicationSets, p.name)}
            onSelect={() => setSelectedKey(projectKey(p))}
          />
        ))}
      </div>
      <ProjectInspector project={selected} apps={selected ? appsForProject(argo.apps, selected.name) : []} appSets={selected ? appSetsForProject(argo.applicationSets, selected.name) : []} />
    </div>
  );
}

function ProjectRow({ project: p, selected, apps, appSets, onSelect }: { project: ArgoProjectDTO; selected: boolean; apps: ArgoAppDTO[]; appSets: ArgoApplicationSetDTO[]; onSelect: () => void }) {
  const risks = projectRiskBadges(p, apps);
  return (
    <button onClick={onSelect} style={{ display: "grid", gridTemplateColumns: "12px minmax(150px, 0.9fr) minmax(0, 1fr) 86px 92px", gap: 10, alignItems: "center", width: "100%", padding: "8px", border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)", background: selected ? "var(--color-background-secondary)" : "transparent", color: "var(--color-text-primary)", textAlign: "left", cursor: "pointer" }}>
      <span style={{ width: 8, height: 8, borderRadius: "50%", background: risks.some((r) => r.tone === "danger") ? "var(--color-text-danger)" : risks.some((r) => r.tone === "warning") ? "var(--color-text-warning)" : "var(--color-text-success)" }} />
      <span style={ellipsis} title={p.description || p.name}>
        <span style={{ fontWeight: 500 }}>{p.name}</span>
        {p.description && <span style={{ color: "var(--color-text-tertiary)", marginLeft: 6 }}>{p.description}</span>}
      </span>
      <span style={{ display: "flex", flexWrap: "wrap", gap: 4, minWidth: 0 }}>{risks.length === 0 ? <span style={{ color: "var(--color-text-tertiary)" }}>restricted</span> : risks.slice(0, 2).map((r) => <RiskBadge key={r.label} tone={r.tone}>{r.label}</RiskBadge>)}</span>
      <span>{apps.length}</span>
      <span>{appSets.length}</span>
    </button>
  );
}

function ApplicationSetsView({ argo }: { argo: ReturnType<typeof useFleet.getState>["argo"] }) {
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [attentionOnly, setAttentionOnly] = useState(false);
  const [missingOnly, setMissingOnly] = useState(false);
  const appSets = useMemo(() => [...argo.applicationSets]
    .filter((a) => matchesAppSetQuery(a, query))
    .filter((a) => !attentionOnly || !a.healthy)
    .filter((a) => !missingOnly || missingGeneratedNames(a, argo.apps).length > 0)
    .sort((a, b) => Number(a.healthy) - Number(b.healthy) || a.name.localeCompare(b.name)), [argo.applicationSets, argo.apps, attentionOnly, missingOnly, query]);
  const selected = appSets.find((a) => appSetKey(a) === selectedKey) ?? appSets[0] ?? null;
  if (argo.applicationSetsLoading && argo.applicationSets.length === 0) return <SkeletonRows rows={5} label="loading applicationsets" />;
  if (!argo.applicationSetsAvailable) return <div style={emptyStyle}>{argo.applicationSetsMessage || "Argo CD ApplicationSets are not available on this cluster."}</div>;
  if (argo.applicationSets.length === 0) {
    return <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title="No ApplicationSets on this cluster." hint="ApplicationSets generate Applications from clusters, Git, pull requests, and other generators." />;
  }
  return (
    <div style={splitPane}>
      <div style={{ minWidth: 0, fontFamily: "var(--font-mono)", fontSize: 12 }}>
        <FilterBar
          query={query}
          onQuery={setQuery}
          placeholder="filter applicationsets"
          toggles={[
            { label: "needs attention", active: attentionOnly, count: argo.applicationSets.filter((a) => !a.healthy).length, onClick: () => setAttentionOnly((v) => !v) },
            { label: "missing apps", active: missingOnly, count: argo.applicationSets.filter((a) => missingGeneratedNames(a, argo.apps).length > 0).length, onClick: () => setMissingOnly((v) => !v) },
          ]}
          visible={appSets.length}
          total={argo.applicationSets.length}
        />
        <div style={headerGrid("12px minmax(150px, 0.9fr) 116px 72px minmax(0, 1fr) 104px")}>
          <span /><span>applicationset</span><span>generators</span><span>apps</span><span>source</span><span>project</span>
        </div>
        {appSets.length === 0 ? <EmptyInline text="No ApplicationSets match the current filter." /> : appSets.map((a) => (
          <ApplicationSetRow
            key={appSetKey(a)}
            appSet={a}
            selected={selected ? appSetKey(selected) === appSetKey(a) : false}
            generatedApps={generatedAppsForSet(argo.apps, a)}
            onSelect={() => setSelectedKey(appSetKey(a))}
          />
        ))}
      </div>
      <ApplicationSetInspector appSet={selected} allApps={argo.apps} project={selected ? argo.projects.find((p) => p.name === selected.project) ?? null : null} />
    </div>
  );
}

function ApplicationSetRow({ appSet: a, selected, generatedApps, onSelect }: { appSet: ArgoApplicationSetDTO; selected: boolean; generatedApps: ArgoAppDTO[]; onSelect: () => void }) {
  const conditions = arrayOf(a.conditions);
  const condition = conditions.find((c) => c.status !== "True") ?? conditions[0];
  return (
    <div>
      <button onClick={onSelect} style={{ display: "grid", gridTemplateColumns: "12px minmax(150px, 0.9fr) 116px 72px minmax(0, 1fr) 104px", gap: 10, alignItems: "center", width: "100%", padding: "8px", border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)", background: selected ? "var(--color-background-secondary)" : "transparent", color: "var(--color-text-primary)", textAlign: "left", cursor: "pointer" }}>
        <span style={{ width: 8, height: 8, borderRadius: "50%", background: a.healthy ? "var(--color-text-success)" : "var(--color-text-danger)" }} />
        <span style={ellipsis} title={`${a.namespace}/${a.name}`}><span style={{ fontWeight: 500 }}>{a.name}</span><span style={{ color: "var(--color-text-tertiary)", marginLeft: 6 }}>{a.namespace}</span></span>
        <span style={ellipsis} title={a.generators.join(", ")}>{a.generators.length === 0 ? "-" : a.generators.join(", ")}</span>
        <span>{a.generatedApps || generatedApps.length || "-"}</span>
        <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={`${a.repoURL} ${a.path}`}>{a.repoURL ? `${a.repoURL}${a.path ? ` · ${a.path}` : ""}` : "-"}</span>
        <span>{a.project || "-"}</span>
      </button>
      {condition && !a.healthy && (
        <div style={{ padding: "6px 8px 8px 30px", background: "var(--color-background-secondary)", borderBottom: "0.5px solid var(--color-border-tertiary)", color: "var(--color-text-warning)", fontSize: 11 }}>
          {condition.type}: {condition.reason || condition.status}{condition.message ? ` - ${condition.message}` : ""}
        </div>
      )}
    </div>
  );
}

function ProjectInspector({ project, apps, appSets }: { project: ArgoProjectDTO | null; apps: ArgoAppDTO[]; appSets: ArgoApplicationSetDTO[] }) {
  if (!project) return <InspectorEmpty text="Select a project to inspect policy and generated workload ownership." style={inspector} />;
  const risks = projectRiskBadges(project, apps);
  return (
    <div style={inspector}>
      <InspectorTitle title={project.name} sub={`${project.namespace} · ${apps.length} app${apps.length === 1 ? "" : "s"} · ${appSets.length} ApplicationSet${appSets.length === 1 ? "" : "s"}`} />
      <InspectorSection title="policy signals">
        <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>
          {risks.length === 0 ? <RiskBadge tone="success">restricted</RiskBadge> : risks.map((r) => <RiskBadge key={r.label} tone={r.tone}>{r.label}</RiskBadge>)}
        </div>
      </InspectorSection>
      <InspectorSection title="sources">
        <ValueList values={project.sourceRepos} empty="no source repos declared" />
      </InspectorSection>
      <InspectorSection title="destinations">
        <ValueList values={project.destinations} empty="no destinations declared" />
      </InspectorSection>
      <InspectorSection title="resource policy" collapsible>
        <RuleList label="cluster allow" rules={project.clusterResourceWhitelist} />
        <RuleList label="cluster deny" rules={project.clusterResourceBlacklist} />
        <RuleList label="namespace allow" rules={project.namespaceResourceWhitelist} />
        <RuleList label="namespace deny" rules={project.namespaceResourceBlacklist} />
      </InspectorSection>
      <InspectorSection title="roles" collapsible>
        <ValueList values={project.roleNames ?? []} empty={project.roles > 0 ? `${project.roles} role${project.roles === 1 ? "" : "s"}` : "no project roles"} />
      </InspectorSection>
      <InspectorSection title="applications" collapsible>
        <LinkedApps apps={apps} />
      </InspectorSection>
      <InspectorSection title="applicationsets" collapsible>
        <ValueList values={appSets.map((a) => a.name)} empty="no ApplicationSets target this project" />
      </InspectorSection>
    </div>
  );
}

function ApplicationSetInspector({ appSet, allApps, project }: { appSet: ArgoApplicationSetDTO | null; allApps: ArgoAppDTO[]; project: ArgoProjectDTO | null }) {
  if (!appSet) return <InspectorEmpty text="Select an ApplicationSet to inspect generator output and template policy." style={inspector} />;
  const condition = arrayOf(appSet.conditions).find((c) => c.status !== "True") ?? null;
  const generatedNames = arrayOf(appSet.generatedNames);
  const liveApps = generatedAppsForSet(allApps, appSet);
  const missing = missingGeneratedNames(appSet, allApps);
  const projectApps = appSet.project ? appsForProject(allApps, appSet.project) : [];
  const extraProjectApps = generatedNames.length > 0 ? projectApps.filter((a) => !generatedNames.includes(a.name)) : [];
  const expected = generatedNames.length > 0 ? generatedNames.length : appSet.generatedApps;
  const actual = liveApps.length;
  return (
    <div style={inspector}>
      <InspectorTitle title={appSet.name} sub={`${appSet.namespace} · ${appSet.generators.join(", ") || "generator unknown"}`} />
      <InspectorSection title="health">
        <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>
          <RiskBadge tone={appSet.healthy ? "success" : "danger"}>{appSet.healthy ? "healthy" : "needs attention"}</RiskBadge>
          {expected === 0 && <RiskBadge tone="warning">no generated apps</RiskBadge>}
          {missing.length > 0 && <RiskBadge tone="warning">{missing.length} missing generated</RiskBadge>}
          {extraProjectApps.length > 0 && <RiskBadge tone="muted">{extraProjectApps.length} extra project app{extraProjectApps.length === 1 ? "" : "s"}</RiskBadge>}
          {condition && <RiskBadge tone="warning">{condition.reason || condition.type}</RiskBadge>}
        </div>
        {condition && <div style={{ marginTop: 7, color: "var(--color-text-warning)", fontSize: 11 }}>{condition.type}: {condition.message || condition.reason || condition.status}</div>}
      </InspectorSection>
      <InspectorSection title="generated drift" collapsible>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 8 }}>
          <MetricTile label="expected" value={String(expected)} tone={expected === 0 ? "warning" : undefined} />
          <MetricTile label="actual" value={String(actual)} tone={actual < expected ? "warning" : undefined} />
          <MetricTile label="missing" value={String(missing.length)} tone={missing.length > 0 ? "warning" : undefined} />
        </div>
        {missing.length > 0 && <ValueList values={missing} empty="no missing generated Applications" />}
      </InspectorSection>
      <InspectorSection title="template" collapsible>
        <MetaLine label="name" value={appSet.templateName || "-"} />
        <MetaLine label="project" value={appSet.project || "-"} tone={project ? undefined : "warning"} />
        <MetaLine label="source" value={appSet.repoURL ? `${appSet.repoURL}${appSet.path ? ` · ${appSet.path}` : ""}` : "-"} />
        <MetaLine label="revision" value={appSet.targetRevision || "HEAD"} />
        <MetaLine label="destination" value={appSet.destNamespace || appSet.destServer || "-"} />
      </InspectorSection>
      <InspectorSection title="generated applications" collapsible>
        {generatedNames.length > 0 ? <ValueList values={generatedNames} /> : <ValueList values={liveApps.map((a) => a.name)} empty="Argo has not reported generated resource names yet" />}
      </InspectorSection>
      <InspectorSection title="live application state" collapsible>
        <LinkedApps apps={liveApps} />
      </InspectorSection>
      {extraProjectApps.length > 0 && (
        <InspectorSection title="same-project applications" collapsible>
          <LinkedApps apps={extraProjectApps} />
        </InspectorSection>
      )}
      {generatedNames.length === 0 && appSet.generatedApps > 0 && (
        <div style={mutedLine}>Argo reported {appSet.generatedApps} generated resource{appSet.generatedApps === 1 ? "" : "s"} but did not include generated Application names.</div>
      )}
    </div>
  );
}

function MetricTile({ label, value, tone }: { label: string; value: string; tone?: "warning" }) {
  return (
    <div style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, padding: "7px 8px", background: "var(--color-background-secondary)", minWidth: 0 }}>
      <div style={fieldLabel}>{label}</div>
      <div style={{ marginTop: 4, fontFamily: "var(--font-mono)", fontSize: 16, color: tone === "warning" ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>{value}</div>
    </div>
  );
}

type FilterToggle = { label: string; active: boolean; count: number; onClick: () => void };

function FilterBar({ query, onQuery, placeholder, toggles, visible, total }: { query: string; onQuery: (value: string) => void; placeholder: string; toggles: FilterToggle[]; visible: number; total: number }) {
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center", marginBottom: 10 }}>
      {toggles.map((t) => (
        <button key={t.label} onClick={t.onClick} style={{ ...smallBtn, background: t.active ? "var(--color-background-secondary)" : "var(--color-background-primary)", color: t.active ? "var(--color-text-primary)" : "var(--color-text-secondary)" }}>
          {t.label} {t.count}
        </button>
      ))}
      <input
        value={query}
        onChange={(e) => onQuery(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
        style={{ marginLeft: "auto", fontSize: 11, padding: "4px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 190 }}
      />
      {visible !== total && <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{visible} of {total}</span>}
    </div>
  );
}

function EmptyInline({ text }: { text: string }) {
  return <div style={{ padding: "12px 8px", color: "var(--color-text-secondary)", fontSize: 12, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>{text}</div>;
}

function ApplicationInspector({ cluster, app, project, appSet, onRefresh, onSync }: { cluster: string; app: ArgoAppDTO | null; project: ArgoProjectDTO | null; appSet: ArgoApplicationSetDTO | null; onRefresh?: () => void; onSync?: () => void }) {
  const openResource = useFleet((s) => s.openResource);
  const openInstance = useFleet((s) => s.openInstance);
  const crd = useFleet((s) => s.crd);
  const crdGroups = crd.cluster === cluster ? crd.groups : [];
  if (!app) return <InspectorEmpty text="Select an Application to inspect source, sync state, resources, and ownership." style={inspector} />;
  const conditions = arrayOf(app.conditions);
  const resources = arrayOf(app.resources);
  const source = app.chart ? `chart ${app.chart}` : app.path || "root";
  const badResources = resources.filter((r) => r.status === "OutOfSync" || r.health === "Degraded" || r.health === "Missing");
  const openLiveResource = (resource: ArgoResourceDTO) => {
    const ref = resolveArgoResourceRef(resource, crdGroups);
    if (!ref) return;
    openResource(ref);
    openInstance(ref.scope === "Cluster" ? "" : (resource.namespace || app.destNamespace), resource.name);
  };
  return (
    <div style={inspector}>
      <InspectorTitle title={app.name} sub={`${app.namespace} · ${app.project || "default"} · ${app.destNamespace || "destination unknown"}`} />
      <InspectorSection title="status">
        <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>
          <RiskBadge tone={app.syncStatus === "Synced" ? "success" : "warning"}>{app.syncStatus}</RiskBadge>
          <RiskBadge tone={app.healthStatus === "Healthy" || app.healthStatus === "Progressing" ? "success" : "danger"}>{app.healthStatus}</RiskBadge>
          {app.autoSync && <RiskBadge tone="muted">auto sync</RiskBadge>}
          {conditions.length > 0 && <RiskBadge tone="danger">{conditions.length} condition{conditions.length === 1 ? "" : "s"}</RiskBadge>}
          {badResources.length > 0 && <RiskBadge tone="warning">{badResources.length} resource{badResources.length === 1 ? "" : "s"} need attention</RiskBadge>}
        </div>
      </InspectorSection>
      <InspectorSection title="source">
        <MetaLine label="repo" value={app.repoURL || "-"} />
        <MetaLine label={app.chart ? "chart" : "path"} value={source} />
        <MetaLine label="target" value={app.targetRevision || "HEAD"} />
        <MetaLine label="revision" value={app.revision ? short(app.revision) : "-"} />
        {app.extraSources > 0 && <MetaLine label="extra" value={`${app.extraSources} more source${app.extraSources === 1 ? "" : "s"}`} />}
      </InspectorSection>
      <InspectorSection title="destination">
        <MetaLine label="namespace" value={app.destNamespace || "-"} />
        <MetaLine label="project" value={app.project || "default"} tone={project ? undefined : "warning"} />
        <MetaLine label="appset" value={appSet ? appSet.name : "-"} />
      </InspectorSection>
      <InspectorSection title="operation">
        <MetaLine label="last" value={app.opPhase || "-"} tone={app.opPhase && app.opPhase !== "Succeeded" ? "warning" : undefined} />
        <MetaLine label="message" value={app.opMessage || "-"} />
        <MetaLine label="reconciled" value={ago(app.reconciledUnix)} />
      </InspectorSection>
      {conditions.length > 0 && (
        <InspectorSection title="conditions" collapsible>
          {conditions.map((c, i) => <div key={`${c.type}/${i}`} style={{ color: "var(--color-text-danger)", fontSize: 11 }}>{c.type}: {c.message || "-"}</div>)}
        </InspectorSection>
      )}
      <InspectorSection title="resources" collapsible>
        {resources.length === 0 ? <div style={mutedLine}>Argo has not reported resource inventory for this Application.</div> : (
          <div style={{ display: "grid", gap: 4 }}>
            {resources.map((r) => (
              <ArgoResourceRow key={resourceKey(r)} resource={r} resolvable={resolveArgoResourceRef(r, crdGroups) !== null} onOpen={() => openLiveResource(r)} />
            ))}
          </div>
        )}
      </InspectorSection>
      <div style={{ display: "flex", gap: 8 }}>
        <button onClick={onRefresh} style={btn}>refresh app</button>
        <button onClick={onSync} style={smallPrimaryBtn}>sync</button>
      </div>
    </div>
  );
}

function ArgoResourceRow({ resource: r, resolvable, onOpen }: { resource: ArgoResourceDTO; resolvable: boolean; onOpen: () => void }) {
  const body = (
    <>
      <span style={{ color: resourceTone(r), fontSize: 10 }}>{r.kind}</span>
      <span style={{ ...monoLine, ...ellipsis }} title={`${r.namespace}/${r.name}`}>{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>
      <span style={{ color: resourceTone(r), fontSize: 10, textAlign: "right" }}>{r.status || r.health || "-"}</span>
    </>
  );
  const style: React.CSSProperties = {
    display: "grid",
    gridTemplateColumns: "82px minmax(0,1fr) 76px",
    gap: 7,
    alignItems: "center",
    width: "100%",
    padding: 0,
    border: "none",
    background: "transparent",
    color: "var(--color-text-primary)",
    textAlign: "left",
    fontFamily: "var(--font-mono)",
    cursor: resolvable ? "pointer" : "default",
  };

  if (!resolvable) return <div style={style}>{body}</div>;
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`open ${r.kind} ${r.namespace ? `${r.namespace}/` : ""}${r.name}`}
      title="Open live Kubernetes resource"
      style={style}
    >
      {body}
    </button>
  );
}

type Risk = { label: string; tone: "success" | "warning" | "danger" | "muted" };

function projectKey(p: ArgoProjectDTO): string {
  return `${p.namespace}/${p.name}`;
}

function appSetKey(a: ArgoApplicationSetDTO): string {
  return `${a.namespace}/${a.name}`;
}

function appsForProject(apps: ArgoAppDTO[], project: string): ArgoAppDTO[] {
  return apps.filter((a) => a.project === project).sort((a, b) => Number(b.broken) - Number(a.broken) || a.name.localeCompare(b.name));
}

function appSetsForProject(appSets: ArgoApplicationSetDTO[], project: string): ArgoApplicationSetDTO[] {
  return appSets.filter((a) => a.project === project).sort((a, b) => Number(a.healthy) - Number(b.healthy) || a.name.localeCompare(b.name));
}

function generatedAppsForSet(apps: ArgoAppDTO[], appSet: ArgoApplicationSetDTO): ArgoAppDTO[] {
  const names = new Set(arrayOf(appSet.generatedNames));
  return apps
    .filter((a) => names.has(a.name) || a.applicationSet === appSet.name)
    .sort((a, b) => Number(b.broken) - Number(a.broken) || a.name.localeCompare(b.name));
}

function missingGeneratedNames(appSet: ArgoApplicationSetDTO, apps: ArgoAppDTO[]): string[] {
  const generatedNames = arrayOf(appSet.generatedNames);
  if (generatedNames.length === 0) return [];
  const liveNames = new Set(generatedAppsForSet(apps, appSet).map((a) => a.name));
  return generatedNames.filter((name) => !liveNames.has(name));
}

function applicationSetForApp(appSets: ArgoApplicationSetDTO[], app: ArgoAppDTO): ArgoApplicationSetDTO | null {
  return appSets.find((a) => a.name === app.applicationSet || arrayOf(a.generatedNames).includes(app.name)) ?? null;
}

function matchesText(values: Array<string | undefined>, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return values.some((v) => (v ?? "").toLowerCase().includes(q));
}

function matchesAppQuery(app: ArgoAppDTO, query: string): boolean {
  return matchesText([app.namespace, app.name, app.project, app.destNamespace, app.syncStatus, app.healthStatus, app.repoURL, app.path, app.chart, app.applicationSet], query);
}

function matchesProjectQuery(project: ArgoProjectDTO, query: string): boolean {
  return matchesText([project.namespace, project.name, project.description, ...arrayOf(project.sourceRepos), ...arrayOf(project.destinations), ...arrayOf(project.roleNames)], query);
}

function matchesAppSetQuery(appSet: ArgoApplicationSetDTO, query: string): boolean {
  return matchesText([appSet.namespace, appSet.name, appSet.project, appSet.repoURL, appSet.path, appSet.destNamespace, appSet.destServer, ...arrayOf(appSet.generators), ...arrayOf(appSet.generatedNames)], query);
}

function projectRiskScore(p: ArgoProjectDTO, apps: ArgoAppDTO[]): number {
  return projectRiskBadges(p, apps).reduce((sum, r) => sum + (r.tone === "danger" ? 4 : r.tone === "warning" ? 2 : 1), 0);
}

function projectRiskBadges(p: ArgoProjectDTO, apps: ArgoAppDTO[]): Risk[] {
  const risks: Risk[] = [];
  const repos = arrayOf(p.sourceRepos);
  const destinations = arrayOf(p.destinations);
  if (repos.length === 0) risks.push({ label: "no repos", tone: "warning" });
  if (repos.some(hasWildcard)) risks.push({ label: "wildcard repo", tone: "danger" });
  if (destinations.length === 0) risks.push({ label: "no destinations", tone: "warning" });
  if (destinations.some(hasWildcard)) risks.push({ label: "all destinations", tone: "danger" });
  if (arrayOf(p.clusterResourceWhitelist).some(ruleWildcard)) risks.push({ label: "cluster wildcard", tone: "danger" });
  if (arrayOf(p.namespaceResourceWhitelist).some(ruleWildcard)) risks.push({ label: "namespace wildcard", tone: "warning" });
  if (!p.orphanWarn && apps.length > 0) risks.push({ label: "orphan warn off", tone: "warning" });
  if (apps.length === 0) risks.push({ label: "unused", tone: "muted" });
  return risks;
}

function hasWildcard(value: string): boolean {
  return value === "*" || value.includes("*");
}

function ruleWildcard(rule: ArgoResourceRuleDTO): boolean {
  return hasWildcard(rule.group || "") || hasWildcard(rule.kind || "");
}

function RiskBadge({ children, tone }: { children: React.ReactNode; tone: Risk["tone"] }) {
  const colors: Record<Risk["tone"], { color: string; border: string; bg: string }> = {
    success: { color: "var(--color-text-success)", border: "var(--color-border-success, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    warning: { color: "var(--color-text-warning)", border: "var(--color-border-warning, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    danger: { color: "var(--color-text-danger)", border: "var(--color-border-danger, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    muted: { color: "var(--color-text-tertiary)", border: "var(--color-border-tertiary)", bg: "var(--color-background-secondary)" },
  };
  const c = colors[tone];
  return <span style={{ display: "inline-flex", alignItems: "center", maxWidth: "100%", border: `0.5px solid ${c.border}`, borderRadius: 4, padding: "1px 5px", background: c.bg, color: c.color, fontSize: 10, lineHeight: 1.4, whiteSpace: "nowrap" }}>{children}</span>;
}

function ValueList({ values, empty }: { values: string[]; empty?: string }) {
  const items = arrayOf(values).filter(Boolean);
  if (items.length === 0) return <div style={mutedLine}>{empty ?? "-"}</div>;
  return (
    <div style={{ display: "grid", gap: 4 }}>
      {items.map((v) => <div key={v} title={v} style={{ ...monoLine, ...ellipsis }}>{v}</div>)}
    </div>
  );
}

function RuleList({ label, rules }: { label: string; rules?: ArgoResourceRuleDTO[] }) {
  const items = arrayOf(rules);
  return (
    <div style={{ display: "grid", gridTemplateColumns: "92px minmax(0,1fr)", gap: 8, alignItems: "start" }}>
      <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{label}</span>
      {items.length === 0 ? <span style={mutedLine}>-</span> : (
        <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
          {items.map((r, i) => <RiskBadge key={`${r.group}/${r.kind}/${i}`} tone={ruleWildcard(r) ? "warning" : "muted"}>{`${r.group || "core"}/${r.kind || "*"}`}</RiskBadge>)}
        </span>
      )}
    </div>
  );
}

function LinkedApps({ apps }: { apps: ArgoAppDTO[] }) {
  if (apps.length === 0) return <div style={mutedLine}>no linked Applications loaded</div>;
  return (
    <div style={{ display: "grid", gap: 4 }}>
      {apps.map((a) => (
        <div key={keyOf(a)} style={{ display: "grid", gridTemplateColumns: "8px minmax(0,1fr) 82px", gap: 7, alignItems: "center" }}>
          <span style={{ width: 6, height: 6, borderRadius: "50%", background: a.broken ? "var(--color-text-danger)" : "var(--color-text-success)" }} />
          <span style={{ ...monoLine, ...ellipsis }} title={keyOf(a)}>{a.name}</span>
          <span style={{ color: a.broken ? "var(--color-text-warning)" : "var(--color-text-tertiary)", fontSize: 10 }}>{a.syncStatus}</span>
        </div>
      ))}
    </div>
  );
}

function MetaLine({ label, value, tone }: { label: string; value: string; tone?: "warning" }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "72px minmax(0,1fr)", gap: 8, alignItems: "center" }}>
      <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{label}</span>
      <span title={value} style={{ ...monoLine, ...ellipsis, color: tone === "warning" ? "var(--color-text-warning)" : monoLine.color }}>{value}</span>
    </div>
  );
}

function SyncApplicationDialog({
  cluster,
  app,
  isProtected,
  onCancel,
  onConfirm,
}: {
  cluster: string;
  app: ArgoAppDTO;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (options: ArgoSyncOptions) => void;
}) {
  const defaultRevision = app.targetRevision || "";
  const resources = app.resources ?? [];
  const [revision, setRevision] = useState(defaultRevision);
  const [strategy, setStrategy] = useState<"hook" | "apply">("hook");
  const [dryRun, setDryRun] = useState(false);
  // Prune deletes live resources - default OFF, matching Argo's own UI. The
  // M6 contract shipped "sync (never prune)"; the tile is a deliberate
  // post-M6 addition, but it must always be an explicit opt-in per sync.
  const [prune, setPrune] = useState(false);
  const [force, setForce] = useState(false);
  const [replace, setReplace] = useState(false);
  const [serverSideApply, setServerSideApply] = useState(false);
  const [selectedResources, setSelectedResources] = useState<string[]>([]);
  const source = app.chart ? `chart ${app.chart}` : app.path || "root";
  const selected = new Set(selectedResources);
  const syncResources = resources.filter((r) => selected.has(resourceKey(r)));

  return (
    <OperationDialog
      title={`Sync Application "${app.name}"`}
      ariaLabel={`Sync Application ${app.name}`}
      subtitle="Starts an Argo CD sync operation with explicit Argo options and fast Klyx defaults."
      cluster={cluster}
      protected={isProtected}
      confirmLabel="Synchronize"
      onCancel={onCancel}
      onConfirm={() => onConfirm({
        revision: revision.trim(),
        strategy,
        prune,
        dryRun,
        force,
        replace,
        serverSideApply,
        resources: syncResources,
      })}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="application" value={`${app.namespace}/${app.name}`} />
        <MetaTile label="destination" value={app.destNamespace || "-"} />
        <MetaTile label="source" value={`${app.repoURL || "-"} - ${source}`} />
        <MetaTile label="current revision" value={app.revision ? short(app.revision) : "-"} title={app.revision} />
      </div>

      <label style={{ display: "grid", gap: 6 }}>
        <span style={fieldLabel}>revision (git ref)</span>
        <input
          autoFocus={!isProtected}
          aria-label="revision git ref"
          value={revision}
          onChange={(e) => setRevision(e.target.value)}
          placeholder="default: spec.source.targetRevision"
          style={inputStyle}
        />
      </label>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>strategy</div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
          <OptionTile
            active={strategy === "hook"}
            title="Hook (recommended)"
            body="Respect sync waves and pre/post-sync hooks. This matches Argo CD's normal operation."
            onClick={() => setStrategy("hook")}
          />
          <OptionTile
            active={strategy === "apply"}
            title="Apply"
            body="Apply resources directly. Skips hooks and is useful when hooks are the problem."
            onClick={() => setStrategy("apply")}
          />
        </div>
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>options</div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 10 }}>
          <OptionTile active={dryRun} title="Dry-run" body="Ask the apiserver what would happen without applying changes." onClick={() => setDryRun((v) => !v)} />
          <OptionTile active={prune} title="Prune" body="Delete resources that are in cluster but no longer in Git." onClick={() => setPrune((v) => !v)} />
          <OptionTile active={force} title="Force" body="Pass Force=true to Argo syncOptions for conflict override." onClick={() => setForce((v) => !v)} />
          <OptionTile active={replace} title="Replace" body="Use replace semantics via Replace=true syncOption." onClick={() => setReplace((v) => !v)} />
          <OptionTile active={serverSideApply} title="Server-side apply" body="Use ServerSideApply=true for field ownership-aware apply." onClick={() => setServerSideApply((v) => !v)} />
        </div>
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>selective sync {selectedResources.length > 0 ? `(${selectedResources.length})` : "(all)"}</div>
        {resources.length === 0 ? (
          <div style={{ color: "var(--color-text-tertiary)", fontSize: 12, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, padding: "9px 10px", background: "var(--color-background-secondary)" }}>
            Argo has not reported resource inventory for this application yet. Synchronize applies all resources.
          </div>
        ) : (
          <div style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, overflow: "hidden", maxHeight: 168, overflowY: "auto", background: "var(--color-background-secondary)" }}>
            {resources.map((r) => {
              const key = resourceKey(r);
              const checked = selected.has(key);
              return (
                <label key={key} style={{ display: "grid", gridTemplateColumns: "18px 110px minmax(0, 1fr) 82px", gap: 8, alignItems: "center", padding: "7px 10px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontFamily: "var(--font-mono)", fontSize: 11, cursor: "pointer" }}>
                  <input
                    aria-label={`select ${r.kind} ${r.namespace}/${r.name}`}
                    type="checkbox"
                    checked={checked}
                    onChange={() => setSelectedResources((prev) => checked ? prev.filter((v) => v !== key) : [...prev, key])}
                  />
                  <span>{r.kind}</span>
                  <span style={ellipsis}>{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>
                  <span style={{ color: resourceTone(r), textAlign: "right" }}>{r.status || r.health || "-"}</span>
                </label>
              );
            })}
          </div>
        )}
      </div>
    </OperationDialog>
  );
}

function resourceKey(r: ArgoResourceDTO): string {
  return `${r.group}/${r.kind}/${r.namespace}/${r.name}`;
}

function resourceTone(r: ArgoResourceDTO): string {
  if (r.status === "OutOfSync" || r.health === "Degraded" || r.health === "Missing") return "var(--color-text-warning)";
  if (r.status === "Synced" || r.health === "Healthy") return "var(--color-text-success)";
  return "var(--color-text-tertiary)";
}

function headerGrid(cols: string): React.CSSProperties {
  return {
    display: "grid",
    gridTemplateColumns: cols,
    gap: 10,
    padding: "0 8px 6px",
    fontSize: 9,
    textTransform: "uppercase",
    letterSpacing: 0.5,
    color: "var(--color-text-tertiary)",
    borderBottom: "0.5px solid var(--color-border-secondary)",
  };
}

const splitPane: React.CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 380px), 1fr))", gap: 14, alignItems: "start" };
const splitPaneWide: React.CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 420px), 1fr))", gap: 14, alignItems: "start" };
const inspector: React.CSSProperties = { border: "0.5px solid var(--color-border-secondary)", borderRadius: 6, background: "var(--color-background-primary)", padding: 14, minWidth: 0, display: "grid", gap: 13, position: "sticky", top: 12 };
const mutedLine: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 11 };
const monoLine: React.CSSProperties = { fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)" };
const smallBtn: React.CSSProperties = { ...btn, padding: "2px 7px", fontSize: 10 };
const smallPrimaryBtn: React.CSSProperties = { ...smallBtn, color: "var(--color-text-info)", borderColor: "var(--color-border-info)", background: "var(--color-background-info)" };
const emptyStyle: React.CSSProperties = { color: "var(--color-text-secondary)", fontSize: 13 };
const fieldLabel: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 10, textTransform: "uppercase", fontWeight: 600, letterSpacing: 0.2 };
const inputStyle: React.CSSProperties = { width: "100%", boxSizing: "border-box", padding: "8px 10px", fontFamily: "var(--font-mono)", fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, color: "var(--color-text-primary)", outline: "none" };
