import { describe, it, expect, vi, beforeEach } from "vitest";

const calls: { id: number; args: unknown[] }[] = [];
vi.mock("@wailsio/runtime", () => ({
  Call: {
    ByID: (id: number, ...args: unknown[]) => {
      calls.push({ id, args });
      return Promise.resolve(null);
    },
  },
}));

import { initShellSync, windowTitleFor } from "./shell";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";

const cluster = (name: string, state: string): ClusterDTO => ({
  name, state, reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  env: "", region: "", provider: "", group: "", ageSeconds: 0,
});

beforeEach(() => {
  calls.length = 0;
  useFleet.setState({ clusters: [], route: { name: "fleet" } });
  useFleet.getState().clearActionStatus();
});

describe("windowTitleFor", () => {
  it("names the cluster and section", () => {
    expect(windowTitleFor({ name: "cluster", cluster: "prd-weu", section: "pods" })).toBe("prd-weu · Pods — Klyx");
    expect(windowTitleFor({ name: "fleet" })).toBe("Klyx");
    expect(windowTitleFor({ name: "activity" })).toBe("Activity — Klyx");
  });
});

describe("initShellSync", () => {
  it("retitles on route change and badges on cluster trouble", () => {
    const off = initShellSync();
    useFleet.getState().openCluster("dev-neu");
    useFleet.getState().setClusters([cluster("dev-neu", "Synced"), cluster("prd-weu", "Failed")]);
    off();
    const titles = calls.filter((c) => c.args[0] === "dev-neu · Overview — Klyx");
    expect(titles.length).toBeGreaterThan(0);
    const badges = calls.filter((c) => typeof c.args[0] === "number");
    expect(badges[badges.length - 1].args[0]).toBe(1);
  });

  it("raises an OS notification for a background error, not a focused one", () => {
    const off = initShellSync();
    const hasFocus = vi.spyOn(document, "hasFocus");

    hasFocus.mockReturnValue(true);
    useFleet.getState().setActionStatus({ kind: "error", message: "focused failure" });
    expect(calls.some((c) => c.args[1] === "focused failure")).toBe(false);

    hasFocus.mockReturnValue(false);
    useFleet.getState().setActionStatus({ kind: "error", message: "background failure" });
    expect(calls.some((c) => c.args[1] === "background failure")).toBe(true);

    // successes never notify
    useFleet.getState().setActionStatus({ kind: "success", message: "quiet win" });
    expect(calls.some((c) => c.args[1] === "quiet win")).toBe(false);

    off();
    hasFocus.mockRestore();
  });
});
