import { beforeEach, describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ClusterCard, clusterDiagnosticScore } from "./ClusterCard";
import type { ClusterDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { useHealth, resetHealthStore, applySummary, applyRows, addSub, openStarted, openSucceeded, dueForBootstrap } from "../store/health";
import { core, dep, evidence, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "../nav/clusterAttention";

const base: ClusterDTO = {
  name: "plt-sea-prd-we-aks-01", state: "Synced", reason: "",
  nodesReady: 12, nodesTotal: 12, pods: 487, version: "v1.30.4",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "prd", region: "we", provider: "aks", group: "prd-we", ageSeconds: 15,
};

beforeEach(() => {
  useFleet.setState({ mesh: null, route: { name: "fleet" } });
  resetHealthStore();
});

describe("ClusterCard", () => {
  it("renders name, version and counts", () => {
    const { getByText } = render(<ClusterCard c={base} />);
    expect(getByText("plt-sea-prd-we-aks-01")).toBeTruthy();
    expect(getByText(/v1\.30\.4/)).toBeTruthy(); // provider · version joined in the header
    // counter wells split value and label into separate elements
    expect(getByText("12/12")).toBeTruthy();
    expect(getByText("487")).toBeTruthy();
    expect(getByText("pods")).toBeTruthy();
  });

  it("shows the reason for a failed cluster", () => {
    const { getByText } = render(
      <ClusterCard c={{ ...base, state: "Failed", reason: "connect timed out" }} />,
    );
    expect(getByText(/connect timed out/i)).toBeTruthy();
  });

  it("shows a lock affordance for a protected cluster", () => {
    const { queryByTitle, getByText } = render(<ClusterCard c={{ ...base, protected: true }} />);
    expect(queryByTitle("protected")).toBeTruthy();
    expect(getByText(/prd lock/)).toBeTruthy();
  });

  it("has no lock for an unprotected cluster", () => {
    const { queryByTitle } = render(<ClusterCard c={{ ...base, protected: false }} />);
    expect(queryByTitle("protected")).toBeNull();
  });

  it("contains long EKS ARN context names inside the card", () => {
    const arn = "arn:aws:eks:us-east-1:934692410245:cluster/eks-prod-platform-observability-us-east-1";
    const { getByTestId, getByText, getByTitle } = render(<ClusterCard c={{ ...base, name: arn, provider: "", region: "" }} />);
    const card = getByTestId(`cluster-card-${arn}`);
    expect(card.style.minWidth).toBe("0px");
    expect(card.style.overflow).toBe("hidden");
    expect(getByText("eks-prod-platform-observability-us-east-1")).toBeTruthy();
    expect(getByTitle(arn)).toBeTruthy();
  });
});

it("drills into the cluster on click", () => {
  useFleet.setState({ route: { name: "fleet" } });
  const { getByText } = render(<ClusterCard c={base} />);
  getByText("plt-sea-prd-we-aks-01").click();
  expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "plt-sea-prd-we-aks-01" });
});

it("shows the mesh row from the graph: peered / asymmetric / standalone / off-fleet", () => {
  useFleet.setState({ mesh: {
    nodes: [
      { cluster: "ctx-blue", name: "homelab-blue", clusterId: 1, state: "peered", present: true },
      { cluster: "ctx-nelli", name: "homelab-nelli", clusterId: null, state: "enabled", present: true },
    ],
    edges: [{ a: "ctx-blue", b: "ctx-orange", mutual: true }],
  }});
  // a peered cluster shows its peer
  const blue = { name: "ctx-blue", state: "Ready", networkTier: "Healthy" } as any;
  const { getByText } = render(<ClusterCard c={blue} />);
  expect(getByText("mesh")).toBeTruthy();
  expect(getByText(/ctx-orange peer/)).toBeTruthy();
});

it("shows 'mesh enabled, no peers' for an installed-but-peerless cluster", () => {
  useFleet.setState({ mesh: {
    nodes: [{ cluster: "ctx-nelli", name: "homelab-nelli", clusterId: null, state: "enabled", present: true }],
    edges: [],
  }});
  const nelli = { name: "ctx-nelli", state: "Ready", networkTier: "Healthy" } as any;
  const { getByText } = render(<ClusterCard c={nelli} />);
  expect(getByText("no peers")).toBeTruthy();
});

// Health 1c slice 5 §4: the card's health lines come from the cluster's
// attention model - each area in its own vocabulary, never summed; every
// expected area's coverage named; per-line provenance; nothing green.
describe("ClusterCard health from the cluster's summary", () => {
  const N = base.name;
  const seed = (tasks = core(), over: Parameters<typeof summary>[1] = {}) => applySummary(summary(tasks, over, N));
  const flux = (recFault = 0) => task("flux", [native("reconciliations", { fault: recFault, ok: 4 }, recFault, "not ready", [kind("kustomizations")]), native("sources", { ok: 2 }, 0, "not ready", [kind("gitrepositories")])]);
  const argo = (fault = 0, sets = true) => task("argo", [native("applications", { fault, ok: 5 }, fault, "need attention", [kind("applications")]),
    ...(sets ? [native("applicationsets", { ok: 1 }, 0, "not healthy", [kind("applicationsets")])] : [])]);
  const row = (label: string) => screen.getByText(label).nextElementSibling as HTMLElement;

  it("each tool in its own vocabulary, never summed; quiet is neutral, never green", () => {
    seed([...core(), flux(2), argo(1)]);
    const { container } = render(<ClusterCard c={{ ...base, fluxPresent: true, argoPresent: true }} />);
    expect(row("workloads").textContent).toBe("none need attention");
    expect(row("gitops").textContent).toBe("flux reconciliations 2 not ready · argo applications 1 need attention");
    expect(row("gateway").textContent).toBe("not detected");
    expect(screen.getByText("2 areas need attention")).toBeTruthy(); // areas, never objects
    expect(container.innerHTML).not.toMatch(/color-text-success/);
    expect(screen.getByText(/raised because 2 areas need attention · summary as of /)).toBeTruthy();
  });

  it("every expected area's coverage is named, lit or not: unreadable Certificates beside quiet workloads, unknown NodeClaims, a missing Argo part", () => {
    seed([...core(),
      task("certs", [native("certificates", {}, 0, "not ready", [kind("certificates", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" }), native("issuers", { ok: 1 }, 0, "not ready", [kind("issuers")])]),
      task("karpenter", [native("nodeclaims", { unknown: 1, ok: 3 }, 0, "failing", [kind("nodeclaims")]), native("nodepools", { ok: 1 }, 0, "not ready", [kind("nodepools")])]),
      argo(0, false)]);
    render(<ClusterCard c={{ ...base, certManagerPresent: true, karpenterPresent: true, argoPresent: true }} />);
    expect(row("workloads").textContent).toBe("none need attention");
    const gaps = row("unverified").textContent ?? "";
    expect(gaps).toContain("Certificates: Certificates could not be read (forbidden)");
    expect(gaps).toContain("Karpenter: NodeClaims 1 unknown");
    expect(gaps).toContain("Argo CD: ApplicationSets: not covered by the summary");
    expect(screen.getByText(/health partly unverified/)).toBeTruthy();
  });

  it("'not detected' only for a confirmed absence; an expected tool the summary leaves out is not covered", () => {
    seed(core());
    render(<ClusterCard c={{ ...base, fluxPresent: false, argoPresent: true }} />);
    expect(row("gitops").textContent).toBe("argo not covered");
  });

  it("other areas needing attention are listed by name; evidence never counts", () => {
    seed(replace(replace(core(), task("nodes", [verdict("nodes", { degraded: 1, ok: 2 }, [kind("nodes")])])), task("events", [evidence(40, [kind("events")])])));
    render(<ClusterCard c={base} />);
    expect(row("also").textContent).toBe("Nodes - 1 need attention");
    expect(screen.getByText("1 area needs attention")).toBeTruthy();
  });

  it("checking before anything answers; ended after a Final - never counts", () => {
    const r1 = render(<ClusterCard c={base} />);
    expect(row("health").textContent).toBe("checking…");
    r1.unmount();
    seed();
    seed([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" });
    render(<ClusterCard c={base} />);
    expect(row("health").textContent).toMatch(/^observation ended at /);
  });

  it("a line's title is its own provenance, on both sides of A4 - never a row count with the summary's time", () => {
    seed(core(), { evaluatedAt: "2026-10-12T12:00:00Z" });
    addSub("w", N, "workloads", "workloads"); openStarted("w", "t"); openSucceeded("w", "t", 1);
    applyRows("w", { cluster: N, incarnation: 1, task: "workloads", evaluatedAt: "2026-10-12T12:00:03Z", rows: [], full: true,
      part: { ...verdict("workloads", { fault: 1, ok: 2 }, [kind("deployments")]), ver: 3, obs: 3 } }, 0);
    applySummary(summary(replace(core(), task("workloads", [{ ...verdict("workloads", { fault: 2, ok: 1 }, [kind("deployments")]), ver: 6, obs: 6 }])), { seq: 2 }, N), 100);
    const r1 = render(<ClusterCard c={base} />);
    expect(row("workloads").textContent).toBe("1 need attention"); // the rows, held
    expect(row("workloads").parentElement?.title).toMatch(/Workloads lens rows as of .* - updating/);
    r1.unmount();
    dueForBootstrap(1200);
    render(<ClusterCard c={base} />);
    expect(row("workloads").textContent).toBe("2 need attention"); // the summary's, refreshing
    expect(row("workloads").parentElement?.title).toMatch(/cluster summary as of .* - refreshing/);
  });

  it("an unreachable cluster renders as a ghost stating reason and recovery", () => {
    const down = { ...base, name: "homelab-orange", state: "Failed", reason: "connect timed out", ageSeconds: 7200 };
    const { getByText, queryByText } = render(<ClusterCard c={down} />);
    expect(getByText(/failed · 2h/)).toBeTruthy();
    expect(getByText("connect timed out")).toBeTruthy();
    expect(getByText(/reconnects automatically/)).toBeTruthy();
    expect(queryByText(/nodes/)).toBeNull(); // no fabricated stats
  });

  it("ordering bands come from the model: unreachable, danger, warning, connection, gaps, quiet", () => {
    const score = (name: string, state = "Synced") => clusterDiagnosticScore({ ...base, name, state }, clusterAttention(useHealth.getState(), name, { ...base, name, state }));
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 1 }, [kind("deployments")])])), {}, "d"));
    applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { degraded: 1 }, [kind("nodes")])])), {}, "w"));
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { ok: 1 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])), {}, "g"));
    applySummary(summary(core(), {}, "q")); applySummary(summary(core(), {}, "s"));
    const order = [score("u", "Failed"), score("d"), score("w"), score("s", "Stale"), score("g"), score("q")];
    expect([...order].sort((a, b) => b - a)).toEqual(order);
    expect(new Set(order).size).toBe(order.length);
  });
});

describe("pod count honesty (1c 2d)", () => {
  it("an unknown count is a dash, never zero; a stale count is marked", () => {
    const { unmount } = render(<ClusterCard c={{ ...base, pods: 0, podsKnown: false }} />);
    expect(screen.getByTitle("no complete pod read yet").textContent).toBe("—");
    unmount();
    render(<ClusterCard c={{ ...base, pods: 42, podsKnown: true, podsStale: true }} />);
    expect(screen.getByTitle("last good count - the pod read is failing").textContent).toBe("42");
  });
});
