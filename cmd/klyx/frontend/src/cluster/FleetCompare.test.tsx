import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, within, fireEvent, act, waitFor } from "@testing-library/react";
import { useFleet, WorkloadCompareDTO, ClusterDTO } from "../store/fleet";
import { FleetCompare, diffAgainst } from "./FleetCompare";

vi.mock("../bridge/workloads", () => ({
  workloadCompare: vi.fn(async () => ({}) as WorkloadCompareDTO),
}));
import { workloadCompare } from "../bridge/workloads";

const cl = (name: string, env: string): ClusterDTO => ({
  name, state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  env, region: "westeurope", group: "", provider: "", ageSeconds: 0,
});

const result = (over: Partial<WorkloadCompareDTO> = {}): WorkloadCompareDTO => ({
  found: true, kind: "Deployment", namespace: "shop", name: "cart",
  desiredReplicas: 2, readyReplicas: 2,
  containers: [{ name: "app", image: "shop/cart:v1.2.3", resources: "req 100m/128Mi" }],
  specHash: "aaaa11112222", ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "apps",
  ownerRevision: "main@sha1:abc", error: "", ...over,
});

describe("FleetCompare", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ clusters: [cl("prd-we", "prd"), cl("dev-we", "dev"), cl("stg-we", "stg")] });
  });

  it("orders columns dev -> stg -> prd and marks the origin", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) => result({ name: cluster }));
    const { findAllByTestId, getByTestId } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    const cols = await findAllByTestId(/compare-col-/);
    expect(cols.map((c) => c.getAttribute("data-testid"))).toEqual([
      "compare-col-dev-we", "compare-col-stg-we", "compare-col-prd-we",
    ]);
    expect(within(getByTestId("compare-col-dev-we")).getByText("origin")).toBeTruthy();
  });

  it("shows image + revision values per column and honest not-deployed/unreadable states", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) => {
      if (cluster === "dev-we") return result();
      if (cluster === "stg-we") return result({ found: false, containers: [], specHash: "", ownerKind: "", ownerRevision: "" });
      return result({ found: false, containers: [], error: "deployments is forbidden" });
    });
    const { findByText, getByTestId } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    expect(await findByText("shop/cart:v1.2.3")).toBeTruthy();
    expect(await findByText("main@sha1:abc")).toBeTruthy();
    expect(within(getByTestId("compare-col-stg-we")).getByText("Not deployed on this cluster.")).toBeTruthy();
    // A failed lookup is unreadable, never "not deployed".
    expect(within(getByTestId("compare-col-prd-we")).getByText("Could not read: deployments is forbidden")).toBeTruthy();
  });

  it("marks differences with a shape against the origin; replicas stay neutral-text", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) =>
      cluster === "prd-we" ? result({ desiredReplicas: 6, readyReplicas: 6, containers: [{ name: "app", image: "shop/cart:v1.2.2", resources: "req 100m/128Mi" }], ownerRevision: "main@sha1:old", specHash: "cccc" }) : result());
    const { getByTestId, findByText } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
    await findByText("≠ 1 differs from dev-we");
    const prd = within(getByTestId("compare-col-prd-we"));
    expect(prd.getByText(/shop\/cart:v1.2.2/).textContent).toBe("≠ shop/cart:v1.2.2");
    expect(prd.getByText(/main@sha1:old/).textContent).toBe("≠ main@sha1:old");
    expect(prd.getByText(/6\/6 ready/).textContent).toBe("≠ 6/6 ready");
    // the image differs, so the spec-hash difference is not double-counted as "other"
    expect(prd.queryByText(/differs beyond images/)).toBeNull();
    expect(within(getByTestId("compare-col-stg-we")).queryByText(/≠/)).toBeNull();
  });

  it("an unreadable or absent origin computes no differences", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) =>
      cluster === "dev-we" ? result({ found: false, containers: [], error: "timeout" }) : result({ specHash: cluster }));
    const { findByText, getAllByTestId } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
    expect(await findByText(/Differences are not computed: the origin dev-we could not be read \(timeout\)/)).toBeTruthy();
    expect(getAllByTestId(/compare-col-/).some((c) => c.textContent?.includes("≠"))).toBe(false);
  });

  it("unresolved revisions are never compared; DaemonSet counts are not compared", () => {
    const a = result({ ownerRevision: "" }), b = result({ ownerRevision: "main@sha1:zzz" });
    expect(diffAgainst(a, b).revision).toBe(false);
    const ds = (n: number) => result({ kind: "DaemonSet", desiredReplicas: n, readyReplicas: n });
    expect(diffAgainst(ds(3), ds(12)).replicas).toBe(false);
    expect(diffAgainst(result(), result({ containers: [] })).missing).toEqual(["app"]);
  });

  it("an unresolved revision says why", async () => {
    vi.mocked(workloadCompare).mockImplementation(async () => result({ ownerRevision: "", ownerRevisionNote: "revision unreadable: forbidden" }));
    const { findAllByText } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
    expect((await findAllByText("revision unreadable: forbidden - not compared")).length).toBe(3);
  });

  it("an answer for a previous workload identity is dropped; Escape closes", async () => {
    let releaseOld: (v: WorkloadCompareDTO) => void = () => {};
    vi.mocked(workloadCompare).mockImplementation((cluster, _k, _ns, name) => {
      if (cluster === "prd-we" && name === "cart") return new Promise((r) => { releaseOld = r; });
      return Promise.resolve(result({ name, containers: [{ name: "app", image: `${name}:1`, resources: "" }] }));
    });
    const onClose = vi.fn();
    const { getByRole, findByText, getByTestId, rerender } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={onClose} />);
    await findByText("1 reading…");
    rerender(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="checkout" onClose={onClose} />);
    await findByText(/no differences found/);
    await act(async () => { releaseOld(result({ containers: [{ name: "app", image: "cart:late", resources: "" }] })); });
    expect(within(getByTestId("compare-col-prd-we")).queryByText(/cart:late/)).toBeNull();
    expect(within(getByTestId("compare-col-prd-we")).getByText("checkout:1")).toBeTruthy();
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("flags other spec drift when hashes differ but images and resources match", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) =>
      cluster === "prd-we" ? result({ specHash: "bbbb33334444" }) : result());
    const { getByTestId, queryByText } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    // Wait for the asserted column's own result - "origin" appears as soon as
    // the origin column lands, which under load can precede prd-we's read.
    expect(await within(getByTestId("compare-col-prd-we")).findByText(/differs beyond images and resources/)).toBeTruthy();
    await waitFor(() => expect(queryByText(/reading…/)).toBeNull());
    // Matching columns carry no such flag.
    expect(within(getByTestId("compare-col-stg-we")).queryByText(/differs beyond images/)).toBeNull();
  });

  describe("modal focus (Codex step 6b)", () => {
    const tab = (shift = false) => fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Tab", shiftKey: shift });

    it("Tab wraps inside the dialog whether Refresh is enabled or disabled; keys never reach the window", async () => {
      let release: (v: WorkloadCompareDTO) => void = () => {};
      vi.mocked(workloadCompare).mockImplementation((cluster) => cluster === "prd-we" ? new Promise((r) => { release = r; }) : Promise.resolve(result()));
      const onWindowKey = vi.fn();
      window.addEventListener("keydown", onWindowKey);
      const { getByRole, findByText } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      const close = getByRole("button", { name: "close fleet compare" });
      await findByText("1 reading…");
      // Refresh disabled: Close is the only stop, both directions stay on it
      expect(document.activeElement).toBe(close);
      tab(); expect(document.activeElement).toBe(close);
      tab(true); expect(document.activeElement).toBe(close);
      await act(async () => { release(result()); });
      // Refresh enabled: the wrap points (jsdom does not move focus between
      // them natively; the browser does)
      const refresh = getByRole("button", { name: "Refresh" });
      refresh.focus(); tab(); expect(document.activeElement).toBe(close);
      tab(true); expect(document.activeElement).toBe(refresh);
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: "j" });
      expect(onWindowKey).not.toHaveBeenCalled();
      window.removeEventListener("keydown", onWindowKey);
    });

    it("focus returns to the invoker on close", () => {
      vi.mocked(workloadCompare).mockImplementation(async () => result());
      const invoker = document.createElement("button"); document.body.appendChild(invoker); invoker.focus();
      const { unmount } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      expect(document.activeElement).not.toBe(invoker);
      unmount();
      expect(document.activeElement).toBe(invoker);
      invoker.remove();
    });
  });

  describe("pinned membership (Codex step 6b)", () => {
    it("a cluster joining mid-read is named, never an endless 'reading'; Refresh reads it and drops the old batch's late replies", async () => {
      let releaseOld: (v: WorkloadCompareDTO) => void = () => {};
      let firstPrd = true;
      vi.mocked(workloadCompare).mockImplementation((cluster) => {
        if (cluster === "prd-we" && firstPrd) { firstPrd = false; return new Promise((r) => { releaseOld = r; }); }
        return Promise.resolve(result({ containers: [{ name: "app", image: `${cluster}:img`, resources: "" }] }));
      });
      const { getByRole, findByText, queryByTestId, getByTestId, queryByText } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      await findByText("1 reading…");
      act(() => { useFleet.setState({ clusters: [cl("prd-we", "prd"), cl("dev-we", "dev"), cl("stg-we", "stg"), cl("qa-we", "qa")] }); });
      expect(queryByTestId("compare-col-qa-we")).toBeNull();
      expect(await findByText(/Fleet membership changed since this read - joined: qa-we/)).toBeTruthy();
      await act(async () => { releaseOld(result({ containers: [{ name: "app", image: "late:0", resources: "" }] })); });
      expect(queryByText(/reading…/)).toBeNull();
      fireEvent.click(getByRole("button", { name: "Refresh" }));
      expect(await findByText("qa-we:img")).toBeTruthy();
      expect(within(getByTestId("compare-col-prd-we")).getByText(/prd-we:img/)).toBeTruthy();
      expect(queryByText(/Fleet membership changed/)).toBeNull();
    });

    it("a superseded batch's late reply never lands in the new batch", async () => {
      const pendingOld: ((v: WorkloadCompareDTO) => void)[] = [];
      let round = 0;
      vi.mocked(workloadCompare).mockImplementation((cluster) => {
        if (round === 0 && cluster === "prd-we") return new Promise((r) => { pendingOld.push(r); });
        return Promise.resolve(result({ containers: [{ name: "app", image: `${cluster}:r${round}`, resources: "" }] }));
      });
      const { rerender, findByText, getByTestId } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      await findByText("1 reading…");
      round = 1;
      rerender(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart2" onClose={() => {}} />);
      expect(await findByText("prd-we:r1")).toBeTruthy();
      await act(async () => { pendingOld[0](result({ containers: [{ name: "app", image: "stale:r0", resources: "" }] })); });
      expect(within(getByTestId("compare-col-prd-we")).queryByText(/stale:r0/)).toBeNull();
    });

    it("a cluster leaving keeps its read column and is named; an origin outside the read is unverified", async () => {
      vi.mocked(workloadCompare).mockImplementation(async (cluster) => result({ containers: [{ name: "app", image: `${cluster}:img`, resources: "" }] }));
      const first = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      await first.findByText("prd-we:img");
      act(() => { useFleet.setState({ clusters: [cl("dev-we", "dev"), cl("stg-we", "stg")] }); });
      expect(await first.findByText(/left: prd-we/)).toBeTruthy();
      expect(first.getByTestId("compare-col-prd-we")).toBeTruthy();
      first.unmount();
      const second = render(<FleetCompare origin="gone-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
      expect(await second.findByText(/the origin gone-we was not in the fleet when this read started/)).toBeTruthy();
      expect(second.queryByText(/Reading gone-we/)).toBeNull();
    });
  });

  it("with images already differing, other pod-spec differences are 'not assessed', not implied absent; replicas ≠ is neutral", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) =>
      cluster === "prd-we" ? result({ desiredReplicas: 5, containers: [{ name: "app", image: "x:2", resources: "req 100m/128Mi" }], specHash: "zzzz" }) : result());
    const { findByText, getByTestId } = render(<FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />);
    await findByText("≠ 1 differs from dev-we");
    const prd = within(getByTestId("compare-col-prd-we"));
    expect(prd.getByText(/other differences \(env, volumes, probes…\) not assessed/)).toBeTruthy();
    const mark = prd.getByText(/5 ready/).querySelector("span[aria-label='differs from origin']") as HTMLElement;
    expect(mark.style.color).toBe("var(--color-text-secondary)");
  });
});
