import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { LensIndex } from "./LensIndex";
import { useListKeys } from "./useListKeys";
import { addSub, applyRows, applySummary, openStarted, openSucceeded, resetHealthStore } from "../store/health";
import { core, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { toggleLensIndex, useLensIndexOpen } from "../nav/lensIndexOpen";
import { coverageOf } from "../nav/lensIndex";
import { handleShellKey } from "../app/AppShell";

vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));

// The Line slice 4 §4: the lens index replaces NavBand.

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "dev", state: "Synced", reason: "", nodesReady: 3, nodesTotal: 3, pods: 42, version: "v1.31",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "dev", region: "weu", provider: "aks", group: "aks", ageSeconds: 10,
  fluxPresent: true, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false,
  ...over,
});
const at = (section: string, extra: object = {}) => useFleet.setState({ route: { name: "cluster", cluster: "dev", section, ...extra } as never });
const routeOf = () => useFleet.getState().route;
const entry = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}`) });
const sum = (tasks: Parameters<typeof summary>[0]) => summary(tasks, {}, "dev");
const flux = (fault: number) => task("flux", [native("reconciliations", { fault, ok: 2 }, fault, "not ready", [kind("kustomizations")]), native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])]);

beforeEach(() => {
  resetHealthStore();
  useLensIndexOpen.setState({ open: true });
  useFleet.setState((s) => ({ clusters: [cluster()], route: { name: "fleet" }, forwards: [], newContexts: 0,
    workloads: { ...s.workloads, kindFilter: { Deployment: true, StatefulSet: true, DaemonSet: true } } }));
});

describe("the lens index", () => {
  it("is a labelled navigation landmark with Needs you and Everything else; the current lens is aria-current", () => {
    at("pods");
    applySummary(sum([...replace(core(), task("workloads", [verdict("workloads", { fault: 2, ok: 3 }, [kind("deployments")])])), flux(0)]));
    render(<LensIndex />);
    const nav = screen.getByRole("navigation", { name: "Lenses" });
    expect(within(nav).getAllByRole("heading").map((h) => h.textContent)).toEqual(["Needs you", "Everything else"]);
    const needs = within(nav).getByRole("heading", { name: "Needs you" }).parentElement as HTMLElement;
    const wl = within(needs).getByRole("button", { name: /^Workloads/ });
    expect(wl.textContent).toBe("Workloads2");
    expect(wl.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(entry("Pods").getAttribute("aria-current")).toBe("page");
  });

  it("selecting a lens opens it; a tool lens opens its first task, and its parts are switched in the lens, not here (slice 13b.1)", () => {
    at("overview");
    applySummary(sum([...core(), flux(0)]));
    const { rerender } = render(<LensIndex />);
    expect(screen.queryByRole("button", { name: /^Sources/ })).toBeNull();
    fireEvent.click(entry("Flux CD"));
    const r = routeOf();
    expect(r.name === "cluster" && r.section === "gitops" && (r.fluxView ?? "reconciliation")).toBe("reconciliation");
    rerender(<LensIndex />);
    expect(entry("Flux CD").getAttribute("aria-current")).toBe("page");
    expect(screen.queryByRole("button", { name: /^Sources/ })).toBeNull();
    // on another part the lens stays the current entry
    at("gitops", { fluxView: "sources" });
    rerender(<LensIndex />);
    expect(entry("Flux CD").getAttribute("aria-current")).toBe("page");
    expect(screen.queryByRole("button", { name: /^Sources/ })).toBeNull();
  });

  it("checking stays listed and disabled; an absent tool is named once, not listed", () => {
    useFleet.setState({ clusters: [cluster({ state: "Connecting" })] });
    at("overview");
    const r1 = render(<LensIndex />);
    const fx = entry("Flux CD") as HTMLButtonElement;
    expect(fx.disabled).toBe(true);
    expect(fx.textContent).toContain("checking…");
    r1.unmount();
    useFleet.setState({ clusters: [cluster()] });
    render(<LensIndex />);
    expect(screen.queryByRole("button", { name: /^Argo CD/ })).toBeNull();
    expect(screen.getByText(/Argo CD.* are not installed here, so they are not listed\./)).toBeTruthy();
  });

  it("a focused entry owns its keys: Enter, j/k and lens verbs never reach the list", () => {
    at("pods");
    const onActivate = vi.fn();
    const onSelect = vi.fn();
    const verb = vi.fn();
    function List() {
      useListKeys({ count: 3, selected: 0, onSelect, onActivate, extraKeys: { r: verb, s: verb } });
      return null;
    }
    render(<><LensIndex /><List /></>);
    const el = entry("Pods");
    el.focus();
    for (const key of ["Enter", "j", "k", "r", "s"]) fireEvent.keyDown(el, { key });
    expect(onActivate).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
    expect(verb).not.toHaveBeenCalled();
  });

  it("the Pods count is the selector's: the summary's, or newer lens rows; another cluster's read shows none", () => {
    at("overview");
    applySummary(sum([...replace(core(), task("pods", [verdict("pods", { fault: 2 }, [kind("pods")])])), flux(0)]));
    const r1 = render(<LensIndex />);
    expect(entry("Pods").textContent).toBe("Pods2");
    r1.unmount();
    addSub("p", "dev", "pods", "pods"); openStarted("p", "t"); openSucceeded("p", "t", 1);
    applyRows("p", { cluster: "dev", incarnation: 1, task: "pods", part: { ...verdict("pods", { fault: 3 }, [kind("pods")]), ver: 2, obs: 2 }, evaluatedAt: "x", rows: [], full: true });
    const r2 = render(<LensIndex />);
    expect(entry("Pods").textContent).toBe("Pods3");
    r2.unmount();
    resetHealthStore();
    applySummary(summary([...replace(core(), task("pods", [verdict("pods", { fault: 2 }, [kind("pods")])])), flux(0)], {}, "prd"));
    render(<LensIndex />);
    expect(entry("Pods").textContent).not.toMatch(/\d/);
  });

  it("fleet and app routes list the fleet views, the current one marked", () => {
    useFleet.setState({ route: { name: "fleet", view: "promotion" } });
    const r1 = render(<LensIndex />);
    expect(entry("Promotion tracker").getAttribute("aria-current")).toBe("page");
    fireEvent.click(entry("Expiry horizon"));
    expect(routeOf()).toEqual({ name: "fleet", view: "expiry" });
    r1.unmount();
    useFleet.setState({ route: { name: "settings" } });
    render(<LensIndex />);
    expect(screen.getAllByRole("button").map((b) => b.getAttribute("aria-current"))).toEqual([null, null, null]);
  });
});

describe("collapse (§4.3)", () => {
  it("mod+\\ and the toggle hide and show it; collapsed is hidden, and the choice persists", () => {
    at("overview");
    localStorage.removeItem("klyx.layout.v1.lensIndex");
    const { container } = render(<LensIndex />);
    expect(container.querySelector("#lens-index")).toBeTruthy();
    act(() => { handleShellKey(new KeyboardEvent("keydown", { key: "\\", metaKey: true })); });
    expect(container.querySelector("#lens-index")).toBeNull();
    expect(localStorage.getItem("klyx.layout.v1.lensIndex")).toBe("0");
    act(() => { toggleLensIndex(); });
    expect(container.querySelector("#lens-index")).toBeTruthy();
    expect(localStorage.getItem("klyx.layout.v1.lensIndex")).toBe("1");
  });
});

describe("focus on collapse (Codex review of 4b)", () => {
  it("hiding the index with mod+\\ while an entry has focus moves focus to the disclosure", async () => {
    const { LocationBar } = await import("./LocationBar");
    at("pods");
    const r = render(<><LocationBar /><LensIndex /></>);
    const pods = entry("Pods");
    pods.focus();
    expect(document.activeElement).toBe(pods);
    act(() => { handleShellKey(new KeyboardEvent("keydown", { key: "\\", metaKey: true })); });
    expect(r.container.querySelector("#lens-index")).toBeNull();
    const toggle = screen.getByRole("button", { name: "Lens index" });
    expect(document.activeElement).toBe(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  it("hiding it with focus elsewhere leaves focus where it is", async () => {
    const { LocationBar } = await import("./LocationBar");
    at("pods");
    render(<><LocationBar /><LensIndex /><input aria-label="elsewhere" /></>);
    const other = screen.getByRole("textbox", { name: "elsewhere" });
    other.focus();
    act(() => { toggleLensIndex(); });
    expect(document.activeElement).toBe(other);
  });
});

describe("a row's notes (slice 13a.5)", () => {
  it("keeps the coverage words that qualify a count, and leaves a part's explanation to the title", () => {
    expect(coverageOf("unverified · routes: 3 with a failing attachment")).toBe("unverified");
    expect(coverageOf("controller not ready · issuers: 1 not ready")).toBe("controller not ready");
    expect(coverageOf("routes: 3 with a failing attachment")).toBe("");
    expect(coverageOf("last good")).toBe("last good");
    expect(coverageOf(undefined)).toBe("");
  });
});
