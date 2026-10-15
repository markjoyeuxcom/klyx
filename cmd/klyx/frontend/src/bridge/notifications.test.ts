import { describe, it, expect, vi, beforeEach } from "vitest";

// The Line slice 10b: the notifications bridge - the baseline listing's
// words, opening a notified object pinned, and focus reporting.
const rt = vi.hoisted(() => ({ handlers: {} as Record<string, (ev: { data: unknown }) => void> }));
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn((name: string, f: (ev: { data: unknown }) => void) => { rt.handlers[name] = f; return () => {}; }) } }));
const svc = vi.hoisted(() => ({ SetFocus: vi.fn(async () => {}), Settings: vi.fn(), SetSettings: vi.fn(), RecentBaselines: vi.fn(async () => [] as unknown[]) }));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({ NotificationsService: svc }));
vi.mock("./helm", () => ({ openHelmRelease: vi.fn() }));

import { useFleet } from "../store/fleet";
import { BASELINE_EVENT, OPEN_EVENT, baselineMessage, openNotified, startHealthNotifications, _listedForTest, _resetHealthNotificationsForTest } from "./notifications";

beforeEach(() => {
  vi.clearAllMocks();
  _resetHealthNotificationsForTest();
  useFleet.setState({ route: { name: "fleet" }, navIntent: null, actionStatus: null });
});

describe("the notifications bridge", () => {
  it("says a baseline listing in one line", () => {
    expect(baselineMessage({ seq: 1, cluster: "prd", reason: "connect", count: 3, items: [
      { cluster: "prd", kind: "Pod", namespace: "shop", name: "a" }, { cluster: "prd", kind: "Node", name: "n1" },
    ] })).toBe("3 faults present at connect on prd: Pod shop/a, Node n1, +1 more");
    expect(baselineMessage({ seq: 2, cluster: "prd", reason: "after-gap", count: 1, items: [] })).toBe("1 fault found after a gap in coverage on prd");
    expect(baselineMessage({ seq: 3, cluster: "prd", reason: "dropped", count: 2, items: [] })).toBe("2 notifications on prd not sent: the de-duplication limit is full");
  });

  it("opens a notified object pinned to its UID; a burst opens its cluster", () => {
    openNotified({ cluster: "prd", task: "pods", part: "pods", gvr: "v1/pods", namespace: "shop", name: "web", uid: "u1" });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: "prd", namespace: "shop", name: "web", uid: "u1" });
    openNotified({ cluster: "stg" });
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "stg" });
  });

  it("lists baselines in the app, opens clicks, and reports focus on route changes", async () => {
    startHealthNotifications();
    rt.handlers[BASELINE_EVENT]({ data: { seq: 1, cluster: "prd", reason: "connect", count: 2, items: [] } });
    expect(useFleet.getState().actionStatus).toEqual({ kind: "info", message: "2 faults present at connect on prd" });
    rt.handlers[OPEN_EVENT]({ data: { cluster: "prd", task: "pods", part: "pods", gvr: "v1/pods", namespace: "shop", name: "web", uid: "u1" } });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", uid: "u1" });
    await Promise.resolve();
    const focus = document.hasFocus();
    expect(svc.SetFocus).toHaveBeenLastCalledWith("prd", focus);
    useFleet.getState().openCluster("stg");
    await Promise.resolve();
    expect(svc.SetFocus).toHaveBeenLastCalledWith("stg", focus);
    const calls = svc.SetFocus.mock.calls.length;
    useFleet.getState().setSection("nodes"); // same cluster: nothing new to say
    await Promise.resolve();
    expect(svc.SetFocus.mock.calls.length).toBe(calls);
  });
});

describe("a webview that subscribes after a connect", () => {
  it("lists what it missed, each listing once", async () => {
    svc.RecentBaselines.mockResolvedValueOnce([{ seq: 7, cluster: "prd", reason: "connect", count: 1, items: [] }]);
    const logged: string[] = [];
    const unsub = useFleet.subscribe((st) => { if (st.actionStatus) logged.push(st.actionStatus.message); });
    startHealthNotifications();
    await new Promise((r) => setTimeout(r, 0));
    rt.handlers[BASELINE_EVENT]({ data: { seq: 7, cluster: "prd", reason: "connect", count: 1, items: [] } }); // the same listing again
    unsub();
    expect(logged).toEqual(["1 fault present at connect on prd"]);
  });
  it("remembers a bounded window of numbers, and a newer event before older unseen ones still lists them", async () => {
    // a newer listing's event arrives before the start-up read returns older ones
    let read!: (v: unknown[]) => void;
    svc.RecentBaselines.mockReturnValueOnce(new Promise((r) => { read = r as never; }) as never);
    const logged: string[] = [];
    const unsub = useFleet.subscribe((st) => { if (st.actionStatus) logged.push(st.actionStatus.message); });
    startHealthNotifications();
    await new Promise((r) => setTimeout(r, 0));
    rt.handlers[BASELINE_EVENT]({ data: { seq: 21, cluster: "c21", reason: "connect", count: 1, items: [] } });
    read([2, 3, 21].map((seq) => ({ seq, cluster: `c${seq}`, reason: "connect", count: 1, items: [] })));
    await new Promise((r) => setTimeout(r, 0));
    expect(logged).toEqual(["1 fault present at connect on c21", "1 fault present at connect on c2", "1 fault present at connect on c3"]);
    // 400 more: the remembered set stays bounded
    for (let seq = 22; seq < 422; seq++) rt.handlers[BASELINE_EVENT]({ data: { seq, cluster: "x", reason: "connect", count: 1, items: [] } });
    unsub();
    expect(_listedForTest()).toBeLessThanOrEqual(64);
  });
});
