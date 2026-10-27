import { useEffect, useRef, useState } from "react";
import { lostAnswer, type ApplyResult, type Plan } from "../../bridge/plan";
import {
  applySources, detect, getSources, migrationOffers, planSources, planSourcesFromFile, removeSourceCredential, setSourceCredential, testSource,
  type DetectResult, type Installation, type MigrationOffer, type SourceDraft, type SourcesDraft, type SourcesState, type Subscription,
} from "../../bridge/sources";
import { ellipsis } from "../../chrome/primitives";
import { setSectionDraft } from "../navGuard";
import { PlanView } from "../PlanView";
import { SettingField } from "../SettingField";
import { anchorOf } from "../registry";
import { InstallationEditor, SubscriptionEditor } from "./SourceEditor";

// Observability's alert sources (Settings S3a §8): the installations in
// force and each running cluster's subscription, with their plans, an
// installation's credential, Detect and the migration offer. It keeps S2c's
// discipline: a change to a draft withdraws its plan, a changed draft is a
// draft to the leave guard, nothing is called current before the readback,
// and drafts are kept across live refreshes.

const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const small: React.CSSProperties = { padding: "1px 8px", fontSize: 11 };
const rowStyle: React.CSSProperties = { padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" };
const line: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" };

type Editor =
  | { kind: "installation"; op: "add" | "edit"; draft: SourceDraft; subscriptions?: SourcesDraft["subscriptions"]; title: string }
  | { kind: "subscription"; cluster: string; block: Subscription | null };

type OpenPlan = { plan: Plan; title: string; draft?: SourcesDraft; fromEditor?: boolean };

// A typed credential belongs to the installation as shown: keyed by its
// revision, and dropped when that revision is gone.
const credKey = (i: Pick<Installation, "id" | "revision">) => `${i.id}@${i.revision}`;

const okText = (p: Plan) => (p.writes ? "Applied: fleet.yaml says so, and Klyx reads the alerts under the new terms." : "Applied: Klyx reads the alerts as fleet.yaml says them.");

export function SourcesPanel({ onLoaded, refreshMs = 30_000 }: { onLoaded?: (s: SourcesState) => void; refreshMs?: number }) {
  const [state, setState] = useState<SourcesState | null>(null);
  const [offers, setOffers] = useState<MigrationOffer[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  busyRef.current = busy;
  const [banner, setBanner] = useState<{ kind: "success" | "error" | "warn"; text: string } | null>(null);
  const [editor, setEditor] = useState<Editor | null>(null);
  const [editorKey, setEditorKey] = useState(0);
  const [dirty, setDirty] = useState(false);
  const [open, setOpen] = useState<OpenPlan | null>(null);
  const [inputs, setInputs] = useState<Record<string, string>>({});
  const [detectScope, setDetectScope] = useState("");
  const [detected, setDetected] = useState<DetectResult | null>(null);
  const draftGen = useRef(0);
  const settling = useRef<Promise<void> | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const planBox = useRef<HTMLDivElement>(null);
  // what opened the editor, which has the focus back when it closes
  const opener = useRef<HTMLElement | null>(null);

  // load reads the sources again; a failed read keeps what was shown and
  // says so - never an empty list
  const load = async (): Promise<boolean> => {
    const [st, of] = await Promise.all([getSources(), migrationOffers()]);
    if (!st) {
      setLoadFailed(true);
      return false;
    }
    setLoadFailed(false);
    setState(st);
    if (of) setOffers(of);
    onLoaded?.(st);
    const live = new Set(st.installations.map(credKey));
    setInputs((cur) => Object.fromEntries(Object.entries(cur).filter(([k]) => live.has(k))));
    return true;
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // live refreshes of scope and credential state keep the drafts
  useEffect(() => {
    if (refreshMs <= 0) return;
    const t = setInterval(() => {
      if (!busyRef.current) void load();
    }, refreshMs);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshMs]);

  const dropDraft = () => {
    draftGen.current++;
    setEditor(null);
    setDirty(false);
    setOpen((o) => (o?.fromEditor ? null : o));
  };
  const openEditor = (e: Editor) => {
    if (!editor) opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    draftGen.current++;
    setDirty(false);
    setOpen(null);
    setEditorKey((k) => k + 1);
    setEditor(e);
  };
  useEffect(() => {
    const typed = (state?.installations ?? []).filter((i) => (inputs[credKey(i)] ?? "").trim() !== "");
    setSectionDraft("observability-sources", {
      settings: false,
      credentials: typed.map((i) => `an unsaved ${i.id} ${i.authType === "basic" ? "password" : "token"}`),
      unsaved: editor && dirty ? ["a change to the alert sources not yet applied"] : [],
      discard: () => {
        setInputs({});
        dropDraft();
      },
      settling: () => settling.current,
    });
  });
  useEffect(() => () => setSectionDraft("observability-sources", null), []);

  // keyboard: an editor opens at its first control; closed, the focus goes
  // back to what opened it once nothing is busy; a new plan takes the focus
  useEffect(() => {
    if (!editor) return;
    const box = root.current?.querySelector('[data-testid="installation-editor"], [data-testid="subscription-editor"]');
    Array.from(box?.querySelectorAll<HTMLInputElement>("input, select, textarea") ?? []).find((el) => !el.disabled)?.focus();
    // a new editor only, never a re-render of the same one
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editorKey]);
  useEffect(() => {
    if (editor || busy) return;
    if (opener.current?.isConnected) opener.current.focus();
    opener.current = null;
  }, [editor, busy]);
  const planId = open ? open.plan.digest || open.plan.refused || "" : "";
  useEffect(() => {
    if (!planId) return;
    planBox.current?.scrollIntoView?.({ block: "nearest" });
    planBox.current?.focus();
  }, [planId]);

  // plan asks for a plan and shows it; a plan from a draft that has moved
  // on since is never shown
  const plan = async (title: string, ask: () => Promise<Plan>, opts: { draft?: SourcesDraft; fromEditor?: boolean } = {}) => {
    const gen = draftGen.current;
    setBusy(true);
    setBanner(null);
    try {
      const p = await ask();
      if (opts.fromEditor && gen !== draftGen.current) return;
      setOpen({ plan: p, title, draft: opts.draft, fromEditor: opts.fromEditor });
    } finally {
      setBusy(false);
    }
  };
  const review = (title: string, d: SourcesDraft) => void plan(title, () => planSources(d), { draft: d, fromEditor: true });

  // settle says an Apply's result, then reads back what is in force before
  // anything is called current; a lost answer is not a refusal
  const settle = async (r: ApplyResult, o: OpenPlan) => {
    if (r.unknown) {
      setOpen(null);
      setBanner({ kind: "error", text: `${lostAnswer(r)}. Reading the alert sources again…` });
      const read = await load();
      setBanner({ kind: "error", text: read ? `${lostAnswer(r)}: what is shown is what is in force now.` : `${lostAnswer(r)}, and reading the alert sources again failed: what is shown may not be what is in force.` });
    } else if (r.plan) {
      setOpen({ ...o, plan: r.plan });
      setBanner(r.error ? { kind: "error", text: `Not applied: ${r.error}` } : null);
    } else if (!r.ok) {
      setBanner({ kind: "error", text: `Not applied: ${r.error ?? "unknown error"}` });
    } else {
      if (o.fromEditor) dropDraft();
      setOpen(null);
      setBanner({ kind: "success", text: okText(o.plan) });
      if (!(await load())) setBanner({ kind: "warn", text: `${okText(o.plan)} Reading the alert sources again failed: what is shown may be out of date.` });
    }
  };

  const run = async (fn: () => Promise<{ ok: boolean; message: string }>) => {
    setBusy(true);
    const r = await fn();
    setBusy(false);
    setBanner({ kind: r.ok ? "success" : "warn", text: r.message });
    await load();
  };

  if (!state) {
    return loadFailed
      ? <div data-testid="sources-load-failed" style={{ ...hint, color: "var(--color-text-danger)" }}>Could not read the alert sources. <button className="l-btn" onClick={() => void load()}>Retry</button></div>
      : <div style={hint}>Reading the alert sources…</div>;
  }
  const installationIds = state.installations.map((i) => i.id);
  const clusters = state.subscriptions.map((s) => s.cluster);
  const planPanel = open && (
    <div ref={planBox} tabIndex={-1} style={{ outline: "none" }}>
      <PlanView
        key={open.plan.digest || open.plan.refused}
        plan={open.plan}
        busy={busy}
        title={open.title}
        interlockTitle={`${open.title}?`}
        onApply={async () => {
          setBusy(true);
          let done = () => {};
          settling.current = new Promise<void>((resolve) => { done = resolve; });
          try {
            const r = await applySources(open.plan.digest);
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
    </div>
  );
  const editorView = editor && (editor.kind === "installation"
    ? <InstallationEditor key={editorKey} initial={editor.draft} op={editor.op} subscriptions={editor.subscriptions} hosts={clusters} busy={busy}
      onCancel={dropDraft}
      onDraftChange={(changed) => { draftGen.current++; setDirty(changed); setOpen((o) => (o?.fromEditor ? null : o)); }}
      onReview={(d) => review(editor.title, d)} />
    : <SubscriptionEditor key={editorKey} cluster={editor.cluster} initial={editor.block} installations={installationIds} busy={busy}
      onCancel={dropDraft}
      onDraftChange={(changed) => { draftGen.current++; setDirty(changed); setOpen((o) => (o?.fromEditor ? null : o)); }}
      onReview={(d) => review(`Review ${editor.cluster}'s alert subscription`, d)} />);

  return (
    <div data-testid="sources-panel" ref={root}>
      {banner && (
        <div data-testid="sources-banner" role="status" className="t-body" style={{ color: banner.kind === "error" ? "var(--color-text-danger)" : banner.kind === "warn" ? "var(--color-text-warning)" : "var(--color-text-primary)", overflowWrap: "anywhere" }}>
          {banner.kind !== "success" ? "▲ " : ""}{banner.text}
        </div>
      )}
      {loadFailed && (
        <div role="status" data-testid="sources-reread-failed" style={{ fontSize: 11, color: "var(--color-text-warning)", marginBottom: 8 }}>
          Klyx could not read the alert sources again: what is shown is as last read and may be out of date. <button className="l-btn" disabled={busy} onClick={() => void load()}>Retry</button>
        </div>
      )}
      <p data-testid="sources-sentence" className="settings-group-note" style={{ fontWeight: 600 }}>{state.sentence}</p>
      {state.fileError && <div role="status" data-testid="sources-file-error" style={{ fontSize: 11, color: "var(--color-text-warning)" }}>{state.fileError}</div>}
      {state.fileDiffers && (
        <div data-testid="sources-file-differs" style={{ ...line, fontSize: 12, color: "var(--color-text-warning)", margin: "4px 0 8px" }}>
          fleet.yaml&apos;s installations or subscription blocks are not the ones in force.
          <button className="l-btn" style={small} disabled={busy} onClick={() => void plan("Use the file's alert sources", () => planSourcesFromFile())}>Use the file&apos;s values</button>
        </div>
      )}

      <h4 className="settings-subhead">Installations</h4>
      {state.installations.length === 0 && <div style={hint}>No installation is in force: each cluster reads its own Alertmanager.</div>}
      {state.installations.map((i) => {
        const ck = credKey(i);
        const value = inputs[ck] ?? "";
        const canStore = !!i.endpoint && !!i.authType;
        return (
          <div key={i.id} data-testid="installation-row" data-installation={i.id} data-anchor={anchorOf("sources[].id", i.id)} tabIndex={-1} role="group" aria-label={`installation ${i.id}`} style={rowStyle}>
            <div style={line}>
              <span style={{ fontWeight: 600, fontFamily: "var(--font-mono)", ...ellipsis, maxWidth: "100%" }} title={i.id}>{i.id}</span>
              {i.name && <span style={hint}>{i.name}</span>}
              <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere" }}>{i.where}</span>
              {i.tenant && <span style={hint}>tenant {i.tenant}</span>}
              {i.memberLabel && <span style={hint}>member label {i.memberLabel}</span>}
            </div>
            <div style={{ ...line, fontSize: 11 }}>
              <span className="t-secondary" style={{ overflowWrap: "anywhere" }}>
                serves {[...(i.servesGroups ?? []).map((g) => `group ${g}`), ...(i.servesClusters ?? [])].join(", ") || "nothing by name or group"}
                {" · "}subscribers: {i.subscribers.length ? i.subscribers.join(", ") : "none"}
              </span>
            </div>
            <div style={{ ...line, fontSize: 11 }}>
              <span data-testid="installation-credential" style={{ color: i.credential === "stored" || i.credential.startsWith("not needed") ? "var(--color-text-secondary)" : "var(--color-text-warning)" }}>credential: {i.credential}</span>
              {i.readAt && <span className="t-secondary">read {i.readAt}{i.truncated ? ", cut at its cap" : ""}{i.unmatched !== undefined && i.unmatched !== null ? `; ${i.unmatched} matched no member` : ""}</span>}
              {i.readError && <span style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>last read failed: {i.readError}</span>}
            </div>
            {canStore && (
              <SettingField id="credential.source" instance={i.id} label={`${i.id} ${i.authType === "basic" ? "password" : "token"}`}>
                {(ids) => (<>
                  <input id={ids.id} aria-describedby={ids.describedBy} type="password" autoComplete="off" spellCheck={false} className="l-input" style={{ flex: 1, minWidth: 160 }}
                    placeholder={i.credential === "stored" ? "paste a new one to replace it" : i.authType === "basic" ? "paste the password" : "paste the token"}
                    value={value} onChange={(e) => setInputs({ ...inputs, [ck]: e.target.value })} />
                  <button className="l-btn" aria-label={`${i.credential === "stored" ? "Replace" : "Store"} the ${i.id} credential`} disabled={busy || value.trim() === ""}
                    onClick={() => {
                      setInputs({ ...inputs, [ck]: "" });
                      void run(() => setSourceCredential(i.id, i.revision, value));
                    }}>{i.credential === "stored" ? "Replace" : "Store"}</button>
                </>)}
              </SettingField>
            )}
            <div style={{ ...line, marginTop: 4 }}>
              <button className="l-btn" style={small} disabled={busy} aria-label={`edit ${i.id}`}
                onClick={() => openEditor({ kind: "installation", op: "edit", draft: i, title: `Review the changes to ${i.id}` })}>Edit</button>
              <button className="l-btn" style={small} disabled={busy} aria-label={`test ${i.id}`} onClick={() => void run(() => testSource(i.id, i.revision))}>Test</button>
              {canStore && i.credential === "stored" && (
                <button className="l-btn l-btn-danger" style={small} disabled={busy} aria-label={`remove the ${i.id} credential`} onClick={() => void run(() => removeSourceCredential(i.id, i.revision))}>Remove credential</button>
              )}
              <button className="l-btn" style={small} disabled={busy} aria-label={`remove ${i.id}`}
                onClick={() => {
                  // its subscribers by their own block go with it, as one plan
                  const own = state.subscriptions.filter((s) => s.block?.source === i.id).map((s) => ({ cluster: s.cluster, block: null }));
                  const d: SourcesDraft = { installation: { op: "remove", source: { id: i.id } }, subscriptions: own };
                  void plan(`Remove ${i.id}`, () => planSources(d), { draft: d });
                }}>Remove…</button>
            </div>
            {editor?.kind === "installation" && editor.op === "edit" && editor.draft.id === i.id && editorView}
          </div>
        );
      })}
      <div style={{ ...line, margin: "6px 0" }}>
        <button className="l-btn" disabled={busy} onClick={() => openEditor({ kind: "installation", op: "add", draft: { id: "" }, title: "Review the new installation" })}>Add an installation</button>
      </div>
      {editor?.kind === "installation" && editor.op === "add" && editorView}

      <h4 className="settings-subhead">Each cluster&apos;s alerts</h4>
      {state.subscriptions.map((s) => (
        <div key={s.cluster} data-testid="subscription-row" data-cluster={s.cluster} data-anchor={anchorOf("clusters[].alertmanager.source", s.cluster)} tabIndex={-1} role="group" aria-label={`${s.cluster}'s alerts`} style={rowStyle}>
          <div style={line}>
            <span style={{ fontWeight: 600, fontFamily: "var(--font-mono)" }}>{s.cluster}</span>
            <span className="t-secondary" style={{ fontSize: 11 }}>{s.mode}{s.via ? `, ${s.via}` : ""}</span>
          </div>
          <div data-testid="subscription-sentence" style={{ fontSize: 12, overflowWrap: "anywhere" }}>{s.sentence}</div>
          {s.shadowed && s.shadowed.length > 0 && <div style={hint}>its own block wins over {s.shadowed.join(", ")}</div>}
          {s.legacy
            ? <div style={hint}>Its own Alertmanager block is set in fleet.yaml; External endpoints tests it.</div>
            : (
              <div style={{ ...line, marginTop: 4 }}>
                <button className="l-btn" style={small} disabled={busy} aria-label={`edit ${s.cluster}'s alert subscription`}
                  onClick={() => openEditor({ kind: "subscription", cluster: s.cluster, block: s.block ?? null })}>{s.block ? "Edit its block" : "Give it a block"}</button>
              </div>
            )}
          {editor?.kind === "subscription" && editor.cluster === s.cluster && editorView}
        </div>
      ))}

      <h4 className="settings-subhead">Detect</h4>
      <p style={hint}>Detect looks for an Alertmanager in a group&apos;s clusters, or one cluster, and reads one small sample of its alerts to propose an installation. It proposes; it never proves one shared.</p>
      <div style={line}>
        <select aria-label="what Detect looks in" className="l-input" value={detectScope} disabled={busy} onChange={(e) => setDetectScope(e.target.value)}>
          <option value="">choose a group or a cluster</option>
          {clusters.map((c) => <option key={`c:${c}`} value={`c:${c}`}>cluster {c}</option>)}
        </select>
        <input aria-label="or a group" className="l-input" placeholder="or a group" disabled={busy}
          onChange={(e) => setDetectScope(e.target.value.trim() ? `g:${e.target.value.trim()}` : "")} />
        <button className="l-btn" disabled={busy || !detectScope} onClick={async () => {
          setBusy(true);
          const scope = detectScope.startsWith("g:") ? { group: detectScope.slice(2) } : { cluster: detectScope.slice(2) };
          setDetected(await detect(scope));
          setBusy(false);
        }}>Detect</button>
      </div>
      {detected && (
        <div data-testid="detect-result" style={{ marginTop: 6 }}>
          {detected.error && <div style={{ fontSize: 12, color: "var(--color-text-warning)" }}>{detected.error}</div>}
          {detected.clusters.map((c) => (
            <div key={c.cluster} style={{ fontSize: 12 }}><span style={{ fontFamily: "var(--font-mono)" }}>{c.cluster}</span>: {c.outcome}{c.detail ? `, ${c.detail}` : ""}</div>
          ))}
          {detected.proposals.map((p, n) => (
            <div key={`${p.host}-${n}`} data-testid="detect-proposal" style={rowStyle}>
              <div style={{ fontWeight: 600, overflowWrap: "anywhere" }}>{p.where}</div>
              <div style={hint}>sample: {p.sample}{p.sampledAt ? `, read ${p.sampledAt}` : ""}</div>
              {p.memberLabel && <div style={{ fontSize: 12 }}>member label {p.memberLabel}; members seen: {(p.membersSeen ?? []).join(", ") || "none"}</div>}
              {p.notes.map((note) => <div key={note} style={hint}>{note}</div>)}
              {p.draft.installation && (
                <button className="l-btn" style={small} disabled={busy}
                  onClick={() => openEditor({ kind: "installation", op: "add", draft: p.draft.installation!.source, subscriptions: p.draft.subscriptions, title: `Review the installation proposed in ${p.host}` })}>Edit and review…</button>
              )}
            </div>
          ))}
        </div>
      )}

      {offers.length > 0 && (<>
        <h4 className="settings-subhead">Make a shared installation</h4>
        {offers.map((o) => (
          <div key={o.where} data-testid="migration-offer" style={rowStyle}>
            <div style={{ fontWeight: 600, overflowWrap: "anywhere" }}>{o.clusters.join(", ")} read {o.where}</div>
            {o.scopes.map((sc) => (
              <div key={sc.cluster} style={{ fontSize: 12, overflowWrap: "anywhere" }}><span style={{ fontFamily: "var(--font-mono)" }}>{sc.cluster}</span>: {sc.before} → {sc.after} ({sc.change})</div>
            ))}
            {o.conflicts.map((c) => <div key={c} style={{ fontSize: 12, color: "var(--color-text-warning)" }}>▲ {c}</div>)}
            {o.choices.map((c) => <div key={c} style={{ fontSize: 12, color: "var(--color-text-warning)" }}>{c}</div>)}
            {o.credential && <div style={hint}>{o.credential}</div>}
            {o.draft.installation && (
              <button className="l-btn" style={small} disabled={busy}
                onClick={() => openEditor({ kind: "installation", op: "add", draft: o.draft.installation!.source, subscriptions: o.draft.subscriptions, title: `Review the shared installation for ${o.clusters.join(", ")}` })}>Edit and review…</button>
            )}
          </div>
        ))}
      </>)}
      {planPanel}
    </div>
  );
}
