import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import type { AIResultDTO } from "../store/fleet";

const bridge = vi.hoisted(() => ({
  aiAvailability: vi.fn(),
  aiEgressAllowed: vi.fn(),
  explain: vi.fn(),
}));
vi.mock("../bridge/ai", () => bridge);

import { useInspectorAI, usePendingExplain, targetKey, pendingExplainTTL } from "./AIExplain";
import { useFleet } from "../store/fleet";
import { buildCommands } from "../chrome/commands";
import type { ExplainTarget } from "../bridge/ai";

// Harness renders what a lens inspector renders: the More items as buttons
// and the block.
function Harness({ target, enabled = true, cluster = "c1" }: { target: ExplainTarget | null; enabled?: boolean; cluster?: string }) {
  const ai = useInspectorAI(target, "Pod/shop/api", enabled, cluster);
  return (
    <div>
      {ai.menu.map((m) => "label" in m && (
        <button key={m.label} disabled={"disabled" in m && m.disabled} title={"hint" in m ? m.hint : undefined} onClick={"onSelect" in m ? m.onSelect : undefined}>{m.label}</button>
      ))}
      {ai.block}
    </div>
  );
}

const pod: ExplainTarget = { kind: "row", task: "pods", part: "pods", uid: "u1" };
const ok = (over: Partial<AIResultDTO> = {}): AIResultDTO => ({
  ok: true, explanation: "The api container keeps exiting.", model: "claude-opus-5-5", backend: "anthropic", origin: "https://api.anthropic.com",
  groundedOn: 'Klyx verdict "CrashLoopBackOff"', error: "", scrubbed: 2, ...over,
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

beforeEach(() => {
  bridge.aiAvailability.mockReset().mockResolvedValue({ backend: "anthropic", ready: true });
  bridge.aiEgressAllowed.mockReset().mockResolvedValue(true);
  bridge.explain.mockReset().mockResolvedValue(ok());
  usePendingExplain.setState({ key: null, at: 0 });
});
afterEach(() => vi.useRealTimers());

describe("useInspectorAI", () => {
  it("offers setup when no provider is configured", async () => {
    bridge.aiAvailability.mockResolvedValue({ backend: "off", ready: false });
    const openSettings = vi.spyOn(useFleet.getState(), "openSettings");
    render(<Harness target={pod} />);
    fireEvent.click(await screen.findByRole("button", { name: "Set up AI…" }));
    expect(openSettings).toHaveBeenCalled();
  });

  it("disables Explain with the reason when AI is unavailable or egress is refused", async () => {
    bridge.aiAvailability.mockResolvedValueOnce({ backend: "openai", ready: false, problem: "No API key is stored for this provider." });
    const { unmount } = render(<Harness target={pod} />);
    let btn = (await screen.findByRole("button", { name: "Explain with AI" })) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.title).toMatch(/No API key/);
    unmount();

    bridge.aiEgressAllowed.mockResolvedValueOnce(false);
    render(<Harness target={pod} cluster="prd" />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Explain with AI" }).getAttribute("title")).toMatch(/protected cluster prd/));
    btn = screen.getByRole("button", { name: "Explain with AI" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it("runs Explain for the target and shows what the answer rests on", async () => {
    render(<Harness target={pod} />);
    fireEvent.click(await screen.findByRole("button", { name: "Explain with AI" }));
    await waitFor(() => expect(screen.getByTestId("ai-explain").textContent).toContain("keeps exiting"));
    expect(bridge.explain).toHaveBeenCalledWith("c1", pod);
    const text = screen.getByTestId("ai-explain").textContent ?? "";
    for (const want of ['Grounded on: Klyx verdict "CrashLoopBackOff"', "claude-opus-5-5 via https://api.anthropic.com", "2 values scrubbed", "verify before acting"]) {
      expect(text).toContain(want);
    }
  });

  it("shows a failure as an error, never as an explanation", async () => {
    bridge.explain.mockResolvedValue({ ...ok(), ok: false, explanation: "", error: "The model declined to answer (cyber)." });
    render(<Harness target={pod} />);
    fireEvent.click(await screen.findByRole("button", { name: "Explain with AI" }));
    await waitFor(() => expect(screen.getByTestId("ai-explain").textContent).toContain("declined to answer"));
  });

  it("drops a reply that arrives after the selection changed", async () => {
    const late = deferred<AIResultDTO>();
    bridge.explain.mockReturnValueOnce(late.promise);
    const { rerender } = render(<Harness target={pod} />);
    fireEvent.click(await screen.findByRole("button", { name: "Explain with AI" }));
    rerender(<Harness target={{ kind: "row", task: "pods", part: "pods", uid: "u2" }} />);
    const status = vi.spyOn(useFleet.getState(), "setActionStatus");
    await act(async () => late.resolve(ok({ explanation: "about the OLD pod" })));
    expect(screen.queryByText(/about the OLD pod/)).toBeNull();
    expect(screen.queryByTestId("ai-explain")).toBeNull();
    expect(status).not.toHaveBeenCalled(); // no Activity entry for a dropped reply
  });

  it("offers nothing for a removed or unverified selection", async () => {
    render(<Harness target={pod} enabled={false} />);
    await act(async () => { await Promise.resolve(); });
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("palette Explain (parked request)", () => {
  it("runs once when the inspector for that target mounts and AI is ready", async () => {
    usePendingExplain.getState().request(targetKey("c1", pod));
    render(<Harness target={pod} />);
    await waitFor(() => expect(bridge.explain).toHaveBeenCalledTimes(1));
    expect(usePendingExplain.getState().key).toBeNull();
  });

  it("never fires for another target, nor after it expired", async () => {
    usePendingExplain.getState().request(targetKey("c1", { kind: "row", task: "pods", part: "pods", uid: "other" }));
    const { unmount } = render(<Harness target={pod} />);
    await screen.findByRole("button", { name: "Explain with AI" });
    expect(bridge.explain).not.toHaveBeenCalled();
    unmount();

    usePendingExplain.setState({ key: targetKey("c1", pod), at: Date.now() - pendingExplainTTL - 1 });
    render(<Harness target={pod} />);
    await screen.findByRole("button", { name: "Explain with AI" });
    expect(bridge.explain).not.toHaveBeenCalled();
  });

  it("buildCommands offers explain for a loaded pod and parks its target", () => {
    const s = useFleet.getState();
    useFleet.setState({ pods: { ...s.pods, cluster: "c1", items: [{ ...(s.pods.items[0] ?? {}), namespace: "shop", name: "api", uid: "u1", gvr: "v1/pods" } as never] } });
    const cmd = buildCommands(useFleet.getState()).find((c) => c.title === "explain pod shop/api");
    expect(cmd).toBeTruthy();
    cmd!.run();
    expect(usePendingExplain.getState().key).toBe(targetKey("c1", pod));
  });
});
