import { useEffect, useRef, useState, type ReactNode } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { dismissPlan, reviewStep, runStep, type ActionPlan, type ActionStep, type ChatCheckSubject, type StepReview } from "../bridge/chat";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { Rail, type AspectKind, type RailSegment, type RailStep } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import type { Cite } from "./SafeMarkdown";

// ActionCard is an investigation turn's proposed plan (AI A3 §3): the
// model proposed it, Klyx decided each step, and a step runs only when the
// operator reviews and confirms it, in order, through the verb's own code.
// The plan is a stretch of The Line: the segment into a step is lit only
// when the step before it took its effect, so the rule that each step needs
// the last is drawn, not only said. Every outcome is in words.

const VERB: Record<string, string> = {
  "flux-suspend": "suspend",
  "flux-resume": "resume",
  "flux-reconcile": "reconcile",
  "rollout-restart": "restart",
  scale: "scale",
  "rollout-undo": "roll back",
  "rollout-pause": "pause rollouts of",
  "rollout-resume": "resume rollouts of",
  "delete-pod": "delete",
  cordon: "cordon",
  uncordon: "uncordon",
  "keda-pause": "pause autoscaling of",
  "keda-resume": "resume autoscaling of",
};

const verbWords = (v: string) => VERB[v] ?? v;

function objectText(t: ChatCheckSubject): string {
  return `${t.kind} ${t.namespace ? `${t.namespace}/` : ""}${t.name}`;
}

// plannedChange is what the step asks for, from its own parameters, before
// Review reads the object again and words the whole change.
function plannedChange(st: ActionStep): string {
  if (st.verb === "scale" && st.replicas !== undefined) return `replicas → ${st.replicas}`;
  if (st.verb === "rollout-undo" && st.revision !== undefined) return `to revision ${st.revision}`;
  return "";
}

// the three outcomes, in §5's words
function requestWords(st: ActionStep): string {
  const r = st.run;
  if (!r) return "";
  if (r.request === "refused") return r.notSent ? "refused, nothing sent" : "refused by the server";
  return `request ${r.request}`;
}

const EFFECT: Record<string, string> = { pending: "effect pending", established: "effect established", unverified: "effect unverified" };

function chip(text: string, tone: "checked" | "suspected" | "unsupported" | "proposed", testid?: string): ReactNode {
  return <span className={`inv-label inv-label-${tone}`} data-testid={testid}>{text}</span>;
}

function requestTone(st: ActionStep): "checked" | "suspected" | "unsupported" {
  if (st.run?.request === "written") return "checked";
  if (st.run?.request === "refused") return "unsupported";
  return "suspected";
}

function effectTone(effect: string | undefined): "checked" | "suspected" | "proposed" {
  if (effect === "established") return "checked";
  if (effect === "pending") return "proposed";
  return "suspected";
}

// isCurrent: the plan can still run a step (no status, or held by a step
// that the operator may yet dismiss).
const isCurrent = (p: ActionPlan) => !p.status;

// nextStep is the step the operator may review now, as the card reads it.
// The backend decides again on Review and on Run; this is emphasis only.
function nextStep(p: ActionPlan): number {
  if (!isCurrent(p)) return 0;
  for (const st of p.steps) {
    if (st.run) {
      if (st.run.effect === "established") continue;
      return 0; // running, refused or unverified: the plan waits or holds
    }
    return st.state === "offered" ? st.n : 0;
  }
  return 0;
}

function stepMark(st: ActionStep, next: number): AspectKind | undefined {
  if (st.run) {
    if (st.run.effect === "pending") return "syncing";
    if (st.run.effect === "established") return undefined;
    return "attention";
  }
  if (st.state !== "offered") return "paused";
  return st.n === next ? "attention" : undefined;
}

// segmentInto is the stretch of line from step n-1 into step n: lit (the
// route) once step n-1 took its effect, the plain track while it waits -
// its words spoken, the step's own state shown - and danger when step n-1
// held the plan, said on the line.
function segmentInto(prev: ActionStep): { segment: RailSegment; words: string } {
  const r = prev.run;
  if (r?.effect === "established") return { segment: "completed", words: `step ${prev.n}'s effect established` };
  if (r && (r.request === "refused" || r.effect === "unverified")) return { segment: "failed", words: `step ${prev.n} held the plan` };
  return { segment: "dependsOn", words: `waits for step ${prev.n}` };
}

function at(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

type Props = {
  cluster: string;
  conversationId: string;
  exchangeId: string;
  plan: ActionPlan;
  protected: boolean;
  cite?: Cite;
  openObject?: (st: ActionStep) => void;
  onChanged: () => void;
};

export function ActionCard({ cluster, conversationId, exchangeId, plan, protected: isProtected, cite, openObject, onChanged }: Props) {
  const [review, setReview] = useState<StepReview | null>(null);
  const [busy, setBusy] = useState<number | "dismiss" | null>(null);
  const [notes, setNotes] = useState<Record<number, string>>({});
  // the plan as it now stands, for replies that arrive later: a review is
  // shown only while its step is still the plan's next (stage 5 review,
  // finding 3), and an open one closes when it no longer is
  const latest = useRef(plan);
  latest.current = plan;
  const next = nextStep(plan);
  useEffect(() => {
    if (review && next !== review.n) setReview(null);
  }, [review, next]);
  if (plan.steps.length === 0 && !plan.note) return null;
  const running = plan.steps.some((s) => s.run?.effect === "pending");
  const unrun = plan.steps.some((s) => s.state === "offered" && !s.run);
  const note = (n: number, text: string) => setNotes((m) => ({ ...m, [n]: text }));

  const onReview = async (st: ActionStep) => {
    setBusy(st.n);
    note(st.n, "");
    try {
      const r = await reviewStep(cluster, conversationId, exchangeId, st.n);
      if (r.offered && r.token && nextStep(latest.current) === r.n) setReview(r);
      else if (r.offered) note(st.n, "the plan changed while Klyx read the step: review it again if it is still offered");
      else {
        note(st.n, r.reason || "Klyx did not offer the step");
        onChanged();
      }
    } catch (e) {
      note(st.n, `Klyx could not review the step: ${String(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const onRun = async (r: StepReview) => {
    setReview(null);
    setBusy(r.n);
    const what = `${verbWords(r.verb)} ${objectText(r.target)}`;
    const activity = useFleet.getState().setActionStatus;
    try {
      const res = await runStep(cluster, conversationId, exchangeId, r.n, r.token ?? "");
      if (res.ran && res.result) {
        const words = res.result.outcome === "refused" ? (res.result.notSent ? "refused, nothing sent" : "refused by the server") : `request ${res.result.outcome}`;
        activity({
          kind: res.result.outcome === "written" ? "success" : res.result.outcome === "refused" ? "error" : "info",
          message: `${what} on ${cluster} - from an investigation's plan, step ${r.n}: ${words}${res.result.error ? `: ${res.result.error}` : ""}`,
        });
      } else {
        const why = res.reason || "Klyx did not run the step";
        note(r.n, why);
        activity({ kind: "error", message: `${what} on ${cluster} - from an investigation's plan, step ${r.n}: not run: ${why}` });
      }
    } catch (e) {
      // the bridge lost RunStep's answer: the step may have run, so it is
      // recorded as unverified, never as not run, and the plan's state
      // read back says what happened (stage 5 review, finding 2)
      const why = `request unverified: Klyx lost RunStep's answer (${String(e)}); the plan shows whether it ran`;
      note(r.n, why);
      activity({ kind: "error", message: `${what} on ${cluster} - from an investigation's plan, step ${r.n}: ${why}` });
    } finally {
      setBusy(null);
      onChanged();
    }
  };

  const onDismiss = async () => {
    setBusy("dismiss");
    try {
      const r = await dismissPlan(cluster, conversationId, exchangeId);
      if (!r.plan && r.reason) note(0, r.reason);
    } catch (e) {
      note(0, `Klyx could not dismiss the plan: ${String(e)}`);
    } finally {
      setBusy(null);
      onChanged();
    }
  };

  const steps: RailStep[] = plan.steps.map((st, i) => {
    const prev = i > 0 ? segmentInto(plan.steps[i - 1]) : { segment: "source" as RailSegment, words: "" };
    const change = plannedChange(st);
    const label = (
      <span className="act-head" data-testid="act-step-head">
        <span className="act-n">{st.n}</span>
        {st.danger && <IconAlertTriangle size={12} stroke={1.8} className="act-danger" aria-label="destructive" data-testid="act-danger" />}
        <span className="act-verb">{verbWords(st.verb)}</span>
        {st.target.uid && openObject ? (
          <button type="button" className="chat-cite act-object" title={`UID ${st.target.uid}`} onClick={() => openObject(st)}>{objectText(st.target)}</button>
        ) : (
          <span className="act-object" title={st.target.uid ? `UID ${st.target.uid}` : undefined}>{objectText(st.target)}</span>
        )}
        {change && <code className="act-change">{change}</code>}
      </span>
    );
    const body = (
      <div className="act-body" data-testid="act-step" data-state={st.run ? st.run.effect ?? st.run.request : st.state}>
        {st.why && (
          <div className="chat-small">
            <span className="t-legend">the model's reading</span> {st.why}
            {st.cites.length > 0 && <> · cites {st.cites.map((id, j) => cite?.(id, `${st.n}-${j}`) ?? <span key={j} className="t-legend">{id}</span>)}</>}
          </div>
        )}
        {st.factsAt && <div className="act-meta">Klyx read it at {at(st.factsAt)}{st.factsRead ? ` (${st.factsRead})` : ""}</div>}
        {st.state !== "offered" && !st.run && <div className="chat-small act-reason" data-testid="act-reason">{st.state}: {st.reason}</div>}
        {st.run && (
          <div className="act-outcome" data-testid="act-outcome">
            {chip(requestWords(st), requestTone(st), "act-request")}
            {st.run.effect && chip(EFFECT[st.run.effect] ?? `effect ${st.run.effect}`, effectTone(st.run.effect), "act-effect")}
            {st.run.ranAt && <span className="act-meta">ran {at(st.run.ranAt)}</span>}
            {st.run.effectWhy && <div className="chat-small">{st.run.effectWhy}</div>}
            {st.run.error && <div className="chat-small act-error">{st.run.error}</div>}
          </div>
        )}
        {notes[st.n] && <div className="chat-small act-note" role="status" data-testid="act-note">{notes[st.n]}</div>}
        {st.n === next && (
          <div>
            <button type="button" className="l-btn l-btn-primary" data-testid="act-review" disabled={busy !== null} aria-busy={busy === st.n} onClick={() => void onReview(st)}>
              {busy === st.n ? "Reading…" : "Review…"}
            </button>
          </div>
        )}
      </div>
    );
    return { key: String(st.n), segment: prev.segment, segmentWords: prev.words, name: `step ${st.n}: ${verbWords(st.verb)} ${objectText(st.target)}`, label, body, aspect: stepMark(st, next) };
  });

  return (
    <section className="act-card" data-testid="act-card" aria-label="Proposed plan">
      <div className="t-section-label">Proposed plan</div>
      <div className="chat-small" data-testid="act-header">
        Proposed by the model. Klyx checked that each step is eligible to run, not that it is the right remedy. A step runs only when you confirm it.
      </div>
      <div className="act-meta">on {cluster} · {plan.steps.length} {plan.steps.length === 1 ? "step" : "steps"}</div>
      {isProtected && <div className="chat-small act-protected">Protected: each step needs its own typed confirmation.</div>}
      {plan.status && (
        <div className={`chat-small act-status act-status-${plan.status}`} data-testid="act-status">
          <span className="inv-label">{plan.status}</span>{plan.statusWhy}
        </div>
      )}
      {plan.note && <div className="chat-small" data-testid="act-plan-note">{plan.note}</div>}
      {steps.length > 0 && <Rail label="The plan's steps, in order" steps={steps} />}
      {plan.incident && <IncidentRows incident={plan.incident} steps={plan.steps} />}
      {notes[0] && <div className="chat-small act-note" role="status">{notes[0]}</div>}
      <div className="act-foot">
        {isCurrent(plan) || plan.status === "held" ? (
          !running && unrun && (
            <button type="button" className="l-btn" data-testid="act-dismiss" disabled={busy !== null} onClick={() => void onDismiss()}>Dismiss plan</button>
          )
        ) : null}
        <span className="chat-small" style={{ color: "var(--color-text-tertiary)" }}>
          Klyx reads each step again before it writes; between that read and the write, and across objects, nothing is atomic.
        </span>
      </div>
      {review && (
        <ConfirmDialog
          title={`Run step ${review.n} of the investigation's plan`}
          cluster={cluster}
          protected={isProtected}
          danger={review.danger}
          confirmLabel={verbWords(review.verb).replace(/^./, (c) => c.toUpperCase())}
          detail={<ReviewDetail r={review} />}
          onConfirm={() => void onRun(review)}
          onCancel={() => setReview(null)}
        />
      )}
    </section>
  );
}

// ReviewDetail is the step in Klyx's words, from the reads Review just made.
function ReviewDetail({ r }: { r: StepReview }) {
  return (
    <div className="act-review" data-testid="act-review-detail">
      <div className="act-meta">{verbWords(r.verb)} {objectText(r.target)}</div>
      {r.change && <div><code>{r.change}</code></div>}
      {(r.consequence ?? []).length > 0 && (
        <ul data-testid="act-consequence">{(r.consequence ?? []).map((c) => <li key={c}>{c}</li>)}</ul>
      )}
      {r.expected && <div data-testid="act-expected">{r.expected}</div>}
      {(r.observed ?? []).length > 0 && (
        <ul className="act-observed">{(r.observed ?? []).map((o) => <li key={o.what}>read {o.what} at {at(o.at)}</li>)}</ul>
      )}
      {r.expiresAt && <div className="act-meta">This review holds until {at(r.expiresAt)}; Klyx reads it all again before it writes.</div>}
    </div>
  );
}

const INCIDENT_TONE: Record<string, "checked" | "suspected" | "unsupported" | "proposed"> = {
  resolved: "checked", "not resolved": "unsupported", unverified: "suspected", pending: "proposed",
};

// holdReminder says how a subject held by a step of the plan stays, in its
// verb's own words (stage 5 review, finding 4).
function holdReminder(subject: string, verb: string | undefined): string {
  switch (verb) {
    case "flux-suspend":
      return `${subject} stays suspended until the Git fix lands and it is resumed.`;
    case "cordon":
      return `${subject} stays cordoned until it is uncordoned.`;
    case "scale":
      return `${subject} stays at zero replicas until it is scaled back.`;
    case "keda-pause":
      return `${subject} stays paused until its autoscaling is resumed.`;
  }
  return `${subject} stays held by the plan until it is released.`;
}

function IncidentRows({ incident, steps }: { incident: NonNullable<ActionPlan["incident"]>; steps: ActionStep[] }) {
  const held = incident.subjects.flatMap((s) => {
    const m = /^held by step (\d+)/.exec(s.reading);
    if (!m) return [];
    const st = steps.find((x) => x.n === Number(m[1]));
    return [holdReminder(objectText({ kind: s.kind, namespace: s.namespace, name: s.name, identity: "uid" }), st?.verb)];
  });
  return (
    <div className="act-incident" data-testid="act-incident">
      <div>
        <span className="t-legend">incident</span>{" "}
        {chip(incident.state, INCIDENT_TONE[incident.state] ?? "suspected", "act-incident-state")}
        {incident.why && <span className="chat-small">{incident.why}</span>}
      </div>
      {incident.subjects.length > 0 && (
        <ul>
          {incident.subjects.map((s) => (
            <li key={s.uid} className="chat-small" data-testid="act-incident-subject">
              {objectText({ kind: s.kind, namespace: s.namespace, name: s.name, identity: "uid" })}: {s.reading}
              {s.verdict && s.at && <span className="act-meta"> · Klyx read {s.verdict} at {at(s.at)}</span>}
            </li>
          ))}
        </ul>
      )}
      {incident.state === "resolved" && held.map((h) => <div key={h} className="chat-small" data-testid="act-held">{h}</div>)}
    </div>
  );
}
