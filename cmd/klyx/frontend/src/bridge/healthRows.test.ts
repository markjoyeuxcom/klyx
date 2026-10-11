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
import { useHealth, type HealthPart, type HealthSummary, type RowsDTO } from "../store/health";

const part = (ver: number): HealthPart => ({ id: "certificates", entity: "Certificates", scope: "all namespaces", classifier: "native", coverage: "live", kinds: [], obs: ver, ver });
const rows = (ver: number, incarnation = 1): RowsDTO => ({ cluster: "c1", incarnation, task: "certs", part: part(ver), evaluatedAt: "", rows: [{ uid: `v${ver}` }] });
const emit = (name: string, data: unknown) => (handlers.get(name) ?? []).forEach((f) => f({ data }));
const subs = () => Object.values(useHealth.getState().subs);
const flush = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  vi.clearAllMocks();
  handlers.clear();
  order.length = 0;
  useHealth.setState({ summaries: {}, finalizedThrough: {}, subs: {} });
  _resetHealthRowsForTest();
  (HealthService.OpenRows as ReturnType<typeof vi.fn>).mockImplementation(async (...a: unknown[]) => {
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
    const tokens = (HealthService.OpenRows as ReturnType<typeof vi.fn>).mock.calls.map((c) => c[3]);
    expect(new Set(tokens).size).toBe(2);
    await flush();
    expect(subs().every((s) => s.ver === 1)).toBe(true);
    close();
    close2();
  });

  it("an event that beats the bootstrap reply wins; the older reply is dropped", async () => {
    let resolve!: (v: unknown) => void;
    (HealthService.OpenRows as ReturnType<typeof vi.fn>).mockImplementation(() => new Promise((r) => { resolve = r; }));
    const close = openRows("c1", "certs", "certificates");
    emit(rowsEvent("c1", "certs", "certificates"), rows(3));
    resolve({ rows: rows(2) });
    await flush();
    expect(subs()[0].ver).toBe(3);
    close();
  });

  it("closing before the bootstrap reply: the reply never lands, and the token is closed", async () => {
    let resolve!: (v: unknown) => void;
    (HealthService.OpenRows as ReturnType<typeof vi.fn>).mockImplementation(() => new Promise((r) => { resolve = r; }));
    const close = openRows("c1", "certs", "certificates");
    const token = (HealthService.OpenRows as ReturnType<typeof vi.fn>).mock.calls[0][3];
    close();
    expect(HealthService.CloseRows).toHaveBeenCalledWith(token);
    resolve({ rows: rows(5) });
    await flush();
    expect(subs()).toHaveLength(0);
    emit(rowsEvent("c1", "certs", "certificates"), rows(6)); // a late event after close
    expect(subs()).toHaveLength(0);
  });

  it("an open error is the subscription's error, never empty rows", async () => {
    (HealthService.OpenRows as ReturnType<typeof vi.fn>).mockImplementation(async () => ({ error: "cluster not observed: c1" }));
    const close = openRows("c1", "certs", "certificates");
    await flush();
    expect(subs()[0]).toMatchObject({ error: "cluster not observed: c1", rows: null });
    close();
  });

  it("the A4 deadline issues a fenced re-bootstrap; its reply resolves the mismatch", async () => {
    vi.useFakeTimers({ now: 0 });
    (HealthService.RowsBootstrap as ReturnType<typeof vi.fn>).mockImplementation(async (_t: string, gen: number) => ({ rows: rows(4), requestGen: gen }));
    const close = openRows("c1", "certs", "certificates");
    await vi.runAllTicks();
    await Promise.resolve();
    await Promise.resolve();
    expect(subs()[0].ver).toBe(1);
    const summary: HealthSummary = { cluster: "c1", incarnation: 1, seq: 2, evaluatedAt: "", connection: "connected",
      tasks: [{ id: "certs", coverage: "live", parts: [part(4)] }] };
    emit("health:summary", summary);
    expect(subs()[0].mismatchSince).not.toBeNull();
    await vi.advanceTimersByTimeAsync(1000);
    const token = (HealthService.OpenRows as ReturnType<typeof vi.fn>).mock.calls[0][3];
    expect(HealthService.RowsBootstrap).toHaveBeenCalledWith(token, 1);
    await vi.advanceTimersByTimeAsync(1);
    expect(subs()[0]).toMatchObject({ ver: 4, mismatchSince: null, refreshing: false });
    close();
  });
});
