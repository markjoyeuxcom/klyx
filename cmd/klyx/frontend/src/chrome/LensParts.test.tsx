import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, kind, native, summary, task } from "../test/healthFixtures";
import { LensParts } from "./LensParts";
import { useListKeys } from "./useListKeys";
import { computeAccessibleName } from "dom-accessibility-api";

vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));

// The Line slice 13b.1: a tool lens's part switch - one button per task,
// each with navCounts' own count, the route's part pressed.

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "dev", state: "Synced", reason: "", nodesReady: 3, nodesTotal: 3, pods: 42, version: "v1.31",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "dev", region: "weu", provider: "aks", group: "aks", ageSeconds: 10,
  fluxPresent: true, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false,
  ...over,
});
const at = (section: string, extra: object = {}) => useFleet.setState({ route: { name: "cluster", cluster: "dev", section, ...extra } as never });
const flux = (recs: number, srcs: number) => task("flux", [native("reconciliations", { fault: recs, ok: 2 }, recs, "not ready", [kind("kustomizations")]), native("sources", { fault: srcs, ok: 1 }, srcs, "not ready", [kind("gitrepositories")])]);
const part = (name: RegExp) => screen.getByRole("button", { name });

beforeEach(() => {
  resetHealthStore();
  useFleet.setState({ clusters: [cluster()] });
});

describe("the part switch", () => {
  it("one button per task, each with its own count, never a sum; the route's part is pressed", () => {
    at("gitops", { fluxView: "reconciliation" });
    applySummary(summary([...core(), flux(2, 1)], {}, "dev"));
    render(<LensParts cluster="dev" groupId="flux" />);
    expect(screen.getByRole("group", { name: "Flux CD parts" })).toBeTruthy();
    expect(part(/^Reconciliation/).textContent).toBe("Reconciliation2");
    expect(part(/^Sources/).textContent).toBe("Sources1");
    expect(part(/^Reconciliation/).getAttribute("aria-pressed")).toBe("true");
    expect(part(/^Sources/).getAttribute("aria-pressed")).toBe("false");
  });

  it("selecting a part opens it", () => {
    at("gitops", { fluxView: "reconciliation" });
    applySummary(summary([...core(), flux(0, 0)], {}, "dev"));
    render(<LensParts cluster="dev" groupId="flux" />);
    fireEvent.click(part(/^Sources/));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.section === "gitops" && r.fluxView).toBe("sources");
  });

  it("a lens with one task has no switch", () => {
    at("helm");
    const { container } = render(<LensParts cluster="dev" groupId="helm" />);
    expect(container.innerHTML).toBe("");
  });
});

// Slice 13b review 1: the switch owns its keys, and a count's tone is never
// colour alone.
describe("the part switch's keys and marks", () => {
  function WithList({ onSelect, onActivate, verb }: { onSelect: () => void; onActivate: () => void; verb: () => void }) {
    // a list with its first row selected, as a lens with an open inspector has
    useListKeys({ count: 3, selected: 0, onSelect, onActivate, extraKeys: { r: verb } });
    return <LensParts cluster="dev" groupId="flux" />;
  }

  it("a focused part keeps Enter, j, k and the row verbs from the list behind it", () => {
    at("gitops", { fluxView: "reconciliation" });
    applySummary(summary([...core(), flux(2, 1)], {}, "dev"));
    const onSelect = vi.fn(), onActivate = vi.fn(), verb = vi.fn();
    render(<WithList onSelect={onSelect} onActivate={onActivate} verb={verb} />);
    const sources = part(/^Sources/);
    sources.focus();
    for (const key of ["Enter", "j", "k", "r"]) {
      const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
      sources.dispatchEvent(e);
      expect(e.defaultPrevented).toBe(false); // Enter stays the button's own
    }
    expect(onActivate).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
    expect(verb).not.toHaveBeenCalled();
  });

  it("a fault count and a look-at count carry their shape and name; a neutral count is bare", () => {
    at("gitops", { fluxView: "reconciliation" });
    const recs = native("reconciliations", { fault: 1, ok: 2 }, 1, "not ready", [kind("kustomizations")]);
    const srcs = native("sources", { degraded: 1, ok: 1 }, 1, "stale", [kind("gitrepositories")]);
    applySummary(summary([...core(), task("flux", [recs, srcs])], {}, "dev"));
    render(<LensParts cluster="dev" groupId="flux" />);
    expect(part(/^Reconciliation/).querySelector("[data-aspect]")?.getAttribute("data-aspect")).toBe("fault");
    expect(part(/^Sources/).querySelector("[data-aspect]")?.getAttribute("data-aspect")).toBe("attention");
    // the accessible names differ, not only the colours
    expect(screen.getAllByRole("button").map((b) => computeAccessibleName(b))).toEqual(["Reconciliation, 1 fault", "Sources, 1 to look at"]);
  });
});
