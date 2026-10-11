import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, PromotionSnapshotDTO } from "../store/fleet";

vi.mock("../bridge/promotion", () => ({ getPromotion: vi.fn() }));
import { getPromotion } from "../bridge/promotion";
import { FleetPromotion } from "./FleetPromotion";

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): Deferred<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r; }); return { promise, resolve }; };
const cl = (name: string, env = "dev") => ({ name, env, region: "" } as unknown as ClusterDTO);
const unit = (name: string, revision: string) => ({ kind: "Kustomization", namespace: "flux-system", name, sourceKind: "GitRepository", sourceName: "repo", revision, ready: "ready", suspended: false, stale: false, message: "" });
const snap = (over: Partial<PromotionSnapshotDTO> = {}): PromotionSnapshotDTO => ({ fluxPresent: true, argoPresent: false, units: [unit("apps", "r1")], sources: [], ...over });
const flush = () => act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });

beforeEach(() => { vi.clearAllMocks(); vi.mocked(getPromotion).mockResolvedValue(snap()); });

describe("fleet batch lifecycle - Promotion (Codex 7d review)", () => {
  it("a cluster that joins is named, not silently pending; Refresh reads it", async () => {
    useFleet.setState({ clusters: [cl("a")] });
    render(<FleetPromotion />);
    await flush();
    expect(getPromotion).toHaveBeenCalledTimes(1);
    act(() => { useFleet.setState({ clusters: [cl("a"), cl("b")] }); });
    expect(screen.getByText(/joined: b/)).toBeTruthy();
    expect(screen.queryByText(/loading…/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await flush();
    expect(vi.mocked(getPromotion).mock.calls.map((c) => c[0])).toEqual(["a", "a", "b"]);
    expect(screen.queryByText(/joined: b/)).toBeNull();
  });

  it("a cluster that leaves is named and stays in the read it belonged to", async () => {
    useFleet.setState({ clusters: [cl("a"), cl("b")] });
    render(<FleetPromotion />);
    await flush();
    act(() => { useFleet.setState({ clusters: [cl("a")] }); });
    expect(screen.getByText(/left: b/)).toBeTruthy();
  });

  it("an empty fleet at mount says so, and the read starts once clusters seed", async () => {
    useFleet.setState({ clusters: [] });
    render(<FleetPromotion />);
    await flush();
    expect(screen.getByText("No clusters to read.")).toBeTruthy();
    act(() => { useFleet.setState({ clusters: [cl("a")] }); });
    await flush();
    expect(getPromotion).toHaveBeenCalledWith("a");
    expect(screen.queryByText("No clusters to read.")).toBeNull();
  });

  it("Refresh drops a late reply from the earlier batch", async () => {
    useFleet.setState({ clusters: [cl("a")] });
    const old = deferred<PromotionSnapshotDTO>();
    vi.mocked(getPromotion).mockReturnValueOnce(old.promise).mockResolvedValueOnce(snap({ units: [unit("apps", "new")] }));
    render(<FleetPromotion />);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await flush();
    old.resolve(snap({ units: [unit("apps", "old")] }));
    await flush();
    fireEvent.click(screen.getByText(/^All units/));
    expect(screen.getByText("new")).toBeTruthy();
    expect(screen.queryByText("old")).toBeNull();
  });

  it("a failed read can be retried", async () => {
    useFleet.setState({ clusters: [cl("a")] });
    vi.mocked(getPromotion).mockRejectedValueOnce(new Error("forbidden")).mockResolvedValueOnce(snap());
    render(<FleetPromotion />);
    await flush();
    expect(screen.getByText(/Could not read a: forbidden/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await flush();
    expect(screen.queryByText(/Could not read a/)).toBeNull();
  });

  it("source heads: pending, unread and absent are three different cells; unread never counts as different", async () => {
    useFleet.setState({ clusters: [cl("a", "dev"), cl("b", "stg"), cl("c", "prd")] });
    const pendingC = deferred<PromotionSnapshotDTO>();
    const src = (rev: string) => [{ kind: "GitRepository", namespace: "flux-system", name: "repo", revision: rev, ready: "ready", suspended: false }];
    vi.mocked(getPromotion).mockImplementation(async (c: string) => (c === "a" ? snap({ sources: src("main@sha1:aaaaaaa1") }) : c === "b" ? snap({ error: "forbidden", units: [] }) : pendingC.promise));
    render(<FleetPromotion />);
    await flush();
    expect(screen.getAllByText("unread").length).toBeGreaterThan(0);
    expect(screen.getAllByText("…").length).toBeGreaterThan(0);
    expect(screen.queryByText("≠ heads differ")).toBeNull();
    pendingC.resolve(snap({ sources: [] }));
    await flush();
    expect(screen.getByText("absent")).toBeTruthy();
  });

  it("readiness the cluster could not establish is named, not folded into quiet", async () => {
    useFleet.setState({ clusters: [cl("a")] });
    vi.mocked(getPromotion).mockResolvedValue(snap({ units: [{ ...unit("apps", "r1"), ready: "unknown" }] }));
    render(<FleetPromotion />);
    await flush();
    expect(screen.getByText(/1 with readiness unknown/)).toBeTruthy();
  });
});
