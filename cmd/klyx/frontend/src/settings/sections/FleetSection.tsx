import { useEffect, useRef, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { addClusters, refreshNewContextCount, type FleetConfigDTO } from "../../bridge/configsvc";
import {
  applyClusterPlan, getClusterEntry, planClusterEdit, planClusterFromFile, planClusterRemove, planClusterRetry, planClusterRevert,
  type ClusterDraft, type ClusterEntry,
} from "../../bridge/fleetPlans";
import { lostAnswer, type ApplyResult, type Plan } from "../../bridge/plan";
import { btn, ellipsis } from "../../chrome/primitives";
import { useFleet } from "../../store/fleet";
import { useLine } from "../../store/line";
import { requestLeave, setSectionDraft } from "../navGuard";
import { PlanView } from "../PlanView";
import { SettingField } from "../SettingField";
import { anchorOf, settingDomId } from "../registry";
import { ClusterEditor } from "./ClusterEditor";
import { PathRow, Group, type SectionProps } from "./common";

// Settings > Fleet (S1 §1, S2 §5): the clusters Klyx runs (slice 0's drift
// included), the kubeconfig contexts, and the line's comparison source, as
// it runs. A cluster's S2 fields are edited in place: Review and apply
// shows the plan - what changes, what it ends and continues, the interlock
// - and writes only on Apply. Each cell carries its anchor, and the
// fields' contracts follow the table.

// a plan on the page, with what it is for; fromEditor: the editor's draft
// made it, so a change to the draft withdraws it
type OpenPlan = { plan: Plan; title: string; cluster: string; records?: boolean; fromEditor?: boolean };

const FIELDS: { id: string; head: string }[] = [
  { id: "clusters[].name", head: "name" },
  { id: "clusters[].context", head: "context" },
  { id: "clusters[].environment", head: "env" },
  { id: "clusters[].group", head: "group" },
  { id: "clusters[].protected", head: "protected" },
];
const DESCRIBED = ["clusters[].name", "clusters[].context", "clusters[].kubeconfig", "clusters[].tags", "clusters[].group", "clusters[].environment", "clusters[].protected"];
const IN_TABLE = new Set(FIELDS.map((f) => f.id));
const GRID = "minmax(0,1fr) minmax(0,1.3fr) 80px 80px 90px 70px 120px";
const notice: React.CSSProperties = { gridColumn: "1 / -1", fontFamily: "var(--font-sans)", color: "var(--color-text-warning)", fontSize: 12, display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" };
const small: React.CSSProperties = { padding: "1px 8px", fontSize: 11 };

// heldText is a held cluster's notice. Its reason is a whole clause - a
// reconnect's "saved, not connected yet: ..." or a build's "failed to
// connect: ..." - shown as it is, never under a second lead.
export function heldText(name: string, held: string): string {
  return `▲ ${held.charAt(0).toUpperCase()}${held.slice(1)}. Nothing new starts on ${name} until it connects.`;
}

// okText is what an applied fleet plan did, in words.
function okText(p: Plan, cluster: string): string {
  switch (p.class) {
    case "reconnect":
      return `Saved: ${cluster} is not connected yet - connecting with the new terms. Nothing new starts on it until it connects.`;
    case "removal":
      return `Removed ${cluster}.`;
    case "addition":
      return `Connecting ${cluster}.`;
  }
  return p.writes ? `Applied: ${cluster}'s entry in fleet.yaml says so, and Klyx uses it.` : `Applied: Klyx uses fleet.yaml's entry for ${cluster}.`;
}

export function sentenceForFleet(cfg: FleetConfigDTO): string {
  const n = cfg.clusters.length;
  const p = cfg.clusters.filter((c) => c.protected).length;
  const running = `${n} cluster${n === 1 ? "" : "s"} running, ${p} protected`;
  if (cfg.loadError) return `${running}; fleet.yaml did not load when Klyx started`;
  if (cfg.fileError) return `${running}; fleet.yaml does not load as it is now`;
  const drift = cfg.clusters.filter((c) => c.drift).length;
  const only = cfg.fileOnly?.length ?? 0;
  const differs = [drift > 0 ? `${drift} differ${drift === 1 ? "s" : ""} from fleet.yaml` : null, only > 0 ? `${only} in fleet.yaml not running` : null].filter(Boolean);
  return differs.length ? `${running}; ${differs.join(", ")}` : `${running}; fleet.yaml agrees`;
}

export function FleetSection({ cfg, reload, ready }: SectionProps & { cfg: FleetConfigDTO; reload: () => Promise<boolean> }) {
  const [selected, setSelected] = useState<string[]>([]);
  const [banner, setBanner] = useState<{ kind: "success" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  busyRef.current = busy;
  const [editing, setEditing] = useState<ClusterEntry | null>(null);
  const [dirty, setDirty] = useState(false);
  const [open, setOpen] = useState<OpenPlan | null>(null);
  // The editor's draft generation: a plan made from an older draft is
  // never shown or applied - any change to the draft withdraws its plan
  // (Codex's review of S2c, R1).
  const draftGen = useRef(0);
  // an Apply being settled - its answer, then its readback - which the
  // leave guard waits for (R2, R3)
  const settling = useRef<Promise<void> | null>(null);
  const dropDraft = () => {
    draftGen.current++;
    setEditing(null);
    setDirty(false);
    setOpen((o) => (o?.fromEditor ? null : o));
  };
  // plan runs one request at a time and shows its plan; a refused plan
  // shows why, and nothing applies it
  const plan = async (cluster: string, title: string, ask: () => Promise<Plan>, opts: { records?: boolean; fromEditor?: boolean } = {}) => {
    const gen = draftGen.current;
    setBusy(true);
    setBanner(null);
    try {
      const p = await ask();
      if (opts.fromEditor && gen !== draftGen.current) return; // the draft moved on
      setOpen({ plan: p, title, cluster, records: opts.records, fromEditor: opts.fromEditor });
    } finally {
      setBusy(false);
    }
  };
  const [editorKey, setEditorKey] = useState(0);
  const openEditor = async (name: string) => {
    setBusy(true);
    setOpen(null);
    try {
      draftGen.current++;
      setDirty(false);
      const e = await getClusterEntry(name);
      setEditorKey((k) => k + 1);
      setEditing(e);
    } finally {
      setBusy(false);
    }
  };
  // another editor, or this one opened again, replaces the draft: a draft
  // with changes asks first, as leaving the section does (R2)
  const edit = (name: string) => (dirty ? requestLeave(() => void openEditor(name)) : void openEditor(name));
  // Leaving the section, or replacing the editor, with a changed draft asks
  // to discard it or stay. There is no Save and leave: a cluster's change
  // is applied only through its plan (R2).
  useEffect(() => {
    setSectionDraft("fleet-editor", editing && dirty
      ? { settings: false, credentials: [], unsaved: [`a change to ${editing.name} not yet applied`], discard: dropDraft, settling: () => settling.current }
      : { settings: false, credentials: [], discard: () => {}, settling: () => settling.current });
  });
  useEffect(() => () => setSectionDraft("fleet-editor", null), []);
  // settle says an Apply's result, then reads back what runs before saying
  // anything is current; a failed readback is said, and the outcome stays
  // (R3). A replacement plan is shown for review, never applied by this; a
  // lost answer is not a refusal (F2b review, R3).
  const settle = async (r: ApplyResult, o: OpenPlan) => {
    if (r.unknown) {
      setOpen(null);
      setBanner({ kind: "error", text: `${lostAnswer(r)}. Reading what runs now…` });
      const read = await reload();
      setBanner({ kind: "error", text: read ? `${lostAnswer(r)}: what is shown is what runs now.` : `${lostAnswer(r)}, and reading the fleet again failed: what is shown may not be what runs.` });
    } else if (r.plan) {
      setOpen({ ...o, plan: r.plan });
      setBanner(r.error ? { kind: "error", text: `Not applied: ${r.error}` } : null);
    } else if (!r.ok) {
      setBanner({ kind: "error", text: `Not applied: ${r.error ?? "unknown error"}` });
    } else {
      dropDraft();
      setOpen(null);
      const text = r.note ? `${okText(o.plan, o.cluster)} ${r.note}` : okText(o.plan, o.cluster);
      setBanner({ kind: r.note ? "warn" : "success", text });
      if (!(await reload())) setBanner({ kind: "warn", text: `${text} Reading the fleet again failed: what is shown may be out of date.` });
    }
  };
  // A held cluster's notice follows its connection: while one is held, a
  // change in the live fleet, and every ten seconds, read the fleet again.
  // Drafts and open plans are kept (R4).
  const live = useFleet((s) => s.clusters);
  const lastLive = useRef(live);
  const anyHeld = cfg.clusters.some((c) => c.held);
  useEffect(() => {
    if (lastLive.current === live) return;
    lastLive.current = live;
    if (anyHeld && !busyRef.current) void reload();
  }, [live, anyHeld, reload]);
  useEffect(() => {
    if (!anyHeld) return;
    const t = setInterval(() => {
      if (!busyRef.current) void reload();
    }, 10_000);
    return () => clearInterval(t);
  }, [anyHeld, reload]);
  const line = useLine((s) => s.dto);
  useEffect(() => { ready(); }, [ready]);
  const newContexts = cfg.contexts.filter((c) => !c.inFleet);
  const toggle = (name: string) => setSelected((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));
  const onAdd = async () => {
    if (selected.length === 0 || busy) return;
    setBusy(true);
    const r = await addClusters(selected);
    setBusy(false);
    if (r.ok) {
      setBanner({ kind: "success", text: `Added ${selected.length} cluster${selected.length === 1 ? "" : "s"} to fleet.yaml - connecting now.` });
      setSelected([]);
      void reload();
      void refreshNewContextCount();
    } else {
      setBanner({ kind: "error", text: r.error || "Could not update fleet.yaml" });
    }
  };
  const overridden = line?.keyHow === "override" ? line.key : undefined;
  const planPanel = open && (
    <PlanView
      key={open.plan.digest || open.plan.refused}
      plan={open.plan}
      busy={busy}
      title={open.title}
      interlockTitle={`${open.title}?`}
      applyLabel={open.plan.class === "removal" ? "Remove" : "Apply"}
      onRecords={open.records ? (chosen) => void plan(open.cluster, open.title, () => planClusterRemove(open.cluster, chosen), { records: true }) : undefined}
      onApply={async (typed) => {
        setBusy(true);
        let done = () => {};
        settling.current = new Promise<void>((resolve) => { done = resolve; });
        try {
          const r = await applyClusterPlan(open.plan.digest, typed);
          await settle(r, open);
          return r;
        } finally {
          settling.current = null;
          done();
          setBusy(false);
        }
      }}
      onCancel={() => setOpen(null)}
    />
  );
  return (
    <>
      {banner && (
        <div data-testid="settings-banner" role="status" className="t-body" style={{ color: banner.kind === "error" ? "var(--color-text-danger)" : banner.kind === "warn" ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>
          {banner.kind !== "success" ? "▲ " : ""}{banner.text}
        </div>
      )}
      <Group title="Clusters">
        <PathRow label="file" path={cfg.path} />
        <p className="settings-group-note">
          The clusters below are the ones Klyx is running. Edit a cluster to change its context, kubeconfig, placement or protection: Review and apply shows what changes and what it ends, and Apply writes only the changed fields into its entry in fleet.yaml, every other line as you wrote it. A hand edit of an entry shows on its row, and its own action applies it. Klyx writes the file in a few other places only: it appends the clusters you add below; Settings &gt; AI, Git and Notifications each rewrite their own block (comments inside that block are not kept); and Observability replaces a cluster&apos;s plaintext metrics token line with auth when you move it to the keychain. A cluster&apos;s endpoints are edited in the file and applied by Test under Observability.
        </p>
        {cfg.fileOnly && cfg.fileOnly.length > 0 && (
          <div data-testid="settings-file-only" role="status" style={{ fontSize: 12, color: "var(--color-text-warning)", marginBottom: 8, display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
            In fleet.yaml but not running:
            {cfg.fileOnly.map((n) => (
              <span key={n} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
                <span style={{ fontFamily: "var(--font-mono)" }}>{n}</span>
                <button className="l-btn" style={small} disabled={busy} onClick={() => void plan(n, `Connect ${n}`, () => planClusterFromFile(n))}>Connect</button>
              </span>
            ))}
          </div>
        )}
        {open && !cfg.clusters.some((c) => c.name === open.cluster) && planPanel}
        {cfg.warnings.length > 0 && (
          <div style={{ marginBottom: 8 }}>
            {cfg.warnings.map((w, i) => (
              <div key={i} style={{ fontSize: 12, color: "var(--color-text-warning)", padding: "2px 0", display: "flex", alignItems: "baseline", gap: 4 }}><IconAlertTriangle size={11} stroke={1.7} style={{ flexShrink: 0, alignSelf: "center" }} /> {w}</div>
            ))}
          </div>
        )}
        <div role="table" aria-label="Clusters running">
          <div role="row" style={{ display: "grid", gridTemplateColumns: GRID, gap: 8, fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", padding: "0 0 4px", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
            {FIELDS.map((f) => <span role="columnheader" key={f.id}>{f.head}</span>)}
            <span role="columnheader">metrics</span>
            <span role="columnheader"><span className="sr-only">actions</span></span>
          </div>
          {cfg.clusters.map((c) => {
            // a cell's id is its own: the editor's field for the same setting
            // and cluster has the -row id (Codex's review of S2c, R7)
            const cell = (id: string, text: string, extra?: React.CSSProperties, title?: string) => (
              <span role="cell" data-setting={id} data-instance={c.name} data-anchor={anchorOf(id, c.name)} id={`${settingDomId(id, c.name)}-cell`} tabIndex={-1}
                style={{ ...ellipsis, ...extra }} title={title ?? text}>{text}</span>
            );
            const pending = c.pendingProtected === undefined || c.pendingProtected === null ? ""
              : ` Once its new connection is usable: ${c.pendingProtected ? `protected, decided by ${c.pendingProtectedBy}` : "not protected"}.`;
            return (
              <div role="row" key={c.name} style={{ display: "grid", gridTemplateColumns: GRID, gap: 8, fontFamily: "var(--font-mono)", fontSize: 12, padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)", alignItems: "center" }}>
                {cell("clusters[].name", c.name)}
                {cell("clusters[].context", c.context, { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].environment", c.env || "—", { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].group", c.group || "—", { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].protected", c.protected ? (c.protectedInferred ? "inferred" : "yes") : "no", { color: c.protected ? "var(--color-text-warning)" : "var(--color-text-tertiary)" },
                  c.protectedInferred ? "inferred from a prd/prod/production token in its env, name or context - set protected: true to make it explicit"
                    : pending ? `${c.protected ? "Protected now." : "Not protected now."}${pending}` : undefined)}
                <span role="cell" style={{ color: "var(--color-text-tertiary)" }}>{c.hasMetrics ? "yes" : "—"}</span>
                <span role="cell" style={{ display: "flex", gap: 4, justifyContent: "flex-end", fontFamily: "var(--font-sans)" }}>
                  <button className="l-btn" style={small} disabled={busy} aria-label={`edit ${c.name}`} onClick={() => edit(c.name)}>Edit</button>
                  <button className="l-btn" style={small} disabled={busy} aria-label={`remove ${c.name}`}
                    onClick={() => void plan(c.name, `Remove ${c.name}`, () => planClusterRemove(c.name, []), { records: true })}>Remove…</button>
                </span>
                {c.held && (
                  <div data-testid="settings-held" role="status" style={notice}>
                    <span>{heldText(c.name, c.held)}</span>
                    {c.canRevert && <button className="l-btn" style={small} disabled={busy} onClick={() => void plan(c.name, `Revert ${c.name}`, () => planClusterRevert(c.name))}>Revert</button>}
                    {c.canRetry && <button className="l-btn" style={small} disabled={busy} onClick={() => void plan(c.name, `Retry ${c.name}`, () => planClusterRetry(c.name))}>Retry</button>}
                  </div>
                )}
                {c.drift && (
                  <div data-testid="settings-drift" style={notice}>
                    <span>{c.drift}</span>
                    <button className="l-btn" style={small} disabled={busy} onClick={() => void plan(c.name, `Use the file's values for ${c.name}`, () => planClusterFromFile(c.name))}>Use the file&apos;s values</button>
                  </div>
                )}
                {editing?.name === c.name && (
                  // keyed by its opening: an editor opened again starts from the entry read then
                  <ClusterEditor key={`${c.name}:${editorKey}`} entry={editing} busy={busy} onCancel={dropDraft}
                    onDraftChange={(changed) => {
                      draftGen.current++;
                      setDirty(changed);
                      setOpen((o) => (o?.fromEditor ? null : o));
                    }}
                    onReview={(d: ClusterDraft) => void plan(c.name, `Review the changes to ${c.name}`, () => planClusterEdit(c.name, d), { fromEditor: true })} />
                )}
                {open?.cluster === c.name && <div style={{ gridColumn: "1 / -1", fontFamily: "var(--font-sans)" }}>{planPanel}</div>}
              </div>
            );
          })}
        </div>
      </Group>

      <Group title="Each cluster's fields">
        <p className="settings-group-note">What each field of a cluster entry does. Edit a cluster in the table above to change them; its name is read-only here.</p>
        {DESCRIBED.map((id) => <SettingField key={id} id={id} source="file" value={id === "clusters[].name" ? "per cluster, in the table above; renaming is a later slice" : IN_TABLE.has(id) ? "per cluster: Edit in the table above" : "per cluster: Edit in the table above shows it"} />)}
      </Group>

      <Group title="The line's comparison source">
        <SettingField id="line.source.url" source={overridden ? "file" : "default"}
          value={overridden ? overridden.repo : line?.key ? `not set: comparing ${line.key.label}, ${line.keyHow === "selector" ? "chosen in this session" : "chosen by most stations"}` : "not set"} />
        <SettingField id="line.source.ref.kind" source={overridden ? "file" : "default"} value={overridden?.refKind || "not set"} />
        <SettingField id="line.source.ref.name" source={overridden ? "file" : "default"} value={overridden?.refName || "not set"} />
      </Group>

      <Group title="Kubeconfig contexts">
        <PathRow label="kubeconfig" path={cfg.kubeconfigPath} />
        {cfg.scanError ? (
          <div style={{ fontSize: 12, color: "var(--color-text-danger)" }}>Could not read the kubeconfig: {cfg.scanError}</div>
        ) : cfg.contexts.length === 0 ? (
          <div style={{ fontSize: 12, color: "var(--color-text-tertiary)" }}>No contexts found.</div>
        ) : (
          <>
            <p className="settings-group-note">Re-scanned every time this page opens - a context added to the kubeconfig shows up here without restarting.</p>
            {cfg.contexts.map((c) => (
              <div key={c.name} style={{ display: "flex", alignItems: "center", gap: 8, fontFamily: "var(--font-mono)", fontSize: 12, padding: "4px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                {c.inFleet ? (
                  <span style={{ width: 14 }} />
                ) : (
                  <input type="checkbox" data-setting-exempt="action" aria-label={`select context ${c.name}`} checked={selected.includes(c.name)} onChange={() => toggle(c.name)} style={{ margin: 0 }} />
                )}
                <span style={{ ...ellipsis, flex: 1 }} title={c.name}>{c.name}</span>
                {c.inFleet ? <span className="t-secondary">in fleet</span> : <span style={{ fontSize: 11, color: "var(--color-text-info)" }}>not in fleet</span>}
              </div>
            ))}
            {newContexts.length > 0 && cfg.loadError && (
              <div style={{ marginTop: 10, fontSize: 12, color: "var(--color-text-tertiary)" }}>Adding clusters waits until fleet.yaml loads.</div>
            )}
            {newContexts.length > 0 && !cfg.loadError && (
              <div style={{ marginTop: 10, display: "flex", alignItems: "center", gap: 8 }}>
                <button onClick={() => void onAdd()} disabled={selected.length === 0 || busy}
                  style={{ ...btn, opacity: selected.length === 0 || busy ? 0.5 : 1, cursor: selected.length === 0 || busy ? "not-allowed" : "pointer" }}>
                  {busy ? "adding…" : `add ${selected.length || ""} to fleet`.replace("  ", " ")}
                </button>
                <span style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>appends to fleet.yaml · connects immediately</span>
              </div>
            )}
          </>
        )}
      </Group>
    </>
  );
}
