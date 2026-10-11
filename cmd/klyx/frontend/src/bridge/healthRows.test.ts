import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const handlers = new Map<string, ((ev: { data: unknown }) => void)[]>();
const order: string[] = [];
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((name: string, fn: (ev: { data: unknown }) => void) => {
      order.push(`on:${name}`);
      handlers.set(name, [...(handlers.get(name) ?? []), fn]);
      return () => handlers.set(name, (handlers.get(name) ?? []).filter((f) => f !== fn));
    }),
  },
}));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  HealthService: {
    Summaries: vi.fn(async () => []),
    OpenRows: vi.fn(),
    CloseRows: vi.fn(async () => {}),
    RowsBootstrap: vi.fn(),
  },
}));
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { openRows, rowsEvent, _resetHealthRowsForTest } from "./healthRows";
import { useHealth, resetHealthStore, backoff, type HealthPart, type HealthSummary, type RowsDTO } from "../store/health";

const part = (ver: number): HealthPart => ({ id: "certificates", entity: "Certificates", scope: "all namespaces", classifier: "native", coverage: "live", kinds: [], obs: ver, ver });
const rows = (ver: number, incarnation = 1): RowsDTO => ({ cluster: "c1", incarnation, task: "certs", part: part(ver), evaluatedAt: "", rows: [{ uid: `i${incarnation}v${ver}` }] });
const summary = (incarnation: number, seq: number, ver: number): HealthSummary => ({ cluster: "c1", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "certs", coverage: "live", parts: [part(ver)] }] });
const emit = (name: string, data: unknown) => (handlers.get(name) ?? []).forEach((f) => f({ data }));
const subs = () => Object.values(useHealth.getState().subs);
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };
const openMock = HealthService.OpenRows as ReturnType<typeof vi.fn>;
const tokens = () => openMock.mock.calls.map((c) => c[3] as string);

beforeEach(() => {
  vi.clearAllMocks();
  handlers.clear();
  order.length = 0;
  resetHealthStore();
  _resetHealthRowsForTest();
  openMock.mockImplementation(async (...a: unknown[]) => {
    order.push(`open:${a[3]}`);
    return { rows: rows(1) };
  });
});
afterEach(() => vi.useRealTimers());

describe("health rows transport (design §2b)", () => {
  it("subscribes to the part's event before it bootstraps, with a fresh token per open", async () => {
    const close = openRows("c1", "certs", "certificates");
    const close2 = openRows("c1", "certs", "certificates");
    const evIdx = order.indexOf(`on:${rowsEvent("c1", "certs", "certificates")}`);
    const openIdx = order.findIndex((o) => o.startsWith("open:"));
    expect(evIdx).toBeGreaterThanOrEqual(0);
    expect(evIdx).toBeLessThan(openIdx);
    expect(new Set(tokens()).size).toBe(2);
    await flush();
    expect(subs().every((s) => s.ver === 1 && s.open === "open" && s.boundIncarnation === 1)).toBe(true);
    close();
    close2();
  });

  it("an event that beats the bootstrap reply wins; the older reply is dropped", async () => {
    let resolve!: (v: unknown) => void;
    openMock.mockImplementation(() => new Promise((r) => { resolve = r; }));
    const close = openRows("c1", "certs", "certificates");
    emit(rowsEvent("c1", "certs", "certificates"), rows(3));
    resolve({ rows: rows(2) });
    await flush();
    expect(subs()[0].ver).toBe(3);
    close();
  });

  it("closing before the bootstrap reply: the reply never lands, and the token is closed", async () => {
    let resolve!: (v: unknown) => void;
    openMock.mockImplementation(() => new Promise((r) => { resolve = r; }));
    const close = openRows("c1", "certs", "certificates");
    const [token] = tokens();
    close();
    expect(HealthService.CloseRows).toHaveBeenCalledWith(token);
    resolve({ rows: rows(5) });
    await flush();
    expect(subs()).toHaveLength(0);
    emit(rowsEvent("c1", "certs", "certificates"), rows(6)); // a late event after close
    expect(subs()).toHaveLength(0);
  });

  // Codex infra review #2: a mounted subscription follows a replaced
  // Observer on the same cluster - a fresh token, the old one closed - with
  // no other lens supplying rows.
  it("rebinds to a replacement Observer without a cluster change", async () => {
    openMock.mockImplementation(async (...a: unknown[]) => ({ rows: rows(1, tokens().length === 1 ? 1 : 2) }));
    const close = openRows("c1", "certs", "certificates");
    await flush();
    const [t1] = tokens();
    expect(subs()[0]).toMatchObject({ boundIncarnation: 1, incarnation: 1 });
    emit("health:summary", summary(1, 5, 1));
    emit("health:summary", { ...summary(1, 6, 1), final: true }); // the old Observer ends
    expect(subs()[0].rows).toBeNull();
    emit("health:summary", summary(2, 1, 1)); // its replacement
    await flush();
    expect(HealthService.CloseRows).toHaveBeenCalledWith(t1);
    expect(tokens()).toHaveLength(2);
    expect(tokens()[1]).not.toBe(t1);
    expect(subs()[0]).toMatchObject({ boundIncarnation: 2, incarnation: 2, open: "open" });
    expect((subs()[0].rows as { uid: string }[])[0].uid).toBe("i2v1");
    close();
  });

  it("a failed first open shows its error, then recovers with a fresh token after backoff", async () => {
    vi.useFakeTimers({ now: 0 });
    openMock.mockImplementationOnce(async () => { throw new Error("connection refused"); });
    const close = openRows("c1", "certs", "certificates");
    await flush();
    expect(subs()[0]).toMatchObject({ open: "failed", error: "connection refused", rows: null });
    await vi.advanceTimersByTimeAsync(backoff(1));
    await flush();
    expect(tokens()).toHaveLength(2);
    expect(subs()[0]).toMatchObject({ open: "open", error: null, ver: 1 });
    close();
  });

  it("an open error reply is the subscription's error, never empty rows", async () => {
    openMock.mockImplementation(async () => ({ error: "cluster not observed: c1" }));
    const close = openRows("c1", "certs", "certificates");
    await flush();
    expect(subs()[0]).toMatchObject({ error: "cluster not observed: c1", rows: null, open: "failed" });
    close();
  });

  it("the A4 deadline issues a fenced re-bootstrap; a rejection retries with backoff until it resolves", async () => {
    vi.useFakeTimers({ now: 0 });
    const boot = HealthService.RowsBootstrap as ReturnType<typeof vi.fn>;
    boot.mockImplementationOnce(async () => { throw new Error("capture timed out"); });
    boot.mockImplementation(async (_t: string, gen: number) => ({ rows: rows(4), requestGen: gen }));
    const close = openRows("c1", "certs", "certificates");
    await flush();
    expect(subs()[0].ver).toBe(1);
    emit("health:summary", summary(1, 2, 4));
    expect(subs()[0].mismatchSince).not.toBeNull();
    await vi.advanceTimersByTimeAsync(1000);
    await flush();
    expect(boot).toHaveBeenCalledTimes(1);
    expect(subs()[0]).toMatchObject({ refreshing: true, attempt: 1, refreshError: "capture timed out" });
    await vi.advanceTimersByTimeAsync(backoff(1));
    await flush();
    expect(boot).toHaveBeenCalledTimes(2);
    expect(boot.mock.calls[1][1]).toBe(2); // a new request generation
    expect(subs()[0]).toMatchObject({ ver: 4, mismatchSince: null, refreshing: false, refreshError: null, error: null });
    close();
  });

  // Codex infra re-review #2: an A4 deadline during a failed open's backoff
  // is consumed once (the counts qualify), then waits for the reopen - no
  // zero-delay timer churn, no bootstrap without a token.
  it("an A4 deadline before a failed open's retry waits for the reopen, then recovers", async () => {
    vi.useFakeTimers({ now: 0 });
    let reject!: (e: Error) => void;
    openMock.mockImplementationOnce(() => new Promise((_r, rej) => { reject = rej; }));
    openMock.mockImplementation(async () => ({ rows: rows(4) }));
    const boot = HealthService.RowsBootstrap as ReturnType<typeof vi.fn>;
    boot.mockImplementation(async (_t: string, gen: number) => ({ rows: rows(4), requestGen: gen }));
    const close = openRows("c1", "certs", "certificates");
    emit(rowsEvent("c1", "certs", "certificates"), rows(1)); // the shared event beats the open
    emit("health:summary", summary(1, 2, 4)); // the summary is ahead at t=0
    await vi.advanceTimersByTimeAsync(500);
    reject(new Error("connection refused")); // the open fails at t=500 -> retry at 1500
    await flush();
    expect(subs()[0]).toMatchObject({ open: "failed", token: null, rows: [{ uid: "i1v1" }] });
    const timers = vi.spyOn(globalThis, "setTimeout");
    await vi.advanceTimersByTimeAsync(600); // past the A4 deadline (1000), before the open retry (1500)
    expect(subs()[0]).toMatchObject({ refreshing: true });
    expect(boot).not.toHaveBeenCalled(); // nothing to bootstrap without a token
    expect(timers.mock.calls.length).toBeLessThan(5); // no zero-delay churn
    timers.mockRestore();
    await vi.advanceTimersByTimeAsync(500); // the open retry
    await flush();
    expect(tokens()).toHaveLength(2);
    expect(subs()[0]).toMatchObject({ open: "open", ver: 4, refreshing: false, mismatchSince: null, error: null });
    close();
  });
});
