import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, PromotionSnapshotDTO, PromotionUnitDTO } from "../store/fleet";
import { FleetPromotion } from "./FleetPromotion";

vi.mock("../bridge/promotion", () => ({
  getPromotion: vi.fn(async () => ({ fluxPresent: false, argoPresent: false, units: [], sources: [] }) as PromotionSnapshotDTO),
}));
import { getPromotion } from "../bridge/promotion";

const cl = (name: string, env: string): ClusterDTO => ({ name, env } as unknown as ClusterDTO);

const unit = (name: string, revision: string, over: Partial<PromotionUnitDTO> = {}): PromotionUnitDTO => ({
  kind: "Kustomization", namespace: "flux-system", name,
  sourceKind: "GitRepository", sourceName: "fleet-repo",
  revision, ready: "ready", suspended: false, stale: false, message: "",
  ...over,
});

const snap = (units: PromotionUnitDTO[], over: Partial<PromotionSnapshotDTO> = {}): PromotionSnapshotDTO => ({
  fluxPresent: true, argoPresent: false, units, sources: [], ...over,
});

describe("FleetPromotion", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ clusters: [cl("prd-weu", "prd"), cl("dev-weu", "dev")] });
  });

  it("joins units across clusters dev-first and flags revision drift", async () => {
    vi.mocked(getPromotion).mockImplementation(async (cluster: string) =>
      cluster === "dev-weu"
        ? snap([unit("apps", "main@sha1:aaaaaaa111")])
        : snap([unit("apps", "main@sha1:bbbbbbb222")]),
    );
    const { findByText, getByTestId } = render(<FleetPromotion />);
    expect(await findByText("≠ differs")).toBeTruthy();
    // dev column leads; both revisions render compacted.
    expect(await findByText("main@aaaaaaa")).toBeTruthy();
    // the cell that differs from the leftmost column carries the ≠ mark
    expect(await findByText("≠ main@bbbbbbb")).toBeTruthy();
    expect(getByTestId("prom-row-Kustomization/flux-system/apps")).toBeTruthy();
  });

  it("a matching fleet is stated neutrally and scoped to what answered - never an all-clear (step 7d)", async () => {
    vi.mocked(getPromotion).mockResolvedValue(snap([unit("apps", "main@sha1:aaaaaaa111")]));
    const { findByText, queryByText } = render(<FleetPromotion />);
    const line = await findByText(/No unit differs or is not ready among 1 unit on the 2 of 2 clusters read/);
    expect(line.getAttribute("style") ?? "").not.toMatch(/success/);
    expect(queryByText(/matching revisions and healthy/)).toBeNull();
  });

  it("show-all reveals healthy rows; absent clusters say absent", async () => {
    vi.mocked(getPromotion).mockImplementation(async (cluster: string) =>
      cluster === "dev-weu" ? snap([unit("apps", "r1")]) : snap([]),
    );
    const { findByText, getByText } = render(<FleetPromotion />);
    await findByText(/Needs attention/);
    fireEvent.click(getByText(/^All units/));
    expect(await findByText("flux-system/apps")).toBeTruthy();
    expect(getByText("absent")).toBeTruthy();
  });

  it("a failed cluster read renders its error", async () => {
    vi.mocked(getPromotion).mockImplementation(async (cluster: string) =>
      cluster === "prd-weu"
        ? snap([], { error: "list kustomizations: forbidden" })
        : snap([unit("apps", "r1")]),
    );
    const { findByText } = render(<FleetPromotion />);
    expect(await findByText(/Could not read prd-weu: list kustomizations: forbidden/)).toBeTruthy();
    // an unread cluster's cell says unread, never "absent"
    fireEvent.click((await findByText(/^All units/)));
    expect(await findByText("unread")).toBeTruthy();
  });

  it("not-ready units surface in the attention default; suspended does not", async () => {
    vi.mocked(getPromotion).mockResolvedValue(snap([
      unit("broken", "r1", { ready: "not-ready", message: "apply failed" }),
      unit("paused", "r1", { suspended: true }),
    ]));
    const { findAllByText, queryByText } = render(<FleetPromotion />);
    expect((await findAllByText(/not ready/)).length).toBeGreaterThan(0);
    // Suspended is intended - it does not join the attention list.
    expect(queryByText("flux-system/paused")).toBeNull();
  });

  it("source heads render with a differ flag when clusters fetched different revisions", async () => {
    vi.mocked(getPromotion).mockImplementation(async (cluster: string) =>
      snap([], {
        sources: [{ kind: "GitRepository", namespace: "flux-system", name: "fleet-repo", revision: cluster === "dev-weu" ? "main@sha1:aaaaaaa111" : "main@sha1:ccccccc333", ready: "ready", suspended: false }],
      }),
    );
    const { findByText } = render(<FleetPromotion />);
    expect(await findByText("≠ heads differ")).toBeTruthy();
    expect(await findByText(/Source heads/)).toBeTruthy();
  });
});
