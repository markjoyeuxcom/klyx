import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { getZoom, zoomReset } from "./zoom";
import { LogsWindow } from "./LogsWindow";
import { ThemeProvider } from "../theme/ThemeProvider";

// LogsWindow hosts the PopoutBar (ThemeToggle needs the provider),
// matching the production boot path where ThemeProvider wraps both branches.
const renderWindow = (params: URLSearchParams) =>
  render(<ThemeProvider><LogsWindow params={params} /></ThemeProvider>);

// Capture event subscriptions so the LogsPane stream lifecycle resolves.
const eventHandlers: Record<string, ((ev: { data: unknown }) => void)[]> = {};
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((eventName: string, handler: (ev: { data: unknown }) => void) => {
      (eventHandlers[eventName] ??= []).push(handler);
      return () => {
        eventHandlers[eventName] = (eventHandlers[eventName] || []).filter((h) => h !== handler);
      };
    }),
  },
}));

const mockOpenLogStream = vi.fn();
const mockOpenWorkloadLogStream = vi.fn();
const mockCloseLogStream = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  LogsService: {
    OpenLogStream: (...args: unknown[]) => mockOpenLogStream(...args),
    OpenWorkloadLogStream: (...args: unknown[]) => mockOpenWorkloadLogStream(...args),
    CloseLogStream: (...args: unknown[]) => mockCloseLogStream(...args),
    CloseAll: vi.fn().mockResolvedValue(undefined),
  },
}));

describe("LogsWindow", () => {
  beforeEach(() => {
    for (const k of Object.keys(eventHandlers)) delete eventHandlers[k];
    mockOpenLogStream.mockReset();
    mockOpenWorkloadLogStream.mockReset();
    mockCloseLogStream.mockReset();
    mockCloseLogStream.mockResolvedValue(undefined);
    mockOpenLogStream.mockResolvedValue({ streamId: "s1", error: undefined });
    mockOpenWorkloadLogStream.mockResolvedValue({ streamId: "s-agg", error: undefined });
  });
  afterEach(() => vi.clearAllMocks());

  function seeded() {
    return new URLSearchParams(
      "?logswin=1&cluster=prod-aks&ns=monitoring&pod=grafana-7d4&container=grafana",
    );
  }

  it("renders header with ns/pod and cluster from params", () => {
    const { getByTestId, getByText } = renderWindow(seeded());
    const win = getByTestId("logs-window");
    expect(win).toBeTruthy();
    // ns/pod present
    expect(win.textContent).toContain("monitoring");
    expect(win.textContent).toContain("grafana-7d4");
    // cluster muted
    expect(getByText("prod-aks")).toBeTruthy();
  });

  it("opens the stream on the container param (static container, empty containers list)", async () => {
    renderWindow(seeded());
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(1));
    expect(mockOpenLogStream).toHaveBeenCalledWith(
      "prod-aks", "monitoring", "grafana-7d4", "grafana", false, 500, expect.any(String),
    );
  });

  it("does not render the expand button when hosted in window", async () => {
    const { queryByRole } = renderWindow(seeded());
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalled());
    expect(queryByRole("button", { name: /expand logs/i })).toBeNull();
  });

  it("mode=workload opens the aggregate stream with kind and workload name", async () => {
    renderWindow(new URLSearchParams(
      "?logswin=1&mode=workload&cluster=prod-aks&ns=monitoring&kind=Deployment&name=grafana&container=",
    ));
    await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalledTimes(1));
    expect(mockOpenWorkloadLogStream).toHaveBeenCalledWith(
      "prod-aks", "monitoring", "Deployment", "grafana", "", 500, expect.any(String),
    );
    expect(mockOpenLogStream).not.toHaveBeenCalled();
  });

  it("mode=workload renders the workload name in the identity line", async () => {
    const { getByTestId } = renderWindow(new URLSearchParams(
      "?logswin=1&mode=workload&cluster=prod-aks&ns=monitoring&kind=Deployment&name=grafana&container=",
    ));
    await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalled());
    const win = getByTestId("logs-window");
    expect(win.textContent).toContain("monitoring");
    expect(win.textContent).toContain("grafana");
  });

  it("fills the window through the 100% chain, never 100vh (viewport units are not zoom-compensated)", () => {
    const { getByTestId } = renderWindow(seeded());
    expect(getByTestId("logs-window").style.height).toBe("100%");
  });

  it("takes the zoom keys - and only those - and lets go of them on close (Codex zoom review)", async () => {
    const { unmount } = renderWindow(seeded());
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalled());
    try {
      act(() => { fireEvent.keyDown(window, { key: "=", metaKey: true }); });
      expect(getZoom()).toBe(1.1);
      act(() => { fireEvent.keyDown(window, { key: "0", metaKey: true }); });
      expect(getZoom()).toBe(1);
      unmount();
      act(() => { fireEvent.keyDown(window, { key: "=", metaKey: true }); });
      expect(getZoom()).toBe(1);
    } finally {
      act(() => zoomReset());
    }
  });

  it("its mast carries no main-window navigation - a pop-out has no places (step 7b regression)", () => {
    const { getByRole, queryByRole, queryByTestId } = renderWindow(seeded());
    expect(getByRole("banner").textContent).toContain("Logs");
    expect(queryByRole("button", { name: /^Back/ })).toBeNull();
    expect(queryByRole("button", { name: "Recent places" })).toBeNull();
    expect(queryByTestId("command-bar")).toBeNull();
    expect(queryByRole("button", { name: /^Settings/ })).toBeNull();
  });
});
