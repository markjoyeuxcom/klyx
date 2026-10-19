import { describe, it, expect, vi, beforeEach } from "vitest";
import { useState } from "react";
import { render, fireEvent, act, screen, within } from "@testing-library/react";

// The Line slice 9c (parent §6, slice 9 note §4): the command console.
const index = vi.hoisted(() => {
  type Reply = { resolve: (v: unknown) => void; q: string; cancelled: boolean };
  const state = { replies: [] as Reply[], auto: true as boolean, data: (q: string) => ({ hits: [] as unknown[], total: 0, searchedClusters: 0, searchedKinds: 0, coverage: [] as unknown[], q }) };
  return {
    state,
    searchIndex: vi.fn((q: string) => {
      let resolve!: (v: unknown) => void;
      const p = new Promise((r) => { resolve = r; });
      const reply = { resolve, q, cancelled: false };
      state.replies.push(reply);
      if (state.auto) resolve(state.data(q));
      return Object.assign(p, { cancel: () => { reply.cancelled = true; } });
    }),
  };
});
vi.mock("../../bridge/fleetIndex", () => ({ searchIndex: index.searchIndex }));
vi.mock("../../bridge/pods", () => ({ openPodDetail: vi.fn(), listPods: vi.fn() }));
vi.mock("../../bridge/helm", () => ({ openHelmRelease: vi.fn() }));

import { CommandPalette } from "../CommandPalette";
import { useFleet, type ClusterDTO, type PodRowDTO } from "../../store/fleet";
import { useHistory, _resetHistoryForTest } from "../../nav/history";
import { publishPin, INSPECTOR_IDENTITY_ID } from "../../nav/inspectorPin";
import { useLine, type LineDTO } from "../../store/line";
import type { IndexHit, IndexSearch } from "../../bridge/fleetIndex";
import { openHit, lensOf } from "./openHit";
import { coverageNotes } from "./consoleModel";
import { LensLayout } from "../../layout/LensLayout";

const cluster = (name: string, over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, env: "", region: "", state: "Synced", reason: "", ...over } as ClusterDTO);
const hit = (over: Partial<IndexHit> = {}): IndexHit => ({
  cluster: "prd", kind: "Pod", group: "", version: "v1", resource: "pods", namespace: "shop", name: "web-1", uid: "u1", kindState: "live",
  role: { state: "current", role: "fault", label: "CrashLoopBackOff" }, ...over,
});
const search = (hits: IndexHit[], over: Partial<IndexSearch> = {}): IndexSearch => ({ hits, total: hits.length, searchedClusters: 2, searchedKinds: 30, coverage: [], ...over });

const pressCmdK = () => fireEvent.keyDown(window, { key: "k", metaKey: true });
const input = () => screen.getByTestId("command-palette-input");
const type = (v: string) => fireEvent.change(input(), { target: { value: v } });
const sections = () => [...document.querySelectorAll('#command-palette-list [role="group"]')].map((g) => g.getAttribute("aria-label"));
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 200)); });

const realOpenCluster = useFleet.getState().openCluster;
beforeEach(() => {
  vi.clearAllMocks();
  index.state.replies = [];
  index.state.auto = true;
  index.state.data = (q: string) => ({ hits: [], total: 0, searchedClusters: 2, searchedKinds: 30, coverage: [], q } as never);
  _resetHistoryForTest();
  useLine.setState({ dto: null });
  useFleet.getState().clearPods();
  useFleet.setState({ clusters: [cluster("prd"), cluster("stg")], route: { name: "fleet" }, navIntent: null, openCluster: realOpenCluster });
});

describe("the console's frame", () => {
  it("is 760px wide, 88px from the top, a dialog that traps Tab", () => {
    render(<CommandPalette />);
    pressCmdK();
    const dialog = screen.getByRole("dialog", { name: "Command console" });
    expect(dialog.style.width).toBe("760px");
    expect(screen.getByTestId("command-palette").style.paddingTop).toBe("88px");
    // Tab from the last control returns to the first
    const toggle = screen.getByTestId("verbs-only");
    toggle.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(document.activeElement).toBe(input());
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(toggle);
  });
});

describe("the empty query", () => {
  it("lists recent places, then the fleet's affected clusters, then the verbs for the selection", () => {
    useFleet.setState({ clusters: [cluster("prd", { state: "Failed", reason: "dial tcp" }), cluster("stg")] });
    useHistory.setState({ recent: [{ route: { name: "cluster", cluster: "stg", section: "pods" } }] });
    useFleet.getState().setPodsVisit("stg");
    useFleet.getState().setPodsFromRows("stg", { items: [{ namespace: "shop", name: "api-1", uid: "uid-api-1", health: { kind: "ok", label: "Running" } } as PodRowDTO], namespaces: ["shop"], loading: false });
    const release = publishPin({ cluster: "stg", uid: "uid-api-1", kind: "Pod", namespace: "shop", name: "api-1" });
    render(<CommandPalette />);
    pressCmdK();
    expect(sections()).toEqual(["Recent places", "Affected clusters", "Run · Pod shop/api-1"]);
    const run = within(screen.getByRole("group", { name: "Run · Pod shop/api-1" }));
    expect(run.getByText("logs shop/api-1")).toBeTruthy();
    expect(within(screen.getByRole("group", { name: "Affected clusters" })).getByText("prd")).toBeTruthy();
    release();
  });

  it("verbs only (⌥V or the button) hides every group but Run", () => {
    render(<CommandPalette />);
    pressCmdK();
    type("prd");
    expect(sections()).toContain("Go to");
    fireEvent.keyDown(input(), { key: "v", code: "KeyV", altKey: true });
    expect(screen.getByTestId("verbs-only").getAttribute("aria-pressed")).toBe("true");
    expect(sections().every((s) => s === "Run")).toBe(true);
    fireEvent.click(screen.getByTestId("verbs-only"));
    expect(sections()).toContain("Go to");
  });
});

describe("object rows from the fleet index", () => {
  it("say the Observer's word, the cluster's place on the line and the evidence's age", async () => {
    useLine.setState({ dto: { version: 1, tracks: [{ name: "weu", label: "West Europe" }], stations: [{ cluster: "prd", track: "weu", rank: 2, env: "prd" }, { cluster: "stg", track: "weu", rank: 1, env: "stg" }], siding: [] } as unknown as LineDTO });
    index.state.data = () => search([
      hit(),
      hit({ cluster: "stg", uid: "u2", role: { state: "lastObserved", role: "ok", at: "2026-10-15T12:00:00Z" }, kindState: "stale", asOf: "2026-10-15T12:01:00Z" }),
      hit({ kind: "ConfigMap", resource: "configmaps", name: "web-config", uid: "u3", role: { state: "unverified", reason: "no ledger for this kind" } }),
    ]) as never;
    render(<CommandPalette />);
    pressCmdK();
    type("web");
    await flush();
    const fault = screen.getByTestId("console-hit-prd-u1");
    expect(fault.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(fault.querySelector('[data-linepos="prd"]')).toBeTruthy();
    expect(fault.querySelector("[data-hit-note]")?.textContent).toBe("shop · CrashLoopBackOff");
    const last = screen.getByTestId("console-hit-stg-u2");
    expect(last.querySelector('[data-aspect="checking"]')).toBeTruthy(); // never healthy
    expect(last.querySelector("[data-hit-note]")?.textContent).toMatch(/^shop · last observed ok at \d\d:\d\d · stale as of \d\d:\d\d$/);
    expect(screen.getByTestId("console-hit-prd-u3").querySelector("[data-hit-note]")?.textContent).toBe("shop · health unverified");
    expect(sections()[0]).toBe("Objects");
  });

  it("the lens index's name for a lens leads, ahead of objects that merely contain it", async () => {
    index.state.data = () => search([hit({ kind: "Namespace", resource: "namespaces", namespace: "", name: "helm-system", uid: "ns1" })]) as never;
    useFleet.setState({ route: { name: "cluster", cluster: "prd", section: "pods" } });
    render(<CommandPalette />);
    pressCmdK();
    type("Helm");
    await flush();
    expect(sections()[0]).toBe("Go to");
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "helm" });
  });

  it("a query that starts a command's title leads with its group", async () => {
    index.state.data = () => search([hit({ name: "fleet-agent" })]) as never;
    render(<CommandPalette />);
    pressCmdK();
    type("fleet board");
    await flush();
    expect(sections()[0]).toBe("Go to");
  });

  it("↵ opens the object pinned to its UID and closes; ⌘↵ opens it beside and keeps the console, then focus goes to the inspector", async () => {
    index.state.data = () => search([hit()]) as never;
    render(<>
      <div id={INSPECTOR_IDENTITY_ID} tabIndex={-1} data-testid="identity" />
      <CommandPalette />
    </>);
    pressCmdK();
    type("web");
    await flush();
    fireEvent.keyDown(input(), { key: "Enter", metaKey: true });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: "prd", namespace: "shop", name: "web-1", uid: "u1" });
    expect(screen.getByTestId("command-palette")).toBeTruthy(); // kept open
    expect(screen.getByTestId("command-palette").style.justifyContent).toBe("flex-start"); // aside: the inspector stays readable
    fireEvent.keyDown(window, { key: "Escape" });
    expect(document.activeElement).toBe(screen.getByTestId("identity"));
    useFleet.setState({ navIntent: null });
    pressCmdK();
    type("web");
    await flush();
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", uid: "u1" });
    expect(screen.queryByTestId("command-palette")).toBeNull();
  });

  it("says what it searched, and every gap, with the lens to search instead", async () => {
    index.state.data = () => search([], {
      searchedClusters: 1, searchedKinds: 2,
      coverage: [
        { cluster: "prd", indexed: true, connection: "connected", kinds: [{ key: "/secrets", kind: "Secret", state: "denied", entries: 0 }, { key: "/pods", kind: "Pod", state: "capped", entries: 40000 }] },
        { cluster: "dr", indexed: false, connection: "unreachable", kinds: [] },
      ],
    }) as never;
    render(<CommandPalette />);
    pressCmdK();
    await flush();
    const cov = screen.getByTestId("console-coverage");
    expect(cov.textContent).toContain("Searching 1 cluster · 2 kinds");
    expect(cov.querySelector("[title]")?.getAttribute("title")).toBe([
      "prd: Secret not permitted - search in Resources",
      "prd: Pod capped at 40,000 - search in Pods",
      "dr: not indexed (unreachable)",
    ].join("\n"));
  });
});

describe("the search fence", () => {
  it("a reply applies only to the latest request: A -> B -> A never shows the first A's reply", async () => {
    index.state.auto = false;
    render(<CommandPalette />);
    pressCmdK();
    await flush(); // the open's coverage-only read
    type("aaa");
    await flush();
    type("bbb");
    await flush();
    type("aaa");
    await flush();
    const [, a1, b, a2] = index.state.replies;
    expect([a1.q, b.q, a2.q]).toEqual(["aaa", "bbb", "aaa"]);
    expect(a1.cancelled && b.cancelled).toBe(true); // each superseded read is cancelled in Go
    await act(async () => { a2.resolve(search([hit({ name: "second" })])); });
    await act(async () => { a1.resolve(search([hit({ name: "first", uid: "stale" })])); });
    expect(screen.queryByTestId("console-hit-prd-stale")).toBeNull();
    expect(screen.getByTestId("console-hit-prd-u1")).toBeTruthy();
  });
});

describe("opening a hit", () => {
  it("reveals a kind with a lens of its own; opens every other kind in Resources, expecting its UID", () => {
    useFleet.setState({ route: { name: "fleet" } });
    openHit(hit({ group: "cert-manager.io", version: "v1", resource: "certificates", kind: "Certificate", name: "tls", uid: "c1" }));
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "reveal", task: "certs", part: "certificates", ref: { gvr: "cert-manager.io/v1/certificates", uid: "c1" } });
    openHit(hit({ kind: "ConfigMap", resource: "configmaps", name: "settings", uid: "cm1" }));
    const r = useFleet.getState().route;
    expect(r).toMatchObject({ name: "cluster", cluster: "prd", section: "resources", resource: { plural: "configmaps", scope: "Namespaced" }, instance: { namespace: "shop", name: "settings", expectUid: "cm1" } });
    expect(lensOf("", "configmaps")).toBe("Resources");
    expect(lensOf("apps", "deployments")).toBe("Workloads");
  });

  it("a kind Klyx's budget truncated is said to be partly indexed (coverage S7a)", () => {
    expect(coverageNotes([{ cluster: "a", indexed: true, connection: "connected", kinds: [{ key: "/namespaces", kind: "Namespace", state: "partial", entries: 1583 }] }]))
      .toEqual([expect.stringMatching(/^a: Namespace partly indexed \(1,583 kept within Klyx's budget; more exist\) - search in /)]);
  });

  it("coverage notes skip kinds that are simply not served", () => {
    expect(coverageNotes([{ cluster: "a", indexed: true, connection: "connected · flux degraded", kinds: [{ key: "x/y", kind: "Y", state: "notServed", entries: 0 }] }])).toEqual([]);
  });
});

describe("hits belong to the visible query", () => {
  it("an old query's hit can be neither shown nor opened while the new query is pending", async () => {
    index.state.data = () => search([hit({ name: "aaa-pod", uid: "old-query" })]) as never;
    render(<CommandPalette />);
    pressCmdK();
    type("aaa");
    await flush();
    expect(screen.getByTestId("console-hit-prd-old-query")).toBeTruthy();
    index.state.auto = false;
    type("bbb");
    expect(screen.queryByTestId("console-hit-prd-old-query")).toBeNull(); // at once, not after the debounce
    await flush();
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(useFleet.getState().navIntent).toBeNull();
    // the index's coverage stays readable while the new query is pending
    expect(screen.getByTestId("console-coverage").textContent).toContain("Searching 2 clusters · 30 kinds · searching…");
  });

  it("a failed replacement search is shown, never hidden behind the old result", async () => {
    index.state.data = () => search([hit({ name: "aaa-pod", uid: "old-query" })]) as never;
    render(<CommandPalette />);
    pressCmdK();
    type("aaa");
    await flush();
    index.searchIndex.mockImplementationOnce(() => Object.assign(Promise.reject(new Error("index unavailable")), { cancel: () => {} }) as never);
    type("bbb");
    await flush();
    expect(screen.getByTestId("console-coverage").textContent).toBe("The fleet index could not be searched: index unavailable");
    expect(screen.queryByTestId("console-hit-prd-old-query")).toBeNull();
  });
});

describe("verbs follow the inspector's current evidence", () => {
  const podRows = (name: string, uid: string) => ({ items: [{ namespace: "shop", name, uid, health: { kind: "ok", label: "Running" } } as PodRowDTO], namespaces: ["shop"], loading: false });

  it("offers the verbs of an inspector whose rows arrive while the console is open", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "stg", section: "pods" } });
    useFleet.getState().setPodsVisit("stg");
    render(<CommandPalette />);
    pressCmdK();
    let release = () => {};
    act(() => {
      useFleet.getState().setPodsFromRows("stg", podRows("fresh", "uid-fresh"));
      release = publishPin({ cluster: "stg", kind: "Pod", namespace: "shop", name: "fresh", uid: "uid-fresh" });
    });
    expect(screen.getByText("logs shop/fresh")).toBeTruthy();
    act(() => release());
  });

  it("drops the old pin's verbs when the loaded name is now another object", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "stg", section: "pods" } });
    useFleet.getState().setPodsVisit("stg");
    useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-old"));
    const release = publishPin({ cluster: "stg", kind: "Pod", namespace: "shop", name: "same", uid: "uid-old" });
    render(<CommandPalette />);
    pressCmdK();
    expect(screen.getByText("logs shop/same")).toBeTruthy();
    act(() => useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-new")));
    expect(screen.queryByText("logs shop/same")).toBeNull();
    act(() => release());
  });

  it("a verb whose object changed before the list caught up runs nothing and says why", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "stg", section: "pods" } });
    useFleet.getState().setPodsVisit("stg");
    useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-old"));
    render(<CommandPalette />);
    pressCmdK();
    type("logs shop/same");
    // the object is replaced and Enter lands in the same tick, before a render
    act(() => {
      useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-new"));
      fireEvent.keyDown(input(), { key: "Enter" });
    });
    expect(useFleet.getState().navIntent).toBeNull();
    expect(screen.getByRole("status").textContent).toContain("its object changed while the console was open");
  });

  it("rows that change nothing a verb names leave the commands as they are", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "stg", section: "pods" } });
    useFleet.getState().setPodsVisit("stg");
    useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-1"));
    render(<CommandPalette />);
    pressCmdK();
    type("logs shop/same");
    const before = screen.getByTestId("command-row-verb:logs:stg/shop/same");
    act(() => useFleet.getState().setPodsFromRows("stg", podRows("same", "uid-1")));
    expect(screen.getByTestId("command-row-verb:logs:stg/shop/same")).toBe(before);
  });
});

describe("Escape belongs to the console above a drawer", () => {
  it("closes the console and keeps the inspector open", () => {
    vi.stubGlobal("ResizeObserver", class {
      cb: ResizeObserverCallback;
      constructor(cb: ResizeObserverCallback) { this.cb = cb; }
      observe() { this.cb([{ contentRect: { width: 900 } } as ResizeObserverEntry], this as unknown as ResizeObserver); }
      unobserve() {}
      disconnect() {}
    });
    const closed = vi.fn();
    function Page() {
      const [shown, setShown] = useState(true);
      return (
        <>
          <LensLayout label="Pods" state="ready" title="Pods" body={<div>rows</div>}
            inspector={shown ? <div id={INSPECTOR_IDENTITY_ID} tabIndex={-1}>identity</div> : undefined}
            onCloseInspector={() => { closed(); setShown(false); }} />
          <CommandPalette />
        </>
      );
    }
    try {
      render(<Page />);
      pressCmdK();
      fireEvent.keyDown(input(), { key: "Escape" });
      expect(screen.queryByTestId("command-palette")).toBeNull();
      expect(closed).not.toHaveBeenCalled();
      expect(screen.getByTestId("lens-inspector")).toBeTruthy();
      // the next Escape is the drawer's again
      fireEvent.keyDown(window, { key: "Escape" });
      expect(closed).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
