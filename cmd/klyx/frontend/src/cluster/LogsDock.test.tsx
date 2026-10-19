import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";

vi.mock("./LogHistoryPane", () => ({ LogHistoryPane: () => <div data-testid="history-pane" /> }));
vi.mock("./LogsPane", () => ({ LogsPane: () => <div data-testid="live-pane" /> }));
const { LogsDock } = await import("./PodsView");

const setPresent = (present: boolean) => useFleet.setState({ clusters: [{ name: "c", state: "Synced", logHistoryPresent: present } as never] } as never);
const dock = (target: Parameters<typeof LogsDock>[0]["target"]) => render(<LogsDock cluster="c" target={target} onClose={() => {}} onPopOut={() => {}} />);
const live = { namespace: "team", name: "api-1", containers: [{ name: "web" }], uid: "u-1", createdUnix: 1 };

// Coverage S8b §7: the dock's Live | History, History pinned to the UID;
// a gone pod has History only; no Loki or no UID, no History.
describe("the logs dock's source", () => {
  beforeEach(() => setPresent(true));
  it("Live first; History on the person's choice", () => {
    dock(live);
    expect(screen.getByTestId("live-pane")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "History" }));
    expect(screen.getByTestId("history-pane")).toBeTruthy();
  });
  it("a gone pod opens in History, Live disabled", () => {
    dock({ ...live, gone: true, history: true });
    expect(screen.getByTestId("history-pane")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Live" }) as HTMLButtonElement).disabled).toBe(true);
  });
  it("no Loki, or no UID: no History", () => {
    setPresent(false);
    const { unmount } = dock(live);
    expect(screen.queryByTestId("logs-mode")).toBeNull();
    unmount();
    setPresent(true);
    dock({ ...live, uid: undefined });
    expect(screen.queryByTestId("logs-mode")).toBeNull();
  });
});
