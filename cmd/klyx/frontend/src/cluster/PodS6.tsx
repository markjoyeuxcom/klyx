import { Fragment, useEffect, useId, useRef, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import type { EphemeralDTO, PodDetailDTO, ResizeConditionDTO, TripleDTO } from "../store/fleet";
import { InspectorBlock } from "../layout/InspectorLayout";
import { fmtCpu, fmtMem } from "./saturation";
import { debugPreflight, type DebugPreflightDTO } from "../bridge/exec";
import { trapTab } from "../chrome/modalFocus";
import { useRestoreFocus } from "../chrome/useRestoreFocus";

// Coverage S6: pods, correct. A pod's resize state as evidence (desired,
// allocated, actual), the scheduler's reservation as an estimate under a
// named assumption, the debug containers as evidence that never changes
// the pod's verdict, and the debug verb's image and profile with the
// privilege each adds.

// podStale: the kubelet has not acknowledged the latest spec. Only when it
// reports a generation (observedGeneration > 0): a kubelet that does not
// report one is never stuck Syncing. Acknowledged is not completed.
export function podStale(p: { generation?: number; observedGeneration?: number }): boolean {
  const g = p.generation ?? 0, o = p.observedGeneration ?? 0;
  return g > 0 && o > 0 && g > o;
}

const genText = (g: number) => (g > 0 ? `generation ${g}` : "generation not reported");

function ResizeCondLine({ type, c }: { type: string; c: ResizeConditionDTO }) {
  return (
    <div className="t-body" data-testid={`resize-cond-${type}`} style={{ overflowWrap: "anywhere" }}>
      <span className="t-legend" style={{ marginRight: 6 }}>{type}</span>
      {c.reason || "active"}
      <span className="t-secondary"> · {type === "PodResizePending" ? "attempted at" : "started at"} {genText(c.generation)}</span>
      {c.message && <div className="t-secondary">{c.message}</div>}
    </div>
  );
}

function tripleText(t: TripleDTO, fmt: (n: number) => string): string {
  const v = (n: number | null | undefined) => (n == null ? null : fmt(n));
  const parts = [`desired ${v(t.desired) ?? "unset"}`];
  if (t.allocated != null) parts.push(`allocated ${v(t.allocated)}`);
  if (t.actual != null) parts.push(`actual ${v(t.actual)}`);
  return parts.join(" · ");
}

// PodResourcesBlock is the inspector's "Resources" block (S6 §8).
export function PodResourcesBlock({ detail }: { detail: PodDetailDTO }) {
  const s = detail.summary;
  const units = detail.resources ?? [];
  const rz = s.resize;
  const g = s.generation ?? 0, o = s.observedGeneration ?? 0;
  const anyReported = units.some((u) => [u.cpuReq, u.memReq, u.cpuLim, u.memLim].some((t) => t.allocated != null || t.actual != null));
  return (
    <InspectorBlock title="Resources">
      <div data-testid="pod-resources" style={{ display: "grid", gap: 6 }}>
        {units.map((u) => (
          <div key={u.name || "(pod)"} data-testid={`resources-${u.name || "pod-level"}`}>
            <div className="t-legend">{u.name ? `${u.name}${u.sidecar ? " (sidecar)" : ""}` : "pod level"}</div>
            <div className="insp-kv">
              {([["cpu request", u.cpuReq, fmtCpu], ["cpu limit", u.cpuLim, fmtCpu], ["memory request", u.memReq, fmtMem], ["memory limit", u.memLim, fmtMem]] as const)
                .filter(([, t]) => t.desired != null || t.allocated != null || t.actual != null)
                .map(([label, t, fmt]) => (
                  <Fragment key={label}>
                    <span>{label}</span>
                    <span style={{ overflowWrap: "anywhere" }}>{tripleText(t, fmt)}{t.differs && <span className="t-secondary" data-testid="resources-differs"> · not yet applied</span>}</span>
                  </Fragment>
                ))}
              {(u.cpuRestart === "RestartContainer" || u.memoryRestart === "RestartContainer") && (
                <><span>resize policy</span><span>restarts the container on a {[u.cpuRestart === "RestartContainer" && "cpu", u.memoryRestart === "RestartContainer" && "memory"].filter(Boolean).join(" or ")} resize</span></>
              )}
            </div>
          </div>
        ))}
        {units.length > 0 && !anyReported && <div className="t-secondary">The kubelet reports no allocated or actual resources for this pod.</div>}
        {rz?.inProgress && <ResizeCondLine type="PodResizeInProgress" c={rz.inProgress} />}
        {rz?.pending && <ResizeCondLine type="PodResizePending" c={rz.pending} />}
        {rz?.legacy && <div className="t-secondary">status.resize (deprecated): {rz.legacy}</div>}
        <div className="t-secondary" data-testid="pod-generation">
          {o > 0 ? (g > o ? `The kubelet has acknowledged generation ${o} of ${g}.` : `The kubelet has acknowledged generation ${o}; acknowledged is not completed.`)
            : `The kubelet does not report observedGeneration; whether it acknowledged generation ${g} is not known.`}
        </div>
        {s.sched && (
          <div className="insp-kv" data-testid="pod-sched">
            <span>scheduled</span>
            <span>cpu {fmtCpu(s.sched.cpu)} · memory {fmtMem(s.sched.mem)}
              <div className="t-secondary">an estimate: {s.sched.assumption}</div>
              {((s.sched.altCpu ?? 0) > 0 || (s.sched.altMem ?? 0) > 0) && (
                <div className="t-secondary" data-testid="pod-sched-alt">a scheduler one minor behind (allowed during an upgrade) would reserve +{fmtCpu(s.sched.altCpu ?? 0)} cpu · +{fmtMem(s.sched.altMem ?? 0)} memory</div>
              )}
            </span>
            <span>desired</span>
            <span>cpu {fmtCpu(s.sched.desiredCpu)} · memory {fmtMem(s.sched.desiredMem)}<div className="t-secondary">what a replacement pod is admitted with</div></span>
          </div>
        )}
        <ul className="t-secondary" data-testid="pod-resources-limits" style={{ margin: 0, paddingLeft: 16 }}>
          <li>The reservation assumes the scheduler's minor and its default feature gates; neither is read from the cluster.</li>
          <li>DRA device resources are not counted.</li>
        </ul>
      </div>
    </InspectorBlock>
  );
}

function ephemeralState(e: EphemeralDTO): string {
  switch (e.state) {
    case "running": return "running";
    case "waiting": return `waiting${e.reason ? `: ${e.reason}` : ""}`;
    case "terminated": return `exited ${e.exitCode ?? 0}${e.reason && e.reason !== "Error" ? ` (${e.reason})` : ""}`;
  }
  return "no status yet";
}

function profileEvidence(e: EphemeralDTO): string {
  const parts: string[] = [];
  if (e.privileged) parts.push("privileged");
  if ((e.capsAdded ?? []).length > 0) parts.push(`adds ${e.capsAdded!.join(", ")}`);
  if (e.runAsNonRoot) parts.push("non-root");
  return parts.length > 0 ? parts.join(" · ") : "no added privilege";
}

// DebugContainersBlock lists a pod's ephemeral containers (S6 §5).
export function DebugContainersBlock({ detail }: { detail: PodDetailDTO }) {
  const eph = detail.ephemeral ?? [];
  if (eph.length === 0) return null;
  return (
    <InspectorBlock title="Debug containers" count={eph.length + (detail.ephemeralMore ?? 0)}>
      <div data-testid="pod-debug-containers" style={{ display: "grid", gap: 4 }}>
        <div className="t-secondary">Debug containers are evidence, not the application's failure: they never change the pod's verdict, and they stay until the pod is recreated.</div>
        {(detail.ephemeralMore ?? 0) > 0 && <div className="t-secondary">{detail.ephemeralMore} older debug containers not listed.</div>}
        {eph.map((e) => (
          <div key={e.name} className="t-body" data-testid={`debug-container-${e.name}`} style={{ overflowWrap: "anywhere" }}>
            <span className="t-identifier">{e.name}</span>
            <span className="t-secondary"> · {e.image}{e.target ? ` · target ${e.target}` : ""}</span>
            <div className="t-secondary">{ephemeralState(e)} · {profileEvidence(e)}</div>
          </div>
        ))}
      </div>
    </InspectorBlock>
  );
}

export type DebugChoice = { image: string; profile: string; adds: string; qualified?: string };

// imageLike is a light client-side check; the backend validates the
// reference before anything runs.
const imageLike = (s: string) => s.length > 0 && s.length <= 255 && !/\s/.test(s);

// DebugChooser picks the debug container's image and profile before the
// confirmation (S6 §6). It reads the preflight: the profiles and what each
// adds, and the kubectl the terminal runs; a refused kubectl continues
// nowhere.
export function DebugChooser({ cluster, target, onCancel, onContinue }: { cluster: string; target: string; onCancel: () => void; onContinue: (c: DebugChoice) => void }) {
  const [pre, setPre] = useState<{ cluster: string; dto: DebugPreflightDTO } | null>(null);
  const [profile, setProfile] = useState("");
  const [custom, setCustom] = useState(false);
  const [image, setImage] = useState("");
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  useRestoreFocus();
  useEffect(() => {
    let live = true;
    void debugPreflight(cluster).then((dto) => { if (live) setPre({ cluster, dto }); });
    return () => { live = false; };
  }, [cluster]);
  const dto = pre && pre.cluster === cluster ? pre.dto : null;
  const chosenProfile = profile || dto?.defaultProfile || "general";
  const chosenImage = custom ? image.trim() : dto?.defaultImage || "busybox:1.36";
  const adds = dto?.profiles.find((p) => p.name === chosenProfile)?.adds ?? "";
  const blocked = !dto || !dto.ok || !imageLike(chosenImage);
  return (
    <div onClick={onCancel} style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.4)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 1000 }}>
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} data-testid="debug-chooser"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => { e.stopPropagation(); if (e.key === "Escape") onCancel(); else trapTab(e, dialogRef.current); }}
        style={{ background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 20, width: 440, maxWidth: "calc(100% - 32px)", maxHeight: "calc(100% - 32px)", overflowY: "auto", boxSizing: "border-box", display: "grid", gap: 10, fontSize: 13 }}>
        <div id={titleId} style={{ fontWeight: 600 }}>Debug shell{target ? ` · target ${target}` : ""}</div>
        {!dto ? <div className="t-secondary">Reading kubectl…</div> : (
          <>
            <fieldset style={{ border: 0, padding: 0, margin: 0, display: "grid", gap: 4 }}>
              <legend className="t-legend" style={{ marginBottom: 4 }}>Profile</legend>
              {dto.profiles.map((p) => (
                <label key={p.name} style={{ display: "flex", gap: 6, alignItems: "baseline", cursor: "pointer" }}>
                  <input type="radio" name="debug-profile" value={p.name} checked={chosenProfile === p.name} onChange={() => setProfile(p.name)} autoFocus={p.name === chosenProfile} />
                  <span>
                    <span className="t-identifier">{p.name}</span>
                    {p.name === "sysadmin" && <IconAlertTriangle size={12} stroke={1.8} style={{ color: "var(--color-text-danger)", marginLeft: 4, verticalAlign: "-1px" }} aria-label="adds full node access" />}
                    <span className="t-secondary"> · {p.adds}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <div style={{ display: "grid", gap: 4 }}>
              <span className="t-legend">Image</span>
              <label style={{ display: "flex", gap: 6, alignItems: "center" }}>
                <input type="radio" name="debug-image" checked={!custom} onChange={() => setCustom(false)} />
                <span className="t-identifier">{dto.defaultImage}</span>
              </label>
              <label style={{ display: "flex", gap: 6, alignItems: "center" }}>
                <input type="radio" name="debug-image" checked={custom} onChange={() => setCustom(true)} />
                <input className="l-input" aria-label="custom debug image" placeholder="registry/image:tag" value={image} onFocus={() => setCustom(true)} onChange={(e) => setImage(e.target.value)}
                  style={{ flex: 1, minWidth: 0, fontFamily: "var(--font-mono)", fontSize: 12 }} />
              </label>
              {custom && image !== "" && !imageLike(image.trim()) && <span className="t-secondary" style={{ color: "var(--color-text-danger)" }}>Not an image reference.</span>}
            </div>
            <div className="t-secondary" data-testid="debug-kubectl">
              {dto.ok ? `kubectl ${dto.kubectl}${dto.qualified ? `: ${dto.qualified}` : ""}` : `Refused: ${dto.reason || dto.error}`}
            </div>
          </>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          <button className="l-btn" onClick={onCancel}>Cancel</button>
          <button className="l-btn" data-testid="debug-continue" disabled={blocked}
            onClick={() => onContinue({ image: chosenImage, profile: chosenProfile, adds, qualified: dto?.qualified })}>Continue</button>
        </div>
      </div>
    </div>
  );
}

// debugConfirmText names the image, the profile and its added privilege,
// and the consequence (S6 §6).
export function debugConfirmText(ns: string, name: string, target: string, c: DebugChoice): string {
  return `${ns}/${name}${target ? ` (target ${target})` : ""}: kubectl debug adds an ephemeral container - image ${c.image}, profile ${c.profile}: ${c.adds}.`
    + (c.qualified ? ` ${c.qualified}.` : "")
    + " It stays on the pod until the pod is recreated; Git and its controller do not remove it, and adding it increments the pod's generation.";
}
