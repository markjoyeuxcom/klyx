import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act, within } from "@testing-library/react";
import type { UpgradeReportDTO } from "../store/fleet";
import { UpgradeView, daysUntil } from "./UpgradeView";

vi.mock("../bridge/upgrade", () => ({ getUpgradeReadiness: vi.fn(async () => ({}) as UpgradeReportDTO) }));
import { getUpgradeReadiness } from "../bridge/upgrade";

const report = (over: Partial<UpgradeReportDTO> = {}): UpgradeReportDTO => ({
  serverVersion: "v1.31.4", serverMinor: 31, eolDate: "2099-01-01", skew: [], deprecated: [], ...over,
});
const api = (over = {}) => ({ group: "flowcontrol.apiserver.k8s.io", version: "v1beta3", resource: "flowschemas", kind: "FlowSchema", removedInMinor: 32, replacedBy: "flowcontrol.apiserver.k8s.io/v1", count: 4, countKnown: true, ...over });
type D<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): D<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };

describe("UpgradeView (redesign step 5)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("clean against the curated table, said as such - not a guarantee", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report());
    render(<UpgradeView cluster="x" />);
    expect(await screen.findByText("No deprecated API from Klyx's curated table is served.")).toBeTruthy();
    expect(screen.getByText(/not a guarantee/)).toBeTruthy();
    expect(screen.getByText("no API removal blocker for 1.32 (curated table)")).toBeTruthy();
  });

  it("a partial report never reads as clean", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({ warnings: ["node list unreadable: forbidden"] }));
    render(<UpgradeView cluster="x" />);
    expect(await screen.findByText(/Partial report - node list unreadable: forbidden/)).toBeTruthy();
    expect(screen.getByText("No deprecated API found in what could be read.")).toBeTruthy();
    expect(screen.queryByText(/against the curated table$/)).toBeNull();
  });

  it("frames removals against the target and re-frames on change; a removal's mark is a fault, a later one look-at", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({ deprecated: [api(), api({ resource: "x", kind: "Later", removedInMinor: 34 })] }));
    render(<UpgradeView cluster="x" />);
    expect(await screen.findByText("1 API removed by 1.32")).toBeTruthy();
    const mark = (kind: string) => ([...document.querySelectorAll(".lens-row")].find((r) => r.textContent?.includes(kind)) as HTMLElement).querySelector(".row-mark") as HTMLElement;
    expect(mark("FlowSchema").querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(mark("FlowSchema").getAttribute("aria-label")).toBe("removed in 1.32");
    expect(mark("Later").querySelector('[data-aspect="attention"]')).toBeTruthy();
    expect(document.querySelector(".lens-row")?.textContent).not.toContain("▲"); // the mark carries the shape
    fireEvent.change(screen.getByLabelText("target minor"), { target: { value: "34" } });
    expect(screen.getByText("2 APIs removed by 1.34")).toBeTruthy();
    expect(mark("Later").querySelector('[data-aspect="fault"]')).toBeTruthy();
  });

  it("the API inspector explains the risk and the count's meaning in words", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({ deprecated: [api({ countCapped: true })] }));
    render(<UpgradeView cluster="x" />);
    fireEvent.click((await screen.findByText("FlowSchema")).closest(".lens-row") as HTMLElement);
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText(/Removed in 1.32 - at or before the 1.32 target/)).toBeTruthy();
    expect(p.getByText("4+ (count capped)")).toBeTruthy();
    expect(p.getByText(/server converts on read/)).toBeTruthy();
  });

  it("skew findings list nodes, violations first, with an Open node action", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({ skew: [
      { node: "a-warn", version: "v1.30.0", delta: 1, level: "warning", detail: "one minor behind" },
      { node: "b-bad", version: "v1.27.0", delta: 4, level: "danger", detail: "outside the supported skew" },
    ] }));
    render(<UpgradeView cluster="x" />);
    await screen.findByText("1 kubelet skew violation now");
    fireEvent.click(screen.getByRole("button", { name: /^Node skew/ }));
    const rows = [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");
    expect(rows[0]).toContain("b-bad");
    expect(document.querySelector(".lens-row .row-mark")?.getAttribute("aria-label")).toBe("Skew violation");
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByRole("button", { name: "Open node" })).toBeTruthy();
  });

  it("an error is unreadable with Retry; EOL past is a danger shape", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValueOnce(report({ error: "discovery failed" }));
    render(<UpgradeView cluster="x" />);
    expect(await screen.findByText("Could not read upgrade readiness.")).toBeTruthy();
    vi.mocked(getUpgradeReadiness).mockResolvedValueOnce(report({ eolDate: "2000-01-01" }));
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    const eol = await screen.findByText(/^upstream EOL \d+d ago$/);
    expect(eol.closest(".t-status")?.querySelector('[data-aspect="fault"]')).toBeTruthy(); // a danger shape, the Aspect
  });

  it("a late report for a previous cluster never lands on the new one", async () => {
    const late = deferred<UpgradeReportDTO>();
    vi.mocked(getUpgradeReadiness).mockReturnValueOnce(late.promise).mockResolvedValueOnce(report({ serverVersion: "v1.33.0", serverMinor: 33 }));
    const { rerender } = render(<UpgradeView cluster="a" />);
    rerender(<UpgradeView cluster="b" />);
    expect(await screen.findByText("v1.33.0")).toBeTruthy();
    await act(async () => { late.resolve(report({ serverVersion: "v1.29.0" })); await late.promise; });
    expect(screen.queryByText("v1.29.0")).toBeNull();
  });

  it("daysUntil handles bad input", () => {
    expect(daysUntil("")).toBeNull();
    expect(daysUntil("nope")).toBeNull();
    expect(daysUntil("2026-01-11", Date.parse("2026-01-01T00:00:00Z"))).toBe(10);
  });
});

describe("UpgradeView skew is a current prerequisite, not target readiness (Codex 1e89fc5a)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("1.31 control plane, a 1.28 kubelet warning, no deprecated APIs: no unqualified 'no blocker'; target changes never imply re-evaluated skew", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({ skew: [{ node: "old", version: "v1.28.0", delta: 3, level: "warning", detail: "upgrade nodes before the next control-plane bump" }] }));
    render(<UpgradeView cluster="x" />);
    expect(await screen.findByText("1 node to upgrade before the next control-plane bump")).toBeTruthy();
    expect(screen.getByText("no API removal blocker for 1.32 (curated table)")).toBeTruthy();
    expect(screen.queryByText(/^no blocker/)).toBeNull();
    fireEvent.change(screen.getByLabelText("target minor"), { target: { value: "34" } });
    expect(screen.getByText("1 node to upgrade before the next control-plane bump")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Node skew \(current control plane\)/ })).toBeTruthy();
  });

  it("a refreshed report with a new server minor drops an obsolete target", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValueOnce(report());
    render(<UpgradeView cluster="x" />);
    await screen.findByText("v1.31.4");
    fireEvent.change(screen.getByLabelText("target minor"), { target: { value: "32" } });
    vi.mocked(getUpgradeReadiness).mockResolvedValueOnce(report({ serverVersion: "v1.33.0", serverMinor: 33 }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("v1.33.0")).toBeTruthy();
    expect((screen.getByLabelText("target minor") as HTMLSelectElement).value).toBe("34");
  });
});
