import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";

vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn(() => () => {}) } }));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  HealthService: {
    Summaries: vi.fn(async () => []),
    OpenRows: vi.fn(async (cluster: string) => ({ rows: { cluster, incarnation: 1, task: "certs", evaluatedAt: "", rows: [{ uid: cluster }],
      part: { id: "certificates", entity: "", scope: "", classifier: "native", coverage: "live", kinds: [], obs: 1, ver: 1 } } })),
    CloseRows: vi.fn(async () => {}),
    RowsBootstrap: vi.fn(),
  },
}));
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { useRows } from "./useRows";
import { useHealth } from "../store/health";
import { _resetHealthRowsForTest } from "../bridge/healthRows";

function Probe({ cluster }: { cluster: string | null }) {
  const r = useRows<{ uid: string }>(cluster, "certs", "certificates");
  return <span data-testid="p">{r.rows ? r.rows.map((x) => x.uid).join(",") : "none"}|{r.counts.source}</span>;
}

beforeEach(() => {
  vi.clearAllMocks();
  useHealth.setState({ summaries: {}, finalizedThrough: {}, subs: {} });
  _resetHealthRowsForTest();
});

describe("useRows", () => {
  it("opens on mount, follows the cluster (A->B->A with fresh tokens), closes on unmount", async () => {
    const { getByTestId, rerender, unmount } = render(<Probe cluster="A" />);
    await act(async () => { await Promise.resolve(); });
    expect(getByTestId("p").textContent).toBe("A|rows");
    rerender(<Probe cluster="B" />);
    await act(async () => { await Promise.resolve(); });
    expect(getByTestId("p").textContent).toBe("B|rows");
    rerender(<Probe cluster="A" />);
    await act(async () => { await Promise.resolve(); });
    const tokens = (HealthService.OpenRows as ReturnType<typeof vi.fn>).mock.calls.map((c) => c[3]);
    expect(tokens).toHaveLength(3);
    expect(new Set(tokens).size).toBe(3);
    expect(HealthService.CloseRows).toHaveBeenCalledTimes(2);
    unmount();
    expect(HealthService.CloseRows).toHaveBeenCalledTimes(3);
    expect(Object.keys(useHealth.getState().subs)).toHaveLength(0);
  });

  it("no cluster opens nothing", () => {
    const { getByTestId } = render(<Probe cluster={null} />);
    expect(getByTestId("p").textContent).toBe("none|none");
    expect(HealthService.OpenRows).not.toHaveBeenCalled();
  });
});
