import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within, waitFor, act } from "@testing-library/react";
import type { VeleroPartRows } from "../health/useVeleroRows";
import type { VeleroBackupDTO, VeleroLocationDTO, VeleroRestoresDTO, VeleroScheduleDTO } from "../bridge/velero";
import { useFleet } from "../store/fleet";
import { VeleroView, phaseKind, horizonCell } from "./VeleroView";

let restoresReply: (cluster: string, readId: string) => Promise<VeleroRestoresDTO>;
vi.mock("../bridge/velero", () => ({ getVeleroRestores: (c: string, id: string) => restoresReply(c, id) }));

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[]): VeleroPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null, facts: null });
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
  location: { state: "found", name: "default", byDefault: true, label: "Available", role: "ok" }, rule: rule("ok", "Completed"),
  horizon: { state: "none", proven: false, text: "no successful backup retained" }, ...over,
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
      validation: ["the storage location is Unavailable"], validationCount: 1, membership: "unattributed", storageLocation: "dr", counted: true,
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
      validation: [], validationCount: 0, membership: "unattributed", counted: true, rule: rule("fault", "Failed") }], ["Backup"]);
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

  // ---- coverage S2b ----------------------------------------------------------

  it("the Schedules column and inspector carry the horizon with its confidence", () => {
    const at = new Date(Date.now() - 3.5 * 3600_000).toISOString();
    const success = { ...run("daily-a", "Completed", at), completed: at };
    schedules = part([
      sched("daily", { horizon: { state: "found", success, at, proven: true, text: "latest proven retained success, completed X", partialText: "a newer run partially failed", partial: run("daily-p", "PartiallyFailed", at) } }),
      sched("named", { horizon: { state: "found", success, at, proven: false, incomplete: true, text: "a Completed record matched by name (not proven to be this schedule's), completed X; a run …" } }),
      sched("bare"),
    ], ["Schedule", "Backup", "BackupStorageLocation"]);
    render(<VeleroView cluster="c" />);
    expect(screen.getByTestId("velero-success-daily").textContent).toBe("3h ago · partial");
    expect(screen.getByTestId("velero-success-named").textContent).toBe("≤ 3h ago · by name");
    expect(screen.getByTestId("velero-success-bare").textContent).toBe("none retained");
    expect(screen.getByTestId("velero-success-named").getAttribute("title")).toContain("matched by name");
    fireEvent.click(screen.getByText("daily"));
    const block = screen.getByTestId("velero-horizon");
    expect(block.textContent).toContain("latest proven retained success");
    expect(screen.getByTestId("velero-schedule-panel").textContent).toContain("a newer run partially failed");
    expect(block.textContent).not.toMatch(/restorable/i);
  });

  it("horizonCell never drops a qualifier", () => {
    const nowS = Math.floor(Date.now() / 1000);
    const at = new Date((nowS - 600) * 1000).toISOString();
    expect(horizonCell({ state: "found", at, proven: true, orderUnknown: true, text: "t" }, nowS).text).toBe("≤ 10m ago");
    expect(horizonCell({ state: "found", at, proven: true, untimed: true, text: "t" }, nowS).text).toBe("10m ago · started");
    expect(horizonCell({ state: "none", proven: false, incomplete: true, tier0: true, text: "t" }, nowS).text).toBe("none held");
    expect(horizonCell({ state: "none", proven: false, incomplete: true, window: at, text: "t" }, nowS).text).toMatch(/^none since /);
    expect(horizonCell({ state: "unreadable", proven: false, text: "runs not readable" }, nowS).text).toBe("runs unknown");
  });

  const bk = (name: string, phase: string, over: Partial<VeleroBackupDTO> = {}): VeleroBackupDTO => ({ uid: `uid-${name}`, gvr: "velero.io/v1/backups", namespace: "velero", name, phase,
    errors: 0, warnings: 0, validation: [], validationCount: 0, membership: "proven", counted: false, ...over });

  it("the Backups tab lists every retained backup as a fact, with filters and notices", () => {
    backups = { ...part([
      bk("daily-1", "Completed", { schedule: "daily", attribution: "proven", started: new Date(Date.now() - 7200_000).toISOString() }),
      bk("daily-2", "PartiallyFailed", { schedule: "daily", attribution: "named", started: new Date(Date.now() - 3600_000).toISOString() }),
      bk("old", "Completed", { schedule: "daily", attribution: "synced", expired: true, gcFailure: "BSLReadOnly" }),
      bk("pre-upgrade", "Failed", { membership: "unattributed", counted: true, rule: rule("fault", "Failed"), reason: "Failed: the backup failed while running" }),
    ], ["Backup", "Schedule"]), coverage: [{ ...live("Backup"), truncated: true, notRetained: 3 }], facts: { veleroWindow: "2026-10-17T01:00:00Z", veleroTier0: false } };
    render(<VeleroView cluster="c" />);
    // the unscheduled tab keeps S2a's counted rows only
    fireEvent.click(tab(/^Unscheduled backups/));
    expect(rowTexts().length).toBe(1);
    fireEvent.click(tab(/^Backups/));
    expect(rowTexts().length).toBe(4);
    expect(screen.getByTestId("velero-retained-note").textContent).toContain("these rows are facts, not verdicts");
    expect(screen.getByTestId("velero-capped").textContent).toContain("Klyx holds the runs that started since");
    expect(rowTexts().find((t) => t.includes("old"))).toContain("expired, awaiting deletion");
    // filters: phase group and schedule, with a clear action
    fireEvent.click(within(screen.getByRole("group", { name: "Phase" })).getByRole("button", { name: "Partially failed" }));
    expect(rowTexts().length).toBe(1);
    fireEvent.click(within(screen.getByRole("group", { name: "Phase" })).getByRole("button", { name: "Completed" }));
    fireEvent.change(screen.getByRole("combobox", { name: "Schedule" }), { target: { value: "-" } });
    expect(screen.getByText("No retained backup matches these filters.")).toBeTruthy();
    fireEvent.click(screen.getByText("Clear filters"));
    expect(rowTexts().length).toBe(4);
    // a listed fact has no verdict
    fireEvent.click(screen.getByText("daily-2"));
    const panel = screen.getByTestId("velero-backup-panel");
    expect(within(panel).getByTestId("velero-backup-fact").textContent).toContain("record matched by name of daily");
    expect(within(panel).getByTestId("velero-backup-fact").textContent).toContain("Velero's phase");
  });

  it("a tier-0 window says finished runs may be missing", () => {
    backups = { ...part([bk("x", "InProgress")], ["Backup"]), coverage: [{ ...live("Backup"), truncated: true }], facts: { veleroTier0: true } };
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Backups/));
    expect(screen.getByTestId("velero-capped").textContent).toContain("finished runs may be missing");
  });

  const restoresOf = (cluster: string, readId: string, over: Partial<VeleroRestoresDTO> = {}): VeleroRestoresDTO => ({ cluster, readId, state: "read", cap: 500, backups: "capped",
    restores: [{ uid: "r1", namespace: "velero", name: "drill", backupName: "daily-1", phase: "Failed", errors: 2, warnings: 0, hooks: 1,
      includedNamespaces: ["apps"], includedCount: 1, excludedNamespaces: [], excludedCount: 0, mapping: [{ from: "apps", to: "apps-drill" }], mappingCount: 3,
      failure: "the restore failed while running", validation: [], validationCount: 0,
      source: { state: "not-retained", text: "not in the retained window: a capped read never proves absence" } }], ...over });

  it("the Restores tab reads on open, says its cap, and its inspector joins the source under the Backups coverage", async () => {
    restoresReply = async (c, id) => restoresOf(c, id, { more: true, remaining: 12 });
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Restores/));
    await waitFor(() => expect(rowTexts().length).toBe(1));
    expect(screen.getByTestId("velero-restores-note").textContent).toContain("+12 more not shown");
    expect(screen.getByTestId("velero-restores-note").textContent).toContain("no verdict");
    fireEvent.click(screen.getByText("drill"));
    const panel = screen.getByTestId("velero-restore-panel");
    expect(within(panel).getByTestId("velero-restore-source").textContent).toContain("a capped read never proves absence");
    expect(panel.textContent).toContain("apps → apps-drill +2");
    expect(panel.textContent).toContain("1 hook (commands withheld)");
  });

  it("a Restores reply for an older read or another cluster is dropped", async () => {
    let release: (() => void) | null = null;
    let calls = 0;
    restoresReply = (c, id) => {
      calls++;
      if (calls === 1) return new Promise((res) => { release = () => res(restoresOf(c, id, { restores: [] })); });
      return Promise.resolve(restoresOf(c, id));
    };
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Restores/));
    await waitFor(() => expect(calls).toBe(1));
    // a stale reply from another cluster never lands
    restoresReply = async (_c, id) => restoresOf("other", id);
    await act(async () => { release?.(); });
    expect(rowTexts().length).toBe(0);
  });

  it("an absent or forbidden Restores API is said, never empty", async () => {
    restoresReply = async (c, id) => ({ ...restoresOf(c, id), state: "forbidden", restores: [] });
    const { unmount } = render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Restores/));
    await waitFor(() => expect(screen.getByText("Could not read the restores.")).toBeTruthy());
    unmount();
    restoresReply = async (c, id) => ({ ...restoresOf(c, id), state: "not-served", restores: [] });
    render(<VeleroView cluster="c" />);
    fireEvent.click(tab(/^Restores/));
    await waitFor(() => expect(screen.getByText("Velero is not installed on this cluster.")).toBeTruthy());
  });
});
