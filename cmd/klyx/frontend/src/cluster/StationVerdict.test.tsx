import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { applyLine, resetLineStore, type LineDTO, type LineStationDTO } from "../store/line";
import { core, replace, summary, task, verdict, kind } from "../test/healthFixtures";
import { useAttentionModel } from "../nav/useAttentionModel";
import { StationVerdict } from "./StationVerdict";
import { NeedsYou } from "../fleet/NeedsYou";

// The Line slice 11c: the station page's verdict - two facts, each with its
// own mark, in the board's words - and its station-scoped "Needs you".
const SHA = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const C = "c1";
const cl = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name: C, state: "Synced", reason: "", nodesTotal: 1, pods: 10, env: "prd", region: "", ...over } as unknown as ClusterDTO);
const st = (over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster: C, env: "prd", track: "westeurope", rank: 3, state: "matches", single: SHA, e: 1, a: [SHA], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as unknown as LineStationDTO);
const line = (stations: LineStationDTO[], over: Partial<LineDTO> = {}): LineDTO => ({
  version: 1, key: { kind: "git", repo: "r", refKind: "branch", refName: "main", label: "r @ main" }, keyHow: "most stations", keyStations: 1, keys: [], r: SHA,
  rStations: 1, applied: 1, compared: 1, notCompared: 0, checking: 0, tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }], fallback: false,
  stations, siding: [], mesh: { drawn: [], card: {}, offFleet: [] }, ...over,
} as unknown as LineDTO);

function Page({ c }: { c: ClusterDTO }) {
  const m = useAttentionModel(c.name);
  return <><StationVerdict c={c} m={m} /><NeedsYou station clusters={[c]} models={m ? { [c.name]: m } : {}} /></>;
}
const health = () => screen.getByTestId("station-verdict").querySelector("h2") as HTMLElement;
const lineEl = () => screen.getByTestId("station-verdict-line");
const mark = (el: HTMLElement) => el.querySelector("[data-aspect]")?.getAttribute("data-aspect") ?? "none";

beforeEach(() => {
  useFleet.setState({ clusters: [cl()], route: { name: "cluster", cluster: C, section: "overview" } });
  resetHealthStore();
  resetLineStore();
});

describe("the station verdict", () => {
  it("says the health in the board's words and the line's place, each with its own mark", () => {
    applySummary(summary(core(), {}, C));
    applyLine(line([st()]));
    render(<Page c={cl()} />);
    expect(health().textContent).toBe("nothing raised");
    expect(mark(health())).toBe("none");
    expect(lineEl().textContent).toBe("prd on West Europe · a1b9e3c");
    expect(mark(lineEl())).toBe("none");
  });

  it("the health line never inherits the line's failure, and the line keeps its own", () => {
    applySummary(summary(core(), {}, C));
    applyLine(line([st({ state: "differs", flags: { failing: true } } as Partial<LineStationDTO>)]));
    render(<Page c={cl()} />);
    expect(mark(health())).toBe("none");
    expect(mark(lineEl())).toBe("fault");
    expect(lineEl().textContent).toContain("a1b9e3c · differs");
  });

  it("a fault reading marks the health line; the line stays its own", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { total: 3, fault: 1 }, [kind("pods")])])), {}, C));
    applyLine(line([st()]));
    render(<Page c={cl()} />);
    expect(mark(health())).toBe("fault");
    expect(health().getAttribute("data-tone")).toBe("danger");
    expect(mark(lineEl())).toBe("none");
  });

  it("before a first LineDTO the line is being read - never 'not on the line'; checking health is never healthy", () => {
    render(<Page c={cl()} />);
    expect(lineEl().textContent).toBe("the line is being read…");
    expect(health().textContent).toMatch(/^health checking…/);
    expect(mark(health())).toBe("checking");
  });

  it("a siding station is read like any other - its mark, revision and times - only its place differs", () => {
    applyLine(line([], { siding: [st({ track: "", state: "differs", flags: { failing: true } } as Partial<LineStationDTO>)] }));
    const { unmount } = render(<Page c={cl()} />);
    expect(lineEl().textContent).toBe("prd on the siding · a1b9e3c · differs");
    expect(mark(lineEl())).toBe("fault");
    unmount();
    applyLine(line([], { version: 2, siding: [st({ track: "", state: "checking", read: "checking", single: "" } as Partial<LineStationDTO>)] }));
    render(<Page c={cl()} />);
    expect(mark(lineEl())).toBe("checking");
  });

  it("a cluster the line does not place", () => {
    applyLine(line([st({ cluster: "other" })]));
    render(<Page c={cl()} />);
    expect(lineEl().textContent).toBe("not on the line");
    expect(mark(lineEl())).toBe("none");
  });

  it("an unreachable cluster says its connection and keeps the board's guard: its place, no comparison, no line mark", () => {
    applyLine(line([st({ flags: { failing: true } } as Partial<LineStationDTO>)]));
    render(<Page c={cl({ state: "Failed", reason: "dial tcp" })} />);
    expect(health().textContent).toBe("failed: dial tcp");
    expect(mark(health())).toBe("checking");
    expect(lineEl().textContent).toBe("prd on West Europe");
    expect(mark(lineEl())).toBe("none");
  });
});

describe("the station's Needs you", () => {
  it("is this station's queue: no fleet filters, no station tag, rows open pinned to their UID", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { total: 3, fault: 1 }, [kind("pods")], {
      top: [{ cluster: C, gvr: "v1/pods", namespace: "shop", name: "web-1", uid: "u1", role: "fault", label: "CrashLoopBackOff" }],
    } as never)])), {}, C));
    render(<Page c={cl()} />);
    const q = screen.getByTestId("station-queue");
    expect(q.querySelector('[role="group"][aria-label="Filter"]')).toBeNull();
    expect(q.querySelector(".queue-cluster, [data-linepos]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Fault: Pod shop\/web-1/ }));
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: C, name: "web-1", uid: "u1" });
  });

  it("an unread station claims nothing", () => {
    render(<Page c={cl({ state: "Connecting" } as Partial<ClusterDTO>)} />);
    expect(screen.getByTestId("station-queue-notread").textContent).toMatch(/^Not read yet: /);
    expect(screen.queryByText(/Nothing on this station needs attention/)).toBeNull();
  });
});
