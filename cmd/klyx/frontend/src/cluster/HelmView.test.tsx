import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, within, screen, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { HelmReleaseDTO, HelmHistoryEntryDTO } from "../store/fleet";
import { HelmView, releaseHealth, PENDING_DEADLINE_S } from "./HelmView";

vi.mock("../bridge/helm", () => ({
  HELM_CLI_MISSING: "helm not found",
  listHelmReleases: vi.fn().mockResolvedValue(undefined),
  openHelmRelease: vi.fn().mockResolvedValue(undefined),
  helmRollback: vi.fn().mockResolvedValue(undefined),
}));
import { openHelmRelease, helmRollback } from "../bridge/helm";
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));

const nowS = Math.floor(Date.now() / 1000);
const rel = (over: Partial<HelmReleaseDTO>): HelmReleaseDTO => ({
  name: "web", namespace: "apps", chart: "web-1.2.0", appVersion: "1.2", status: "deployed", revision: 3, updatedUnix: nowS - 600, ...over,
});
const hist = (revision: number, status: string, description = ""): HelmHistoryEntryDTO => ({
  revision, status, chart: `web-1.${revision}.0`, appVersion: `1.${revision}`, description, updatedUnix: nowS - 1000 * (10 - revision),
});

function seed(releases: HelmReleaseDTO[], over: Record<string, unknown> = {}) {
  useFleet.setState((s) => ({ helm: { ...s.helm, cluster: "x", releases, available: true, message: "", loading: false, snapshotAt: 1000, error: null, ...over } }));
}
function select(r: HelmReleaseDTO, history: HelmHistoryEntryDTO[] = [], extra: Record<string, unknown> = {}) {
  useFleet.setState((s) => ({ helm: { ...s.helm, selected: { namespace: r.namespace, name: r.name }, history, values: "", detailLoading: false, ...extra } }));
}
const insp = () => within(screen.getByTestId("lens-inspector"));
const rowsText = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("releaseHealth", () => {
  it("maps helm's vocabulary; pending past the deadline is stuck; historical never faults", () => {
    expect(releaseHealth(rel({}), nowS).kind).toBe("ok");
    expect(releaseHealth(rel({ status: "failed" }), nowS).kind).toBe("fault");
    expect(releaseHealth(rel({ status: "pending-upgrade", updatedUnix: nowS - 60 }), nowS)).toEqual({ kind: "progressing", label: "pending-upgrade" });
    expect(releaseHealth(rel({ status: "pending-upgrade", updatedUnix: nowS - PENDING_DEADLINE_S - 1 }), nowS)).toEqual({ kind: "degraded", label: "stuck pending-upgrade" });
    expect(releaseHealth(rel({ status: "pending-install", updatedUnix: 0 }), nowS).kind).toBe("progressing");
    expect(releaseHealth(rel({ status: "superseded" }), nowS).kind).toBe("unknown");
    expect(releaseHealth(rel({ status: "uninstalled" }), nowS).kind).toBe("unknown");
  });
});

describe("HelmView (redesign step 5)", () => {
  beforeEach(() => { useFleet.getState().clearHelm(); vi.clearAllMocks(); });

  it("the required empty gate: a complete snapshot with no releases says what was searched, offers no install", () => {
    seed([]);
    render(<HelmView cluster="x" />);
    expect(screen.getByText("No Helm releases on this cluster.")).toBeTruthy();
    expect(screen.getByText(/across all namespaces found no deployed, failed, pending or uninstalling release/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /install/i })).toBeNull();
  });

  it("a missing CLI and a failed first read are distinct unreadable states, never an empty list", () => {
    seed([], { available: false, message: "helm not found", snapshotAt: null });
    const first = render(<HelmView cluster="x" />);
    expect(screen.getByText("helm CLI not found.")).toBeTruthy();
    expect(screen.queryByText("No Helm releases on this cluster.")).toBeNull();
    first.unmount();
    seed([], { available: false, message: "secrets is forbidden", error: "secrets is forbidden", snapshotAt: null });
    render(<HelmView cluster="x" />);
    expect(screen.getByText("Could not read Helm releases.")).toBeTruthy();
    expect(screen.getByText("secrets is forbidden")).toBeTruthy();
    expect(screen.queryByText("No Helm releases on this cluster.")).toBeNull();
  });

  it("before the first snapshot it is loading, never empty", () => {
    seed([], { snapshotAt: null, loading: true });
    render(<HelmView cluster="x" />);
    expect(screen.queryByText("No Helm releases on this cluster.")).toBeNull();
  });

  it("a failed re-read keeps the last snapshot: '*' count, banner, no unqualified claim", () => {
    seed([rel({})], { error: "context deadline exceeded" });
    render(<HelmView cluster="x" />);
    expect(screen.getByText("1*")).toBeTruthy();
    expect(screen.getByText(/showing the last good snapshot/)).toBeTruthy();
    expect(screen.getByText("none known failed (last snapshot)")).toBeTruthy();
    expect(screen.queryByText("all deployed")).toBeNull();
  });

  it("broken first: failed, then stuck pending, then in progress, then deployed; header counts match", () => {
    seed([
      rel({ name: "ok" }),
      rel({ name: "fresh", status: "pending-upgrade", updatedUnix: nowS - 30 }),
      rel({ name: "stuck", status: "pending-upgrade", updatedUnix: nowS - 3600 }),
      rel({ name: "bad", status: "failed" }),
    ]);
    render(<HelmView cluster="x" />);
    const rows = rowsText();
    expect(rows[0]).toContain("bad");
    expect(rows[1]).toContain("stuck pending-upgrade");
    expect(rows[2]).toContain("fresh");
    expect(rows[3]).toContain("ok");
    expect(screen.getByText("▲ 1 failed · 1 stuck pending")).toBeTruthy();
    expect(screen.getByText("1 in progress")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Pending/ }));
    expect(rowsText().length).toBe(2);
  });

  it("auto-selects the first release needing attention once, from a fresh snapshot only", () => {
    seed([rel({ name: "ok" }), rel({ name: "bad", status: "failed" })]);
    render(<HelmView cluster="x" />);
    expect(openHelmRelease).not.toHaveBeenCalled(); // cached snapshot at entry
    act(() => seed([rel({ name: "ok" }), rel({ name: "bad", status: "failed" })], { snapshotAt: 2000 }));
    expect(openHelmRelease).toHaveBeenCalledWith("x", "apps", "bad");
  });

  it("a failed release's verdict carries helm's own account; earlier failures stay muted history", () => {
    const r = rel({ status: "failed", revision: 4 });
    seed([r]);
    select(r, [hist(4, "failed", 'Upgrade "web" failed: context deadline exceeded'), hist(3, "superseded", "Upgrade complete"), hist(2, "failed", "old failure")]);
    render(<HelmView cluster="x" />);
    expect(insp().getAllByText('Upgrade "web" failed: context deadline exceeded').length).toBeGreaterThan(0);
    const oldRow = insp().getByText("old failure").closest("li") as HTMLElement;
    const oldStatus = within(oldRow).getByText("failed");
    expect(oldStatus.style.color).toBe("var(--color-text-tertiary)");
    const currentStatus = within(insp().getByText("rev 4").closest("li") as HTMLElement).getByText("failed · current");
    expect(currentStatus.style.color).toBe("var(--color-text-danger)");
  });

  it("a stuck release explains the deadline and the way out in words", () => {
    const r = rel({ status: "pending-upgrade", updatedUnix: nowS - 3600 });
    seed([r]);
    select(r, [hist(3, "pending-upgrade", "Preparing upgrade")]);
    render(<HelmView cluster="x" />);
    expect(insp().getByText(/pending-upgrade for \d+[mh] - longer than 10m/)).toBeTruthy();
    expect(insp().getByText(/Helm refuses new operations/)).toBeTruthy();
  });

  it("history and values read failures are shown, never as empty", () => {
    const r = rel({});
    seed([r]);
    select(r, [], { historyError: "release: not found", valuesError: "forbidden" });
    render(<HelmView cluster="x" />);
    expect(insp().getByText("History unreadable: release: not found")).toBeTruthy();
    expect(insp().getByText("Values unreadable: forbidden")).toBeTruthy();
    expect(insp().queryByText("No user-supplied values.")).toBeNull();
  });

  it("secondary rolls back to the last GOOD revision (skips failed); confirm is pinned to the release", () => {
    const r = rel({ status: "failed", revision: 5 });
    seed([r]);
    select(r, [hist(5, "failed"), hist(4, "failed"), hist(3, "superseded"), hist(2, "superseded")]);
    render(<HelmView cluster="x" />);
    fireEvent.click(insp().getByRole("button", { name: "Roll back to rev 3…" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back" }));
    expect(helmRollback).toHaveBeenCalledWith("x", "apps", "web", 3, { wait: true, noHooks: false, timeoutSeconds: 120 });
  });

  it("a release missing from a complete snapshot is removed, read-only; from a failed re-read it is not", () => {
    const r = rel({ name: "gone", status: "failed" });
    seed([r, rel({ name: "other" })]);
    select(r, [hist(3, "failed", "boom")]);
    render(<HelmView cluster="x" />);
    act(() => seed([rel({ name: "other" })], { error: "timeout" }));
    expect(screen.queryByText("Release removed.")).toBeNull();
    act(() => seed([rel({ name: "other" })], { error: null, snapshotAt: 3000 }));
    expect(insp().getByText("Release removed.")).toBeTruthy();
    expect(insp().queryByRole("button", { name: /Roll back|Re-read/ })).toBeNull();
  });

  it("direct navigation to a release the filters hide clears them once", () => {
    const ok = rel({ name: "ok" });
    seed([ok, rel({ name: "bad", status: "failed" })]);
    render(<HelmView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^Failed/ }));
    expect(rowsText().join(" ")).not.toContain("ok");
    act(() => select(ok));
    expect(rowsText().join(" ")).toContain("ok");
  });
});
