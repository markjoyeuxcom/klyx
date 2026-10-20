import { describe, it, expect } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { useFleet, type PodDetailDTO, type PodSummaryDTO } from "../store/fleet";
import { DebugContainersBlock, PodExtendedBlock, PodResourcesBlock, debugConfirmText, podStale } from "./PodS6";

const summary = (over: Partial<PodSummaryDTO> = {}): PodSummaryDTO => ({
  namespace: "app", name: "web", uid: "u", ready: true, phase: "Running", reason: "", rank: "healthy", restarts: 0, node: "n1", ip: "", ownerKind: "", ownerName: "",
  ageSeconds: 60, containers: [], generation: 4, observedGeneration: 4, ...over,
});
const detail = (over: Partial<PodDetailDTO> = {}, s: Partial<PodSummaryDTO> = {}): PodDetailDTO => ({
  summary: summary(s), labels: {}, conditions: [], events: [], yaml: "", qosClass: "Burstable", serviceAccount: "default", ...over,
});

describe("coverage S6 pod pieces", () => {
  it("Syncing only when the kubelet reports a generation behind the spec", () => {
    expect(podStale({ generation: 5, observedGeneration: 4 })).toBe(true);
    expect(podStale({ generation: 5, observedGeneration: 5 })).toBe(false);
    expect(podStale({ generation: 5, observedGeneration: 0 })).toBe(false); // not reported: never stuck Syncing
    expect(podStale({ generation: 0, observedGeneration: 0 })).toBe(false);
  });

  it("the Resources block says desired, allocated and actual, the resize conditions, the acknowledgement and the scheduled estimate", () => {
    render(<PodResourcesBlock detail={detail({
      resources: [{ name: "app", cpuReq: { desired: 0.5, allocated: 0.1, actual: 0.1, differs: true }, memReq: { desired: 134217728, allocated: null, actual: null }, cpuLim: { desired: null, allocated: null, actual: null }, memLim: { desired: null, allocated: null, actual: null }, cpuRestart: "RestartContainer" }],
    }, {
      generation: 5, observedGeneration: 4,
      resize: { pending: { reason: "Infeasible", message: "Node didn't have enough capacity", generation: 4 } },
      sched: { cpu: 0.5, mem: 134217728, desiredCpu: 0.5, desiredMem: 134217728, altCpu: 0.25, assumption: "assuming kube-scheduler 1.37 at its default gates" },
    })} />);
    const b = screen.getByTestId("pod-resources");
    expect(b.textContent).toContain("desired 500m · allocated 100m · actual 100m");
    expect(screen.getByTestId("resources-differs")).toBeTruthy();
    expect(b.textContent).toContain("restarts the container on a cpu resize");
    expect(screen.getByTestId("resize-cond-PodResizePending").textContent).toContain("attempted at generation 4");
    expect(screen.getByTestId("pod-generation").textContent).toContain("acknowledged generation 4 of 5");
    expect(screen.getByTestId("pod-sched").textContent).toContain("an estimate: assuming kube-scheduler 1.37");
    expect(screen.getByTestId("pod-sched-alt").textContent).toContain("one minor behind");
  });

  it("an unreported observedGeneration is said, never Syncing or acknowledged", () => {
    render(<PodResourcesBlock detail={detail({ resources: [] }, { generation: 3, observedGeneration: 0 })} />);
    expect(screen.getByTestId("pod-generation").textContent).toContain("does not report observedGeneration; whether it acknowledged generation 3 is not known");
  });

  it("debug containers are listed as evidence with their profile and state", () => {
    render(<DebugContainersBlock detail={detail({
      ephemeral: [
        { name: "debugger-a", image: "busybox:1.36", target: "app", capsAdded: ["SYS_PTRACE"], state: "terminated", reason: "Error", exitCode: 3 },
        { name: "debugger-b", image: "busybox:1.36", runAsNonRoot: true, state: "running" },
        { name: "debugger-c", image: "busybox:1.36", privileged: true, state: "waiting", reason: "CreateContainerConfigError" },
      ], ephemeralMore: 2,
    })} />);
    expect(screen.getByTestId("pod-debug-containers").textContent).toContain("never change the pod's verdict");
    expect(screen.getByTestId("debug-container-debugger-a").textContent).toContain("exited 3 · adds SYS_PTRACE");
    expect(screen.getByTestId("debug-container-debugger-b").textContent).toContain("running · non-root");
    expect(screen.getByTestId("debug-container-debugger-c").textContent).toContain("waiting: CreateContainerConfigError · privileged");
    expect(screen.getByTestId("pod-debug-containers").textContent).toContain("2 older debug containers not listed");
  });

  it("no debug containers, no block", () => {
    const { container } = render(<DebugContainersBlock detail={detail({ ephemeral: [] })} />);
    expect(container.textContent).toBe("");
  });

  it("the confirmation names the image, the profile, what it adds, and the consequence", () => {
    const t = debugConfirmText("app", "web", "app", { image: "busybox:1.36", profile: "netadmin", adds: "adds NET_ADMIN and NET_RAW", qualified: "profile effects verified for kubectl 1.30-1.37; this is v1.38.0" });
    expect(t).toContain("image busybox:1.36, profile netadmin: adds NET_ADMIN and NET_RAW.");
    expect(t).toContain("verified for kubectl 1.30-1.37");
    expect(t).toContain("increments the pod's generation");
  });
});

// Coverage S11a §6: the pod's extended requests, the DRA-served pairs and
// their claim, and the bounds' omissions.
describe("PodExtendedBlock", () => {
  it("lists each container's extended requests, marks DRA-served pairs and names the claim", () => {
    render(<PodExtendedBlock cluster="c" detail={detail({ extended: { claim: "web-extended-resources-x", omitted: 2, containers: [
      { container: "init", init: true, entries: [{ name: "example.com/x", quantity: "1" }] },
      { container: "app", omitted: 1, entries: [{ name: "nvidia.com/gpu", quantity: "2", servedByDra: true }, { name: "deviceclass.resource.kubernetes.io/gpu.example.com", quantity: "-", servedByDra: true }] },
    ] } })} />);
    const block = screen.getByTestId("pod-extended").textContent ?? "";
    expect(block).toContain("init init: example.com/x 1");
    expect(block).toContain("app: nvidia.com/gpu 2 (served by DRA) · deviceclass.resource.kubernetes.io/gpu.example.com not established (served by DRA) · 1 more not shown");
    expect(block).toContain("2 more extended requests not shown");
    expect(block).toContain("DRA serves the marked requests through the ResourceClaim web-extended-resources-x.");
    // S11b §5: the claim links to the DRA lens, which selects it
    useFleet.setState({ navIntent: null });
    fireEvent.click(screen.getByTestId("claim-link"));
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "reveal", cluster: "c", task: "dra", part: "claims", ref: { namespace: "app", name: "web-extended-resources-x" } });
  });
  it("renders nothing for a pod with no extended request", () => {
    const { container } = render(<PodExtendedBlock cluster="c" detail={detail({ extended: { containers: [] } })} />);
    expect(container.textContent).toBe("");
  });
});
