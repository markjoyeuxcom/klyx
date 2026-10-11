import { describe, it, expect, vi } from "vitest";

// Mock all six services before importing the bridge module.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  WorkloadsService: {
    ListWorkloads: vi.fn(),
  },
  PodsService: {
    ListPods: vi.fn(),
  },
  EventsService: {
    ListEvents: vi.fn(),
  },
  NodesService: {
    ListNodes: vi.fn(),
  },
  HelmService: {
    ListHelmReleases: vi.fn(),
  },
  GitOpsService: {
    GetGitOpsSummary: vi.fn(),
  },
}));

import { summarise, SummariseInput } from "./overview";

// ---- summarise() pure-function tests -------------------------------------------

describe("summarise", () => {
  it("counts workload attention (fault + degraded) from the verdict", () => {
    const input: SummariseInput = {
      workloads: [
        { health: { kind: "fault" } },
        { health: { kind: "degraded" } },
        { health: { kind: "ok", note: "restarted 4m ago" } }, // history only - NOT attention
        { health: { kind: "progressing" } },
        { health: { kind: "paused" } },
        {}, // no verdict: unknown, never counted either way
      ],
      pods: [],
      events: [],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.workloads.attention).toBe(2);
    expect(out.workloads.unknown).toBe(1); // carried, so a surface never reads it as quiet
  });

  it("counts pod attention from the verdict (restarts are history)", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [
        { health: { kind: "fault" } },
        { health: { kind: "ok", note: "restarted 2m ago" } },
        { health: { kind: "ok" } },
      ],
      events: [],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.pods.attention).toBe(1);
  });

  it("counts Warning events only (not Normal)", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [],
      events: [
        { type: "Warning" },
        { type: "Warning" },
        { type: "Normal" },
      ],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.warningEvents).toBe(2);
  });

  it("counts node attention from the verdict; cordoned is paused, not a problem", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [],
      events: [],
      nodes: [
        { health: { kind: "ok" } },
        { health: { kind: "fault" } },    // NotReady
        { health: { kind: "degraded" } }, // pressure
        { health: { kind: "paused" } },   // cordoned - intended
        { health: { kind: "unknown" } },  // Ready not reported - visible, not attention
      ],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.nodes.attention).toBe(2);
    expect(out.nodes.unknown).toBe(1);
  });

  it("returns null failedReleases when helmAvailable=false", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false,
      releases: [{ status: "failed" }],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.failedReleases).toBeNull();
  });

  it("counts failed releases when helmAvailable=true", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: true,
      releases: [{ status: "failed" }, { status: "deployed" }, { status: "failed" }],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.failedReleases).toBe(2);
  });

  it("returns zero counts for empty datasets", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: true, releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.workloads.attention).toBe(0);
    expect(out.pods.attention).toBe(0);
    expect(out.warningEvents).toBe(0);
    expect(out.nodes.attention).toBe(0);
    expect(out.failedReleases).toBe(0);
  });

  it("derives namespace count from the provided set", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: null,
    };
    const ns = new Set(["kube-system", "monitoring", "default"]);
    const out = summarise(input, ns);
    expect(out.namespaces).toBe(3);
  });

  it("passes flux through when present=true", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: { present: true, notReady: 2, suspended: 1 },
    };
    const out = summarise(input, new Set());
    expect(out.flux).toEqual({ present: true, notReady: 2, suspended: 1 });
  });

  it("returns null flux when input flux is null", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.flux).toBeNull();
  });

  it("returns null flux when present=false", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: { present: false, notReady: 0, suspended: 0 },
    };
    const out = summarise(input, new Set());
    expect(out.flux).toBeNull();
  });
});

// ---- fetchOverviewSummary stale-guard test ------------------------------------
