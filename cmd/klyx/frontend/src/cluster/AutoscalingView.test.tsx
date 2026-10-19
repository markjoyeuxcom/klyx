import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import type { AutoscalingPartRows } from "../health/useAutoscalingRows";
import type { HPARowDTO, KEDAAuthDTO, KEDAInspectionDTO, KEDAJobsDTO, KEDAPauseDTO, ScaledJobDTO, ScaledObjectDTO } from "../bridge/keda";
import { useFleet } from "../store/fleet";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[]): AutoscalingPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null });
let hpas: AutoscalingPartRows<HPARowDTO>;
let sos: AutoscalingPartRows<ScaledObjectDTO>;
let sjs: AutoscalingPartRows<ScaledJobDTO>;
let auths: AutoscalingPartRows<KEDAAuthDTO>;
vi.mock("../health/useAutoscalingRows", () => ({ useHPAs: () => hpas, useScaledObjects: () => sos, useScaledJobs: () => sjs, useKEDAAuths: () => auths }));
const bridge = vi.hoisted(() => ({
  inspection: { uid: "so-1", handler: "keda/keda-operator", version: "2.21.0", candidates: [], disagree: [], auths: [] } as KEDAInspectionDTO,
  jobs: { jobs: [], complete: true, read: 0, owned: 0 } as KEDAJobsDTO,
}));
vi.mock("../bridge/keda", () => ({
  inspectScaler: vi.fn(async () => bridge.inspection),
  scaledJobJobs: vi.fn(async () => bridge.jobs),
  setScalerPaused: vi.fn(async () => ({ ok: true, error: "" })),
}));
const keda = await import("../bridge/keda");
const { AutoscalingView, pauseDetail } = await import("./AutoscalingView");

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
const noPause: KEDAPauseDTO = { kind: "", directions: [], completed: false };
const so = (name: string, role: string, label: string, over: Partial<ScaledObjectDTO> = {}): ScaledObjectDTO => ({
  uid: `uid-${name}`, gvr: "keda.sh/v1alpha1/scaledobjects", namespace: "team", name, targetGroup: "apps", targetKind: "Deployment", targetName: name,
  triggers: [{ type: "cron" }], triggerCount: 1, conditions: [{ type: "Ready", status: "True", reason: "ScaledObjectReady" }], metrics: [], healthTracked: false,
  pause: noPause, hpaCurrent: true, hpaComplete: true, others: [], version: "2.21.0", authsRead: "found", findings: [{ role, label }], rule: rule(role, label), ...over,
});
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "Autoscaling objects" })).getByRole("button", { name });

describe("AutoscalingView (coverage S9)", () => {
  beforeEach(() => {
    hpas = part([], ["HorizontalPodAutoscaler"]);
    sos = part([], ["ScaledObject"]);
    sjs = part([], ["ScaledJob"]);
    auths = part([], ["TriggerAuthentication", "ClusterTriggerAuthentication"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", kedaPresent: true } as never] });
    vi.mocked(keda.setScalerPaused).mockClear();
  });

  it("a fallback reads degraded with every finding; KEDA's conditions say what they do not report", async () => {
    sos = part([
      so("web", "ok", "Active"),
      so("failing", "degraded", "Fallback active (static)", {
        conditions: [{ type: "Ready", status: "True" }, { type: "Fallback", status: "True", reason: "FallbackExists" }],
        fallback: { failureThreshold: 2, replicas: 2, behavior: "static" }, healthTracked: true,
        metrics: [{ metric: "s0-prometheus", failures: 5, status: "Failing", active: true }],
        findings: [{ role: "degraded", label: "Fallback active (static)", text: "fallback active (static): s0-prometheus failed 5 times (threshold 2)" }, { role: "ok", label: "Active" }],
        hpa: { name: "keda-hpa-failing", uid: "h", max: 3, current: 2, desired: 2, conditions: [{ type: "ScalingActive", status: "True" }] },
      }),
    ], ["ScaledObject"]);
    render(<AutoscalingView cluster="c" />);
    expect(document.querySelectorAll(".lens-row")[0].textContent).toContain("team/failing");
    await act(async () => { fireEvent.click(screen.getByText("team/failing")); });
    const panel = screen.getByTestId("keda-so-panel");
    expect(within(panel).getByTestId("keda-conditions").textContent).toContain("HPAActive not reported");
    expect(within(panel).getByTestId("keda-findings").textContent).toContain("s0-prometheus failed 5 times (threshold 2)");
    expect(within(panel).getByTestId("keda-triggers").textContent).toContain("5 failures (Failing)");
    expect(within(panel).getByTestId("keda-hpa").textContent).toContain("keda-hpa-failing");
    expect(panel.textContent).toContain("does not prove which count results");
    expect(panel.textContent).not.toContain("held at");
  });

  it("Pause asks, names the HPA's deletion and the GitOps owner, and sends the armed UID", async () => {
    sos = part([so("web", "ok", "Active", { owner: { kind: "Kustomization", namespace: "flux-system", name: "apps" },
      hpa: { name: "keda-hpa-web", uid: "h", max: 3, current: 1, desired: 1, conditions: [] } })], ["ScaledObject"]);
    render(<AutoscalingView cluster="c" />);
    await act(async () => { fireEvent.click(screen.getByText("team/web")); });
    fireEvent.click(screen.getByTestId("keda-pause"));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("KEDA deletes its HPA (keda-hpa-web)");
    expect(dialog.textContent).toContain("Owned by Kustomization flux-system/apps");
    await act(async () => { fireEvent.click(within(dialog).getByRole("button", { name: "Pause" })); });
    expect(keda.setScalerPaused).toHaveBeenCalledWith("c", "scaledobjects", "team", "web", "uid-web", true);
  });

  it("an unknown or too-old KEDA version offers no working pause; another pause annotation offers no resume", async () => {
    sos = part([
      so("old", "ok", "Active", { version: "2.11.0" }),
      so("unknown", "ok", "Active", { version: "unknown" }),
      so("held", "paused", "Paused at 1", { pause: { paused: "true", pausedReplicas: "1", kind: "full", annotation: "autoscaling.keda.sh/paused-replicas", requested: 1, applied: 1, directions: [], completed: true } }),
    ], ["ScaledObject"]);
    render(<AutoscalingView cluster="c" />);
    await act(async () => { fireEvent.click(screen.getByText("team/old")); });
    expect((screen.getByTestId("keda-pause") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("keda-verb-limit").textContent).toContain("KEDA 2.11.0 does not apply");
    await act(async () => { fireEvent.click(screen.getByText("team/unknown")); });
    expect(screen.getByTestId("keda-verb-limit").textContent).toContain("KEDA version unknown");
    await act(async () => { fireEvent.click(screen.getByText("team/held")); });
    expect(screen.queryByTestId("keda-resume")).toBeNull();
    expect(screen.getByTestId("keda-so-panel").textContent).toContain("set outside Klyx");
  });

  it("the operator and authentication evidence: a missing authentication and an EKS role the workload's ServiceAccount lacks", async () => {
    bridge.inspection = { uid: "uid-web", handler: "keda/keda-operator", version: "2.21.0", candidates: [], disagree: [], auths: [
      { kind: "TriggerAuthentication", name: "ghost", state: "not found", visible: {}, refs: [] },
      { kind: "TriggerAuthentication", name: "aws", state: "exists", visible: { "keda/keda-operator": "visible" }, refs: [],
        subject: { provider: "aws", mode: "workload", base: "the KEDA operator, assuming the role on the target workload's ServiceAccount", serviceAccount: "keda/keda-operator",
          roleFrom: "team/bare", requested: [], refusals: ["KEDA requires eks.amazonaws.com/role-arn on ServiceAccount team/bare; it has none"], unresolved: [], notes: [] } },
    ] };
    sos = part([so("web", "fault", "TriggerAuthentication ghost does not exist", { triggers: [{ type: "prometheus", authName: "ghost" }, { type: "aws-sqs-queue", authName: "aws" }] })], ["ScaledObject"]);
    render(<AutoscalingView cluster="c" />);
    await act(async () => { fireEvent.click(screen.getByText("team/web")); });
    const ev = screen.getByTestId("keda-auth-evidence").textContent ?? "";
    expect(ev).toContain("Handled by the KEDA operator keda/keda-operator (KEDA 2.21.0)");
    expect(ev).toContain("ghost: not found");
    expect(screen.getByTestId("keda-subject").textContent).toContain("KEDA requires eks.amazonaws.com/role-arn on ServiceAccount team/bare");
  });

  it("a KEDA-owned HPA is a fact, judged under its ScaledObject; the HPA tab lists it without a verdict", async () => {
    hpas = part([
      { uid: "h1", gvr: "autoscaling/v2/horizontalpodautoscalers", namespace: "team", name: "plain", target: { apiVersion: "apps/v1", kind: "Deployment", name: "api" },
        max: 3, current: 1, desired: 1, metrics: 1, metricTypes: ["Resource"], conditions: [{ type: "ScalingActive", status: "False", reason: "FailedGetResourceMetric" }],
        generation: 1, generationReported: false, others: [], rule: rule("fault", "Cannot compute replicas") },
      { uid: "h2", gvr: "autoscaling/v2/horizontalpodautoscalers", namespace: "team", name: "keda-hpa-web", target: { apiVersion: "apps/v1", kind: "Deployment", name: "web" },
        max: 3, current: 1, desired: 1, metrics: 1, metricTypes: ["External"], conditions: [], generation: 1, generationReported: true, observedGeneration: 1, kedaOwner: "web", others: [] },
    ], ["HorizontalPodAutoscaler"]);
    render(<AutoscalingView cluster="c" />);
    fireEvent.click(tab(/^HPAs/));
    const rows = [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");
    expect(rows[0]).toContain("team/plain");
    expect(rows[1]).toContain("KEDA's · judged under ScaledObject web");
    await act(async () => { fireEvent.click(screen.getByText("team/plain")); });
    expect(screen.getByTestId("hpa-panel").textContent).toContain("does not report which generation it observed");
    await act(async () => { fireEvent.click(screen.getByText("team/keda-hpa-web")); });
    expect(screen.getByTestId("hpa-keda-owned").textContent).toContain("never counted twice");
  });

  it("ScaledJob Jobs: the newest is a global claim only when the read completed", async () => {
    bridge.jobs = { jobs: [{ name: "batch-1", succeeded: 0, failed: 1, active: 0, complete: false, failedOut: true }], complete: false, read: 1000, owned: 1 };
    sjs = part([{ uid: "sj-1", gvr: "keda.sh/v1alpha1/scaledjobs", namespace: "team", name: "batch", triggers: [{ type: "cron" }], triggerCount: 1,
      conditions: [{ type: "Ready", status: "True" }], pause: noPause, version: "2.21.0", findings: [{ role: "ok", label: "Active" }], rule: rule("ok", "Active") }], ["ScaledJob"]);
    render(<AutoscalingView cluster="c" />);
    fireEvent.click(tab(/^ScaledJobs/));
    await act(async () => { fireEvent.click(screen.getByText("team/batch")); });
    const jobs = screen.getByTestId("keda-jobs").textContent ?? "";
    expect(jobs).toContain("batch-1: failed");
    expect(jobs).toContain("The newest among the first 1000 read; more exist (count unknown)");
    expect(jobs).toContain("not part of the verdict");
  });

  it("a ScaledJob's pause applies from KEDA 2.11, a ScaledObject's from 2.12", async () => {
    sjs = part([{ uid: "sj-1", gvr: "keda.sh/v1alpha1/scaledjobs", namespace: "team", name: "batch", triggers: [{ type: "cron" }], triggerCount: 1,
      conditions: [{ type: "Ready", status: "True" }], pause: noPause, version: "2.11.2", findings: [{ role: "ok", label: "Active" }], rule: rule("ok", "Active") }], ["ScaledJob"]);
    render(<AutoscalingView cluster="c" />);
    fireEvent.click(tab(/^ScaledJobs/));
    await act(async () => { fireEvent.click(screen.getByText("team/batch")); });
    expect((screen.getByTestId("keda-pause") as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByTestId("keda-verb-limit")).toBeNull();
  });

  it("an authentication is a fact; an incomplete users read is never unused", async () => {
    auths = part([{ uid: "ta-1", gvr: "keda.sh/v1alpha1/triggerauthentications", namespace: "team", name: "ta", cluster: false,
      sources: [{ kind: "secretTargetRef", refs: [{ kind: "Secret", name: "tok", key: "token" }], refCount: 1 }], users: [], usersComplete: false }], ["TriggerAuthentication", "ClusterTriggerAuthentication"]);
    render(<AutoscalingView cluster="c" />);
    fireEvent.click(tab(/^Authentications/));
    await act(async () => { fireEvent.click(screen.getByText("team/ta")); });
    const panel = screen.getByTestId("keda-auth-panel").textContent ?? "";
    expect(panel).toContain("A fact: KEDA reports no conditions");
    expect(panel).toContain("never unused");
  });

  it("the pause copy: a ScaledJob's Jobs continue; resume recreates the HPA", () => {
    expect(pauseDetail({ resource: "scaledjobs", pause: true })).toContain("Jobs already running continue");
    expect(pauseDetail({ resource: "scaledobjects", pause: false, target: "Deployment/web" })).toContain("KEDA recreates its HPA and resumes scaling Deployment/web");
  });
});
