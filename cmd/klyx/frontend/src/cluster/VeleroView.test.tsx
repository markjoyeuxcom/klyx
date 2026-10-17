import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import type { VeleroPartRows } from "../health/useVeleroRows";
import type { VeleroBackupDTO, VeleroLocationDTO, VeleroScheduleDTO } from "../bridge/velero";
import { useFleet } from "../store/fleet";
import { VeleroView, phaseKind } from "./VeleroView";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[]): VeleroPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null });
let schedules: VeleroPartRows<VeleroScheduleDTO>;
let locations: VeleroPartRows<VeleroLocationDTO>;
let backups: VeleroPartRows<VeleroBackupDTO>;
vi.mock("../health/useVeleroRows", () => ({
  useVeleroSchedules: () => schedules, useVeleroLocations: () => locations, useVeleroBackups: () => backups,
}));

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
const sched = (name: string, over: Partial<VeleroScheduleDTO> = {}): VeleroScheduleDTO => ({
  uid: `uid-${name}`, gvr: "velero.io/v1/schedules", namespace: "velero", name, generation: 1, cron: "0 2 * * *", zone: "UTC", library: "go-cron",
  paused: false, phase: "Enabled", useOwnerReferences: false, due: "2026-10-18T02:00:00Z", validation: [], runs: [], newer: [], synced: 0,
  template: { volumeSnapshotLocations: [], includedNamespaces: [], includedCount: 0, excludedNamespaces: [], excludedCount: 0, hooks: 0 },
  location: { state: "found", name: "default", byDefault: true, label: "Available", role: "ok" }, rule: rule("ok", "Completed"), ...over,
});
const run = (name: string, phase: string, at: string) => ({ uid: `uid-${name}`, name, phase, at, proven: true, timing: "local" as const });
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "Velero objects" })).getByRole("button", { name });
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("VeleroView (coverage S2a)", () => {
  beforeEach(() => {
    schedules = part([], ["Schedule", "Backup", "BackupStorageLocation"]);
    locations = part([], ["BackupStorageLocation", "Schedule", "Backup"]);
    backups = part([], ["Backup", "Schedule"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", veleroPresent: true } as never] });
  });

  it("schedules are broken first; the inspector leads with the verdict, the rail and a separate location block", () => {
    schedules = part([
      sched("daily", { runs: [run("daily-20261016020005", "Completed", "2026-10-16T02:00:05Z")] }),
      sched("hourly", { rule: rule("fault", "Missed"), reason: "Missed: Velero created no backup for the run due at 2026-10-17 02:00:00Z",
        runs: [run("hourly-20261017010000", "Failed", "2026-10-17T01:00:00Z")], location: { state: "found", name: "dr", label: "Unavailable", role: "fault" } }),
    ], ["Schedule", "Backup", "BackupStorageLocation"]);
    render(<VeleroView cluster="c" />);
    expect(rowTexts()[0]).toContain("hourly");
    expect(rowTexts()[0]).toContain("Missed · Velero created no backup");
    fireEvent.click(screen.getByText("hourly"));
    const panel = screen.getByTestId("velero-schedule-panel");
    expect(within(panel).getByTestId("velero-owed").textContent).toContain("missed");
    expect(within(panel).getByTestId("velero-rail").textContent).toContain("Failed");
    expect(within(panel).getByTestId("velero-location-block").textContent).toContain("dr");
    expect(within(panel).getByTestId("velero-location-block").textContent).toContain("Unavailable");
    expect(panel.textContent).toContain("Velero records no Kubernetes events for schedules.");
    expect(within(panel).getByTestId("velero-proof").textContent).toContain("Only the run Velero last created is proven");
  });

  it("storage locations say a withheld message, a Secret by name only, and the schedules that target them", () => {
    locations = part([{ uid: "uid-dr", gvr: "velero.io/v1/backupstoragelocations", namespace: "velero", name: "dr", default: false, phase: "Unavailable",
      provider: "aws", bucket: "dr", credential: "cloud-credentials", credentialKey: "cloud", message: "Velero's message is withheld: provider errors can carry credentials; the provider reported AccessDenied",
      messageWithheld: true, configKeys: ["region", "s3Url"], configCount: 2, region: "eu-west-1", s3Url: "https://minio:9000",
      schedules: [{ namespace: "velero", name: "hourly", label: "Missed", role: "fault" }], schedulesState: "found", expired: 2, expiredState: "found",
      gcFailures: { BSLCannotGet: 2 }, reason: "Unavailable: Velero's message is withheld: provider errors can carry credentials; the provider reported AccessDenied",
      rule: rule("fault", "Unavailable") }], ["BackupStorageLocation", "Schedule", "Backup"]);
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Storage locations/));
    fireEvent.click(screen.getByText("dr"));
    const panel = screen.getByTestId("velero-location-panel");
    expect(within(panel).getByTestId("velero-message-withheld")).toBeTruthy();
    expect(panel.textContent).toContain("Secret cloud-credentials key cloud (named only, never read)");
    expect(panel.textContent).toContain("region, s3Url (values not shown)");
    expect(panel.textContent).toContain("2 expired backups await deletion: 2 BSLCannotGet");
    expect(panel.textContent).toContain("hourly");
  });

  it("unscheduled backups say why they are counted", () => {
    backups = part([{ uid: "uid-m", gvr: "velero.io/v1/backups", namespace: "velero", name: "pre-upgrade", phase: "FailedValidation", errors: 0, warnings: 0,
      validation: ["the storage location is Unavailable"], validationCount: 1, membership: "unattributed", storageLocation: "dr",
      reason: "FailedValidation: the storage location is Unavailable", rule: rule("fault", "FailedValidation") }], ["Backup", "Schedule"]);
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Unscheduled backups/));
    fireEvent.click(screen.getByText("pre-upgrade"));
    const panel = screen.getByTestId("velero-backup-panel");
    expect(panel.textContent).toContain("not a run of any current schedule");
    expect(panel.textContent).toContain("the storage location is Unavailable");
    expect(panel.textContent).toContain("which Klyx does not read");
  });

  it("an absent install says so", () => {
    schedules = { ...part([], []), coverage: [{ kind: "Schedule", served: false, synced: false, coverage: "absent" }] };
    render(<VeleroView cluster="c" />);
    expect(screen.getByText("Velero is not installed on this cluster.")).toBeTruthy();
  });

  // Codex's S2a implementation review, finding 4: absence from a capped or
  // filtered view is never proof of deletion.
  it("an object left out by the cap is unverified rather than removed", () => {
    schedules = part([sched("daily")], ["Schedule"]);
    const { rerender } = render(<VeleroView cluster="c" />);
    fireEvent.click(screen.getByText("daily"));
    schedules = { ...part<VeleroScheduleDTO>([], ["Schedule"]), coverage: [{ ...live("Schedule"), truncated: true, notRetained: 1 }] };
    rerender(<VeleroView cluster="c" />);
    expect(screen.queryByText("Schedule removed.")).toBeNull();
    expect(screen.getByText("Not in the current Schedule read.")).toBeTruthy();
  });

  it("leaving the recent-backups set does not prove deletion", () => {
    backups = part([{ uid: "uid-m", gvr: "velero.io/v1/backups", namespace: "velero", name: "pre-upgrade", phase: "Failed", errors: 1, warnings: 0,
      validation: [], validationCount: 0, membership: "unattributed", rule: rule("fault", "Failed") }], ["Backup"]);
    const { rerender } = render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Unscheduled backups/));
    fireEvent.click(screen.getByText("pre-upgrade"));
    // still held by the Observer, but aged out of the part's 24 hours
    backups = part([], ["Backup"]);
    rerender(<VeleroView cluster="c" />);
    expect(screen.queryByText("Backup removed.")).toBeNull();
    expect(screen.getByText("No longer in this list.")).toBeTruthy();
  });

  it("phases keep Velero's vocabulary as marks", () => {
    expect(["Completed", "PartiallyFailed", "Failed", "FailedValidation", "InProgress", "Queued"].map(phaseKind))
      .toEqual(["ok", "degraded", "fault", "fault", "progressing", "progressing"]);
  });
});
