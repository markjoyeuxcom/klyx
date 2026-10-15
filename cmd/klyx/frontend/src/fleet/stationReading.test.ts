import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { core, dep, kind, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "../nav/clusterAttention";
import { stationReading } from "./stationReading";

// The Line slice 5 §3.2: the cell's reading - areas never objects, never an
// all-clear on missing or partial evidence, the connection named apart.
const cl = (name: string, state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", ...over } as ClusterDTO);
const read = (c: ClusterDTO) => stationReading(c, clusterAttention(useHealth.getState(), c.name, c));

beforeEach(() => resetHealthStore());

describe("stationReading", () => {
  it("areas needing attention, with the danger tone and the gaps beside them", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 1, ok: 4 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])), {}, "a"));
    expect(read(cl("a"))).toEqual({ text: "1 area needs attention · 1 unverified", tone: "danger", unreachable: false });
  });

  it("nothing raised only on a full quiet read over a synced connection; gaps say partly unverified", () => {
    applySummary(summary(core(), {}, "q"));
    expect(read(cl("q")).text).toBe("nothing raised");
    expect(read(cl("q", "Degraded")).text).toBe("nothing raised · connected with notes");
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { ok: 3 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])), {}, "g"));
    expect(read(cl("g")).text).toBe("health partly unverified");
  });

  it("checking, ended and unreachable never read as quiet", () => {
    expect(read(cl("n")).text).toBe("health checking…");
    expect(read(cl("c", "Connecting")).text).toBe("health checking… · connecting");
    applySummary(summary(core(), {}, "e"));
    applySummary(summary([], { seq: 2, final: true }, "e"));
    expect(read(cl("e")).text).toMatch(/^observation ended at /);
    expect(read(cl("x", "Failed", { reason: "dial tcp" }))).toEqual({ text: "failed: dial tcp", tone: undefined, unreachable: true });
  });
});
