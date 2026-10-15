import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { resetHealthStore } from "../store/health";
import { applyLine, resetLineStore, type LineDTO, type LineStationDTO } from "../store/line";
import { applyLineGit, resetLineGitStore, type LineGitDTO } from "../store/lineGit";
import { resetExpiryStore } from "../store/expiry";

const rt = vi.hoisted(() => ({ opened: [] as string[], due: 0 }));
vi.mock("@wailsio/runtime", async (orig) => ({
  ...(await orig<typeof import("@wailsio/runtime")>()),
  Browser: { OpenURL: async (u: string) => { rt.opened.push(u); } },
  Events: { On: () => () => {} },
}));
vi.mock("../bridge/lineGit", () => ({ fetchDue: () => { rt.due++; } }));
vi.mock("../bridge/expiry", () => ({ getExpiryScan: vi.fn(async () => ({ items: [], warnings: [] })) }));
import { FleetView } from "./FleetView";

// The Line slice 6d: the fleet screen with Git history - each ordered cell,
// T and its freshness, the Ledger's commits not yet everywhere with dots,
// "Open in Git", the fence, and the 5-minute fetch cadence.

const T = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const OLD = "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const NEW = "77aa0010a1b2c3d4e5f60718293a4b5c6d7e8f90";
const c = (name: string): ClusterDTO => ({ name, state: "Synced", reason: "", nodesTotal: 1, pods: 10, env: "", region: "" } as unknown as ClusterDTO);
const st = (cluster: string, env: string, rank: number, single: string, state = "differs"): LineStationDTO =>
  ({ cluster, env, track: "westeurope", rank, state, single, e: 1, a: [single], u: 0, read: "ok", units: [], flags: { failing: false } } as unknown as LineStationDTO);
const line = (version: number): LineDTO => ({
  version, key: { kind: "git", repo: "github.com/acme/fleet", refKind: "branch", refName: "main", label: "github.com/acme/fleet @ branch main" },
  keyHow: "most stations", keyStations: 3, keys: [], r: OLD, rStations: 2, applied: 2, compared: 3, notCompared: 0, checking: 0,
  tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }], fallback: false,
  stations: [st("d", "dev", 0, T, "matches"), st("s", "stg", 2, OLD), st("p", "prd", 3, OLD)], siding: [], mesh: { drawn: [], card: {}, offFleet: [] },
} as unknown as LineDTO);
const commit = (sha: string, subject: string, has: string[]) => ({ sha, subject, author: "Ada", time: "2026-10-15T18:00:00Z", has });
const git = (lineVersion: number): LineGitDTO => ({
  version: 7, lineVersion, state: "ready", repo: "git@github.com:acme/fleet.git", commitLink: "https://github.com/acme/fleet/commit/{sha}",
  target: { ref: "refs/heads/main", name: "main", sha: T, fetchedAt: "2026-10-15T18:31:00Z" },
  stations: [
    { cluster: "d", sha: T, state: "at" },
    { cluster: "s", sha: OLD, state: "behind", n: 2 },
    { cluster: "p", sha: OLD, state: "behind", n: 2, held: { kind: "dependency", text: "blocked by dependency flux-system/infra", unit: "Kustomization flux-system/apps" } },
  ],
  ledger: [commit(T, "scale shop-api to 3 <b>replicas</b>", ["d"]), commit(NEW, "bump chart", ["d"])],
  only: [],
} as unknown as LineGitDTO);

beforeEach(() => {
  useFleet.setState({ clusters: [c("d"), c("s"), c("p")], route: { name: "fleet" } });
  resetHealthStore();
  resetLineStore();
  resetLineGitStore();
  resetExpiryStore();
  rt.opened.length = 0;
  rt.due = 0;
});

describe("the fleet screen with Git history (slice 6d)", () => {
  it("orders each cell against T, names T, and lists the commits not yet everywhere", () => {
    applyLine(line(3));
    applyLineGit(git(3));
    render(<FleetView />);
    expect(screen.getByRole("heading", { level: 2, name: "1 of 3 compared stations at main · 2 behind" })).toBeTruthy();
    expect(screen.getByTestId("line-target").textContent).toMatch(/^main at a1b9e3c · fetched \d\d:\d\d$/);
    expect(screen.getByText(/prd · West Europe is held, 2 commits behind main: blocked by dependency flux-system\/infra/)).toBeTruthy();
    const cells = screen.getAllByRole("button", { name: /^(d|s|p), / });
    expect(cells.map((b) => b.querySelector(".station-cell-rev")?.textContent)).toEqual(["a1b9e3c · at main", "9f2c11d · 2 behind main", "9f2c11d · held · 2 behind"]);
    // held behind T marks the cell for attention
    expect(cells[2].getAttribute("data-mark")).toBe("attention");

    const ledger = screen.getByTestId("ledger-commits");
    const rows = within(ledger).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    // untrusted text is plain text
    expect(rows[0].textContent).toContain("scale shop-api to 3 <b>replicas</b>");
    expect(rows[0].querySelector("b")).toBeNull();
    const dots = [...rows[0].querySelectorAll(".ledger-dot")].map((d) => d.hasAttribute("data-holds"));
    expect(dots).toEqual([true, false, false]);
    expect(screen.getByText("commits not yet everywhere · main")).toBeTruthy();
    fireEvent.click(within(rows[0]).getByRole("button", { name: "a1b9e3c" }));
    expect(rt.opened).toEqual([`https://github.com/acme/fleet/commit/${T}`]);
  });

  it("never shows an ordering beside another line's facts", () => {
    applyLine(line(4));
    applyLineGit(git(3)); // answers LineDTO 3; the board is at 4
    render(<FleetView />);
    expect(screen.queryByTestId("line-target")).toBeNull();
    expect(screen.queryByTestId("ledger-commits")).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: /9f2c11d is applied on 2 of 3 compared stations/ })).toBeTruthy();
  });

  it("says why the line is not ordered, beside the revisions on the line now", () => {
    applyLine(line(5));
    applyLineGit({ ...git(5), state: "unapproved", approve: "github.com/acme/fleet", stations: [], ledger: [], target: undefined } as unknown as LineGitDTO);
    render(<FleetView />);
    expect(screen.getByTestId("line-history").textContent).toBe("Git history: approve github.com/acme/fleet in Settings > Git to order the line by its commits");
    expect(screen.getByTestId("ledger-history-note")).toBeTruthy();
    expect(screen.getByText("revisions on the line now")).toBeTruthy();
  });

  it("asks for due fetches on mount and every 5 minutes while open", () => {
    vi.useFakeTimers();
    try {
      applyLine(line(6));
      const { unmount } = render(<FleetView />);
      expect(rt.due).toBe(1);
      vi.advanceTimersByTime(5 * 60 * 1000);
      expect(rt.due).toBe(2);
      unmount();
      vi.advanceTimersByTime(10 * 60 * 1000);
      expect(rt.due).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
