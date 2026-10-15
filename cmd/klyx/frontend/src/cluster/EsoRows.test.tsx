import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, fireEvent, within } from "@testing-library/react";

// The External Secrets lens mounted on the REAL rows adapter
// (health/useExternalSecretsRows) and health store - only the transport is
// stubbed (slice 3 §5.2, held to the Certificates migration's review).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
import { ExternalSecretsView } from "./ExternalSecretsView";
import { _resetExternalSecretsRowsForTest } from "../health/useExternalSecretsRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, openFailed, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type ClusterDTO, type ExternalSecretDTO, type ExternalSecretStoreDTO } from "../store/fleet";
import { secretRule, storeRule, withRule } from "../test/backendRules";
import { clock } from "../layout/coverage";

const secret = (name: string, over: Partial<ExternalSecretDTO> = {}): ExternalSecretDTO => withRule<ExternalSecretDTO>({
  namespace: "payments", name, state: "Synced", reason: "", message: "", storeKind: "ClusterSecretStore", storeName: "vault",
  targetSecret: `${name}-secret`, refreshIntervalSeconds: 3600, lastRefreshAgeSeconds: 60, refreshed: true, uid: `uid-${name}`, ...over,
}, secretRule);
const store = (name: string, over: Partial<ExternalSecretStoreDTO> = {}): ExternalSecretStoreDTO => withRule<ExternalSecretStoreDTO>({
  kind: "ClusterSecretStore", namespace: "", name, state: "Synced", reason: "", message: "", provider: "vault", ...over,
}, storeRule);

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const SECRET_KINDS = () => [kind("externalsecrets")];
const STORE_KINDS = () => [kind("secretstores"), kind("clustersecretstores")];
const part = (id: string, ver: number, kinds: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, id: string, ver: number, rows: { rule: { included: boolean } }[], kinds: KindCoverage[], evaluatedAt = "2026-10-11T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "eso", part: part(id, ver, kinds, { total: rows.length, included: rows.filter((r) => r.rule.included).length }), evaluatedAt, rows,
});
const summary = (incarnation: number, seq: number, secrets: [number, number, number], stores: [number, number, number]): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "eso", coverage: "live", parts: [part("externalsecrets", secrets[0], SECRET_KINDS(), { total: secrets[1], included: secrets[2] }), part("stores", stores[0], STORE_KINDS(), { total: stores[1], included: stores[2] })] }],
});

const clusterDTO = { name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", externalSecretsTier: "Healthy",
  env: "", region: "", provider: "", group: "", ageSeconds: 0 } as ClusterDTO;

// Two subscriptions stand in for the transport's opens (one per part).
function open(incarnation = 1) {
  for (const [h, id] of [["s", "externalsecrets"], ["t", "stores"]] as const) {
    if (!useHealth.getState().subs[h]) addSub(h, "x", "eso", id);
    openStarted(h, `tok-${h}-${incarnation}`);
    openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
  }
}

const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const chip = (re: RegExp) => screen.getByRole("button", { name: re }).textContent;
const row = (name: string) => screen.getByText(name).closest(".lens-row")!.textContent ?? "";

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  _resetExternalSecretsRowsForTest();
  useFleet.getState().clearExternalSecrets();
  useFleet.setState({ clusters: [clusterDTO], route: { name: "cluster", cluster: "x", section: "externalsecrets" } });
});

describe("External Secrets rows, mounted end to end (slice 3 §5.2)", () => {
  it("A4 moves the global counts with the notice, in both views, beside a read error", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db"), secret("api", { state: "Failed", reason: "SecretSyncedError" })], SECRET_KINDS()), 0);
      applyRows("t", rowsDTO(1, "stores", 1, [store("vault"), store("aws", { state: "Failed" })], [kind("secretstores"),
        kind("clustersecretstores", { coverage: "stale", class: "forbidden", error: "rbac", lastGoodAt: "2026-10-11T11:00:00Z" })]), 0);
      render(<ExternalSecretsView cluster="x" />);
      expect(chip(/^Not synced or refresh overdue/)).toBe("Not synced or refresh overdue 1");
      act(() => { applySummary(summary(1, 2, [2, 6, 4], [2, 5, 3]), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/watch failed - showing the last good state from .*rbac/);
      expect(headline()).toBe("6");
      expect(screen.getByText(/^▲ 4 not synced or stale$/)).toBeTruthy();
      expect(chip(/^Not synced or refresh overdue/)).toBe("Not synced or refresh overdue 4");
      expect(chip(/^Stores/)).toBe("Stores 3");
      expect(screen.getByTestId("eso-breakdown").textContent).toMatch(/^1 failed \(as of /);
      expect(document.querySelectorAll(".lens-row")).toHaveLength(2);
      fireEvent.click(screen.getByRole("button", { name: /^Stores/ }));
      expect(headline()).toBe("5*"); // the summary's store total, last observed (stale kind)
      expect(screen.getByText("3 not ready")).toBeTruthy();
      expect(chip(/^Not ready/)).toBe("Not ready 3");
      const gen = useHealth.getState().subs.s.requestGen;
      act(() => bootstrapSettled("s", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Final(1) -> summary(2) -> staggered parts: incarnation 1's rows never stay", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("old")], SECRET_KINDS()));
    applyRows("t", rowsDTO(1, "stores", 1, [store("vault")], STORE_KINDS()));
    render(<ExternalSecretsView cluster="x" />);
    expect(screen.getByText("old")).toBeTruthy();
    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.getByLabelText("loading external secret state")).toBeTruthy();
    act(() => applySummary(summary(2, 1, [1, 2, 0], [1, 1, 0])));
    expect(screen.queryByText("old")).toBeNull();
    act(() => { open(2); applyRows("s", rowsDTO(2, "externalsecrets", 1, [secret("old"), secret("new")], SECRET_KINDS())); });
    expect(screen.getByText("new")).toBeTruthy();
    // the stores part has not answered: the join is unverified, not "not found"
    expect(row("new")).not.toMatch(/not found/);
    fireEvent.click(screen.getByRole("button", { name: /^Stores/ }));
    expect(screen.getByLabelText("loading external secret state")).toBeTruthy();
    expect(useFleet.getState().externalsecrets.stores).toEqual([]);
    act(() => { applyRows("t", rowsDTO(2, "stores", 1, [store("vault2")], STORE_KINDS())); });
    expect([...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent)).toEqual(["vault2"]);
  });

  it("a failed open is unreadable with its error, never an empty list", () => {
    addSub("s", "x", "eso", "externalsecrets");
    addSub("t", "x", "eso", "stores");
    openStarted("s", "tok-s");
    openFailed("s", "tok-s", "cluster not observed: x", 0);
    render(<ExternalSecretsView cluster="x" />);
    expect(screen.getByText("External Secrets state could not be read.")).toBeTruthy();
    expect(screen.getByText(/cluster not observed: x/)).toBeTruthy();
  });

  it("discovery gaps and last-good times are surfaced, without downgrading live rows", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db")], [kind("externalsecrets", { discovery: "forbidden: discovery re-check" })]));
    applyRows("t", rowsDTO(1, "stores", 1, [store("vault")], STORE_KINDS()));
    render(<ExternalSecretsView cluster="x" />);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/ExternalSecret: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
    expect(screen.queryByRole("alert")).toBeNull();
    act(() => { applyRows("s", rowsDTO(1, "externalsecrets", 2, [secret("db")], SECRET_KINDS())); });
    expect(screen.queryByTestId("discovery-gap")).toBeNull();
    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("s", rowsDTO(1, "externalsecrets", 3, [secret("db")], [kind("externalsecrets", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood })])); });
    expect(screen.getByRole("alert").textContent).toContain(`showing the last good state from ${clock(lastGood)}`);
    expect(headline()).toBe("1*");
  });

  // §3: rows are pushed only when they change, so the last-sync age ticks
  // from status.refreshTime - never frozen at the capture.
  it("the last-sync age ticks between pushes", () => {
    vi.useFakeTimers({ now: new Date("2026-10-11T12:00:00Z") });
    try {
      const nowS = Math.floor(Date.now() / 1000);
      open();
      applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db", { lastRefreshUnix: nowS - 60, lastRefreshAgeSeconds: 60 })], SECRET_KINDS()));
      applyRows("t", rowsDTO(1, "stores", 1, [store("vault")], STORE_KINDS()));
      render(<ExternalSecretsView cluster="x" />);
      expect(row("db")).toContain("1m");
      act(() => { vi.advanceTimersByTime(30 * 60_000); });
      expect(row("db")).toContain("31m"); // no new push: the age moved with the clock
    } finally {
      vi.useRealTimers();
    }
  });

  // §5a: a selection is "removed" only when its own kind is live and the
  // set complete; a failing kind keeps it with its last row.
  it("removal needs the secret's own live read; a stale read keeps the selection", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db")], SECRET_KINDS()));
    applyRows("t", rowsDTO(1, "stores", 1, [store("vault")], STORE_KINDS()));
    render(<ExternalSecretsView cluster="x" />);
    act(() => useFleet.getState().selectExternalSecret("payments/db"));
    act(() => { applyRows("s", rowsDTO(1, "externalsecrets", 2, [], [kind("externalsecrets", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" })])); });
    expect(within(screen.getByTestId("eso-secret-panel")).queryByText(/ExternalSecret removed\./)).toBeNull();
    expect(screen.getByTestId("selection-unverified").textContent).toMatch(/unverified - it may still exist/);
    act(() => { applyRows("s", rowsDTO(1, "externalsecrets", 3, [], SECRET_KINDS())); });
    expect(within(screen.getByTestId("eso-secret-panel")).getByText(/ExternalSecret removed\./)).toBeTruthy();
  });

  // §5a mixed store coverage: SecretStores live, ClusterSecretStores
  // unreadable - a secret bound to a ClusterSecretStore is unverified, never
  // "not found", and a store's usage count is qualified by the secrets' read.
  it("mixed store coverage leaves the unreadable kind's joins unverified", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db"), secret("local", { storeKind: "SecretStore", storeName: "ns-vault" })], SECRET_KINDS()));
    applyRows("t", rowsDTO(1, "stores", 1, [store("ns-vault", { kind: "SecretStore", namespace: "payments" })], [kind("secretstores"),
      kind("clustersecretstores", { coverage: "unreadable", class: "forbidden", error: "rbac" })]));
    render(<ExternalSecretsView cluster="x" />);
    act(() => useFleet.getState().selectExternalSecret("payments/db"));
    const panel = within(screen.getByTestId("eso-secret-panel"));
    expect(panel.getByText(/unverified \(ClusterSecretStore read unavailable\)/)).toBeTruthy();
    expect(panel.queryByText(/was not found/)).toBeNull();
    expect(screen.queryByTestId("eso-breakdown")?.textContent ?? "").not.toMatch(/store problem/);
  });
});

// Health 1c slice 4 §5.4: a summary Top link reveals its object.
describe("a Top link reveals its object", () => {
  const selectedRow = () => document.querySelector('[data-selected="true"]')?.textContent ?? "";
  const reveal = (part: string, ref: { gvr: string; namespace?: string; name: string }) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "eso", part, ref }); });

  it("a store opens the stores view and is selected past the filters; an ExternalSecret is selected in its view", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db"), secret("api", { state: "Failed", reason: "SecretSyncedError" })], SECRET_KINDS()), 0);
    applyRows("t", rowsDTO(1, "stores", 1, [store("vault"), store("aws", { state: "Failed" })], STORE_KINDS()), 0);
    render(<ExternalSecretsView cluster="x" />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } });
    reveal("stores", { gvr: "external-secrets.io/v1/clustersecretstores", name: "aws" });
    expect(screen.getByRole("button", { name: /^Stores/ }).getAttribute("aria-pressed")).toBe("true");
    expect(selectedRow()).toContain("aws");
    expect(useFleet.getState().navIntent).toBeNull();
    reveal("externalsecrets", { gvr: "external-secrets.io/v1/externalsecrets", namespace: "payments", name: "db" });
    expect(screen.getByRole("button", { name: /^ExternalSecrets/ }).getAttribute("aria-pressed")).toBe("true");
    expect(useFleet.getState().externalsecrets.selectedKey).toBe("payments/db");
  });
});

// The Line slice 9 note §4.2: a reveal by UID pins that UID.
describe("an ESO reveal by UID", () => {
  const gone = () => document.querySelector("[data-pinned-gone]")?.getAttribute("data-pinned-gone") ?? null;
  it("an ExternalSecret replaced at its name, and a store gone from a complete read, are said", () => {
    open();
    applyRows("s", rowsDTO(1, "externalsecrets", 1, [secret("db", { uid: "uid-db-2" })], SECRET_KINDS()), 0);
    applyRows("t", rowsDTO(1, "stores", 1, [store("vault")], STORE_KINDS()), 0);
    render(<ExternalSecretsView cluster="x" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "eso", part: "externalsecrets", ref: { gvr: "external-secrets.io/v1/externalsecrets", namespace: "payments", name: "db", uid: "uid-db" } }); });
    expect(gone()).toBe("replaced");
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "eso", part: "stores", ref: { gvr: "external-secrets.io/v1/clustersecretstores", name: "gone", uid: "uid-gone" } }); });
    expect(gone()).toBe("removed");
  });
});
