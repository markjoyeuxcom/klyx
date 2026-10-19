import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { HistoryLine, HistoryPage, PodHistoryRequest } from "../bridge/logHistory";

const calls: PodHistoryRequest[] = [];
let answer: (req: PodHistoryRequest) => HistoryPage;
vi.mock("../bridge/logHistory", () => ({
  readPodHistory: async (req: PodHistoryRequest) => { calls.push(req); return answer(req); },
  closeHistoryView: () => {},
}));
const { LogHistoryPane } = await import("./LogHistoryPane");

const lines = (from: number, n: number, tag = "l"): HistoryLine[] =>
  Array.from({ length: n }, (_, i) => ({ ts: new Date(from - i * 1000).toISOString(), line: `${tag} ${n - i}`, branch: 0, container: "web" }));
const page = (over: Partial<HistoryPage> = {}): HistoryPage => ({
  view: "v", lines: lines(Date.parse("2026-10-19T12:00:00Z"), 3), selectors: [`{k8s_namespace_name="team", k8s_pod_name="api-1", pod=""} | k8s_pod_uid="u-1"`],
  copyable: true, mode: "uid", uidOffer: false, strength: "selected by this pod's UID (k8s_pod_uid); lines without a pod UID are not shown",
  source: "Service logs/loki:http", tenant: "lab", start: "2026-10-19T11:00:00Z", end: "2026-10-19T12:00:00Z", done: false, ...over,
});
const target = { namespace: "team", name: "api-1", uid: "u-1", createdUnix: 1_792_400_000, containers: [{ name: "web" }] };
const shown = () => screen.getAllByTestId("history-line").map((l) => l.textContent ?? "");

beforeEach(() => { calls.length = 0; });

describe("LogHistoryPane (coverage S8b §7)", () => {
  it("says the selector and its strength, holds pages oldest first, and loads older", async () => {
    answer = (req) => (req.restart ? page() : page({ lines: lines(Date.parse("2026-10-19T11:30:00Z"), 2, "older"), done: true }));
    render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect(screen.getByTestId("history-strength").textContent).toContain("selected by this pod's UID"));
    expect(calls[0]).toMatchObject({ restart: true, namespace: "team", pod: "api-1", uid: "u-1", createdUnix: 1_792_400_000, mode: "auto" });
    expect(calls[0].rangeSeconds).toBeUndefined(); // since created
    expect(screen.getByTestId("history-selector").textContent).toContain(`k8s_pod_uid="u-1"`);
    expect(shown().map((t) => t.replace(/^.*Z /, ""))).toEqual(["l 1", "l 2", "l 3"]);
    await act(async () => { fireEvent.click(screen.getByTestId("history-older")); });
    expect(calls[1]).toMatchObject({ restart: false });
    expect(shown().map((t) => t.replace(/^.*Z /, ""))).toEqual(["older 1", "older 2", "l 1", "l 2", "l 3"]);
    expect(screen.getByTestId("history-start").textContent).toContain("Start of the range reached");
  });

  it("an explicit mode change starts a new page set; a UID offer is the person's choice", async () => {
    answer = (req) => (req.mode === "name" ? page({ mode: "name", uidOffer: true, strength: "selected by namespace and name only: every pod with this name in the range" }) : page());
    render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect(screen.getByTestId("history-strength").textContent).toContain("UID"), { timeout: 3000 });
    await act(async () => { fireEvent.click(await screen.findByTestId("history-by-name", {}, { timeout: 3000 })); });
    expect(calls[calls.length - 1]).toMatchObject({ restart: true, mode: "name" });
    await act(async () => { fireEvent.click(await screen.findByTestId("history-by-uid", {}, { timeout: 3000 })); });
    expect(calls[calls.length - 1]).toMatchObject({ restart: true, mode: "uid" });
  });

  it("Copy LogQL is unavailable when the query shown differs from the one sent", async () => {
    answer = () => page({ copyable: false, selectors: ["{cluster=\"[redacted]\"}"] });
    render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect((screen.getByLabelText("Copy LogQL") as HTMLButtonElement).disabled).toBe(true));
    expect(screen.getByLabelText("Copy LogQL").getAttribute("title")).toContain("does not copy it");
  });

  it("says a source change, a saturated boundary with Skip past, and nothing to select", async () => {
    answer = (req) => (req.skip ? page({ lines: lines(Date.parse("2026-10-19T10:00:00Z"), 1, "after"), done: true })
      : page({ reset: true, markers: [{ branch: 0, ts: "2026-10-19T11:50:00Z", count: 5000, refused: "max entries limit per query exceeded" }] }));
    const { unmount } = render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect(screen.getByText("The Loki source changed; reading the newest again.")).toBeTruthy());
    const marker = screen.getByTestId("history-marker").textContent ?? "";
    expect(marker).toContain("At least 5,000 lines share the timestamp");
    expect(marker).toContain("Klyx cannot establish whether more remain");
    expect(marker).toContain("Loki refused a larger read");
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: /^Skip past/ })); });
    expect(calls[calls.length - 1]).toMatchObject({ restart: false, skip: [0] });
    unmount();
    answer = () => page({ lines: [], selectors: [], strength: "", noStreams: "match names cluster, which is not an index label on tenant lab in the range" });
    render(<LogHistoryPane cluster="c" target={{ ...target, createdUnix: undefined }} />);
    await waitFor(() => expect(screen.getByTestId("history-nostreams").textContent).toContain("Nothing is claimed about this pod's lines"));
    expect(calls[calls.length - 1].rangeSeconds).toBe(24 * 3600); // no created time: a day
    expect(screen.queryByTestId("history-older")).toBeNull();
  });

  it("past 10,000 lines the newest go and older pages stay reachable", async () => {
    let n = 0;
    answer = () => { n++; return page({ lines: lines(Date.parse("2026-10-19T12:00:00Z") - n * 2_000_000, 1000, `p${n}`) }); };
    render(<LogHistoryPane cluster="c" target={target} />);
    await screen.findByTestId("history-older");
    for (let i = 0; i < 10; i++) await act(async () => { fireEvent.click(screen.getByTestId("history-older")); });
    expect(screen.getByTestId("history-cap").textContent).toContain("Newer lines dropped (10,000 held)");
    expect(screen.getByTestId("history-older")).toBeTruthy();
  });

  it("a failed Refresh keeps the last good lines, marked; a changed target never shows them (Codex's review of b2f945c)", async () => {
    answer = () => page();
    render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect(shown().length).toBe(3));
    answer = () => page({ lines: [], error: "read failed" });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Refresh" })); });
    expect(shown().length).toBe(3);
    expect(screen.getByTestId("history-error").textContent).toContain("the lines below are the last read, not refreshed");
    expect(screen.getByTestId("history-strength").textContent).toContain("selected by this pod's UID"); // what they were read by
    // a different range is another target: its failure shows no old lines
    await act(async () => { fireEvent.change(screen.getByLabelText("range"), { target: { value: String(3600) } }); });
    await waitFor(() => expect(screen.queryAllByTestId("history-line").length).toBe(0));
  });

  it("a view stopped at markers offers Skip past, not an endless Load older", async () => {
    answer = () => page({ more: false, markers: [{ branch: 0, ts: "2026-10-19T11:50:00Z", count: 5000 }] });
    render(<LogHistoryPane cluster="c" target={target} />);
    await waitFor(() => expect(screen.getByTestId("history-marker")).toBeTruthy());
    expect(screen.queryByTestId("history-older")).toBeNull();
    expect(screen.queryByTestId("history-start")).toBeNull();
  });
});

