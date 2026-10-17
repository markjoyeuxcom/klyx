import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { PartRows } from "../health/useCompositionRows";
import type { NotificationsDTO, ReceiverDTO } from "../bridge/fluxcomposition";
import { useFleet } from "../store/fleet";
import { NotificationsView } from "./NotificationsView";

let notes: NotificationsDTO;
vi.mock("../bridge/fluxcomposition", () => ({
  getCompositionDetail: vi.fn(async (_c: string, _k: string, _ns: string, _n: string, uid: string) => ({ uid, sources: [], consumers: [], events: [] })),
  getNotifications: vi.fn(async () => notes),
}));
vi.mock("../bridge/gitops", () => ({ reconcile: vi.fn(async () => {}), setSuspend: vi.fn(async () => {}) }));
import { getNotifications } from "../bridge/fluxcomposition";

let rowsValue: PartRows<ReceiverDTO>;
vi.mock("../health/useCompositionRows", () => ({ useReceiverRows: () => rowsValue }));
const ok = { included: false, role: "ok", label: "Ready" };
const receiver = (name: string, over: Partial<ReceiverDTO> = {}): ReceiverDTO => ({
  uid: `uid-${name}`, gvr: "x", kind: "Receiver", namespace: "flux-system", name, generation: 1, type: "github", secretRef: "token", events: ["push"],
  resources: [{ kind: "GitRepository", name: "fleet" }], resourceCount: 1, filterCount: 0, suspended: false, ready: "True", clipped: [], rule: ok,
  meaning: "configuration accepted, token read", ...over,
});
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "Notification kinds" })).getByRole("button", { name });
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");
const clickRow = (text: string) => fireEvent.click([...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => (r.textContent ?? "").includes(text))!);

describe("NotificationsView (coverage S1c)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    rowsValue = { rows: [], coverage: [{ kind: "Receiver", served: true, synced: true, coverage: "live" }], watchError: null, loading: false,
      rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null };
    notes = { providers: [{ uid: "p", namespace: "flux-system", name: "slack", type: "slack", channel: "alerts", hasAddress: true, hasProxy: false, secretRef: "slack-url",
      suspended: false, alerts: [{ kind: "Alert", namespace: "flux-system", name: "on-call", found: true }], alertsState: "found" }],
      alerts: [{ uid: "a", namespace: "flux-system", name: "on-call", providerName: "slack", providerState: "found", eventSources: [{ kind: "Kustomization", name: "*" }],
        inclusionList: [], exclusionList: [], metadataKeys: ["cluster"], suspended: false, dispatch: { NotificationDispatchFailed: 2 },
        dispatchSummary: "2 NotificationDispatchFailed (from the controller's events, which expire after about an hour)" }],
      providersList: {}, alertsList: { more: 3 }, asOfUnix: 1760000000 };
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", fluxPresent: true, fluxNotificationsPresent: true } as never] });
  });

  it("Receivers have verdicts and say what Ready means, never their path", async () => {
    rowsValue = { ...rowsValue, rows: [receiver("gh"), receiver("broken", { ready: "False", reason: "TokenNotFound", rule: { included: true, role: "fault", label: "Failed" } })] };
    render(<NotificationsView cluster="c" />);
    expect(rowTexts()[0]).toContain("broken");
    fireEvent.click(screen.getByText("gh"));
    expect((await screen.findByTestId("receiver-meaning")).textContent).toBe("Ready: configuration accepted, token read.");
    expect(screen.getByTestId("receiver-panel").textContent).toContain("Klyx cannot see whether webhooks arrive");
  });

  it("Providers and Alerts are read on demand as facts: no verdict, the address never shown, dispatch as evidence, the cap said", async () => {
    render(<NotificationsView cluster="c" />);
    fireEvent.click(tab(/^Providers/));
    await waitFor(() => expect(rowTexts().join("|")).toContain("slack"));
    expect(vi.mocked(getNotifications)).toHaveBeenCalledTimes(1);
    clickRow("flux-system/slack");
    const p = await screen.findByTestId("provider-panel");
    expect(p.textContent).toContain("set (not shown: it can carry the webhook secret)");
    expect(p.textContent).toContain("without a verdict");
    fireEvent.click(tab(/^Alerts/));
    await waitFor(() => expect(screen.getByTestId("notifications-asof").textContent).toContain("+3 alerts past the 500-object read"));
    clickRow("flux-system/on-call");
    expect((await screen.findByTestId("alert-dispatch")).textContent).toContain("2 NotificationDispatchFailed");
    expect(screen.getByTestId("alert-panel").textContent).toContain("cluster (values not shown)");
  });
});
