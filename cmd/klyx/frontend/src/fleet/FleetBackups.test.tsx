import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import type { VeleroFleetHorizonDTO, VeleroScheduleDTO } from "../bridge/velero";
import { useFleet } from "../store/fleet";

let replies: Record<string, VeleroFleetHorizonDTO>;
vi.mock("../bridge/velero", () => ({ getBackupHorizon: async (c: string) => replies[c] }));
const { FleetBackups, coverageNote } = await import("./FleetBackups");

const sched = (name: string, role: string, at?: string): VeleroScheduleDTO => ({
  uid: `u-${name}`, gvr: "velero.io/v1/schedules", namespace: "velero", name, generation: 1, cron: "0 2 * * *", paused: false, phase: "Enabled",
  useOwnerReferences: false, validation: [], runs: [], newer: [], synced: 0,
  template: { volumeSnapshotLocations: [], includedNamespaces: [], includedCount: 0, excludedNamespaces: [], excludedCount: 0, hooks: 0 },
  location: { state: "found", name: "default" }, rule: { included: role === "fault", role, label: role === "fault" ? "Missed" : "Completed" },
  horizon: at ? { state: "found", at, proven: true, text: "latest proven retained success" } : { state: "none", proven: false, text: "no successful backup retained" },
});
const live = (kind: string, over = {}) => ({ kind, served: true, synced: true, coverage: "live" as const, ...over });

describe("FleetBackups (coverage S2b §2)", () => {
  beforeEach(() => {
    useFleet.setState({ clusters: [{ name: "a", env: "dev", region: "weu" }, { name: "b", env: "prd", region: "weu" }, { name: "c", env: "stg", region: "neu" }, { name: "d", env: "stg", region: "neu" }] as never });
    const old = new Date(Date.now() - 5 * 86400_000).toISOString();
    const recent = new Date(Date.now() - 3600_000).toISOString();
    replies = {
      a: { cluster: "a", state: "read", asOf: recent, kinds: [live("schedules"), live("backups", { truncated: true })], schedules: [sched("daily", "ok", recent), sched("weekly", "ok", old)] },
      b: { cluster: "b", state: "read", asOf: recent, kinds: [live("schedules"), live("backups")], schedules: [sched("hourly", "fault", recent), sched("never", "ok")] },
      c: { cluster: "c", state: "no-velero", kinds: [], schedules: [] },
      d: { cluster: "d", state: "no-observer", kinds: [], schedules: [] },
    };
  });

  it("orders broken first, then the longest without a success, and names what it could not read", async () => {
    render(<FleetBackups />);
    await waitFor(() => expect(document.querySelectorAll(".lens-row").length).toBe(4));
    const order = [...document.querySelectorAll(".lens-row")].map((r) => r.getAttribute("data-testid"));
    expect(order).toEqual(["fleet-backups-row-b-hourly", "fleet-backups-row-b-never", "fleet-backups-row-a-weekly", "fleet-backups-row-a-daily"]);
    const cov = screen.getByTestId("fleet-backups-coverage").textContent ?? "";
    expect(cov).toContain("a: backups capped");
    expect(cov).toContain("c: no Velero");
    expect(cov).toContain("d: not read");
    expect(screen.getByTestId("fleet-backups-row-b-never").textContent).toContain("none retained");
  });

  it("coverageNote reads each kind's own coverage", () => {
    expect(coverageNote({ cluster: "x", state: "read", kinds: [live("schedules", { coverage: "stale" }), live("backups", { coverage: "unreadable" })], schedules: [] }))
      .toBe("schedules as of the last good read · backups unreadable");
    expect(coverageNote({ cluster: "x", state: "read", kinds: [live("schedules"), live("backups")], schedules: [] })).toBe("");
  });

  it("review: unreadable history is never counted as no retained success", async () => {
    const s = sched("unread", "unknown");
    s.horizon = { state: "unreadable", proven: false, text: "runs not readable" };
    const capped = sched("capped", "ok");
    capped.horizon = { state: "none", proven: false, incomplete: true, window: new Date().toISOString(), text: "no successful backup retained since …" };
    replies.a = { cluster: "a", state: "read", kinds: [live("schedules"), live("backups", { coverage: "unreadable" })], schedules: [s, capped] };
    for (const c of ["b", "c", "d"]) replies[c] = { cluster: c, state: "no-velero", kinds: [], schedules: [] };
    render(<FleetBackups />);
    await waitFor(() => expect(screen.getByTestId("fleet-backups-row-a-unread")).toBeTruthy());
    expect(screen.getByTestId("fleet-backups-row-a-unread").textContent).toContain("runs unknown");
    expect(screen.queryByText(/with no retained success/)).toBeNull();
    expect(screen.getByText("1 with runs unknown")).toBeTruthy();
    expect(screen.getByText("1 with none in the history Klyx holds")).toBeTruthy();
    expect(screen.getByTestId("fleet-backups-row-a-capped").textContent).toMatch(/none since/);
  });
});
