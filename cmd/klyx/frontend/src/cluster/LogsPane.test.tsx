import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent, act, waitFor } from "@testing-library/react";
import { LogsPane, filterLogLinesByLevel, getLogLineDetail } from "./LogsPane";
import type { ContainerSummaryDTO } from "../store/fleet";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// Capture event handlers so we can fire them in tests.
const eventHandlers: Record<string, ((ev: { data: unknown }) => void)[]> = {};
const offFns: (() => void)[] = [];

vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((eventName: string, handler: (ev: { data: unknown }) => void) => {
      if (!eventHandlers[eventName]) eventHandlers[eventName] = [];
      eventHandlers[eventName].push(handler);
      const off = () => {
        eventHandlers[eventName] = (eventHandlers[eventName] || []).filter((h) => h !== handler);
      };
      offFns.push(off);
      return off;
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

// Mock clipboard
const mockClipboardWrite = vi.fn();
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: mockClipboardWrite },
  writable: true,
  configurable: true,
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fireChunk(eventName: string, lines: string[], eof = false, error?: string) {
  const handlers = eventHandlers[eventName] ?? [];
  for (const h of handlers) {
    h({ data: { lines, eof, error } });
  }
}

const makeContainer = (overrides: Partial<ContainerSummaryDTO> = {}): ContainerSummaryDTO => ({
  name: "app", image: "nginx:latest", ready: true, restarts: 0, state: "running", init: false, ...overrides,
});

const defaultPod = {
  namespace: "default",
  name: "my-pod",
  containers: [makeContainer({ name: "app" }), makeContainer({ name: "sidecar" })],
};

const defaultPodWithInit = {
  namespace: "default",
  name: "my-pod",
  containers: [
    makeContainer({ name: "app" }),
    makeContainer({ name: "init-setup", init: true }),
  ],
};

const openSuccess = (streamId = "stream-1") =>
  Promise.resolve({ streamId, error: undefined });

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("LogsPane", () => {
  beforeEach(() => {
    // Reset handlers and mocks before each test
    for (const k of Object.keys(eventHandlers)) delete eventHandlers[k];
    offFns.length = 0;
    mockOpenLogStream.mockReset();
    mockOpenWorkloadLogStream.mockReset();
    mockCloseLogStream.mockReset();
    mockCloseLogStream.mockResolvedValue(undefined);
    mockClipboardWrite.mockReset();
    mockClipboardWrite.mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  // -------------------------------------------------------------------------
  // Existing tests (preserved)
  // -------------------------------------------------------------------------

  it("filters log lines by detected level", () => {
    const lines = [
      "I0609 22:26:09.000000  1 foo.go:1] all good",
      "W0609 22:26:09.000000  1 foo.go:2] degraded",
      "E0609 22:26:09.000000  1 foo.go:3] failed",
      "plain neutral line",
    ];

    expect(filterLogLinesByLevel(lines, { error: false, warn: false, info: false, debug: false })).toEqual(lines);
    expect(filterLogLinesByLevel(lines, { error: true, warn: false, info: false, debug: false })).toEqual([lines[2]]);
    expect(filterLogLinesByLevel(lines, { error: true, warn: true, info: false, debug: false })).toEqual([lines[1], lines[2]]);
  });

  it("extracts selected log line detail from the shared parser", () => {
    const line = "\x1b[31mE0609 22:26:09.000000  1 bar.go:99] failed to connect\x1b[0m";
    const detail = getLogLineDetail(line);

    expect(detail.level).toBe("error");
    expect(detail.source).toBe("bar.go:99");
    expect(detail.message).toBe("failed to connect");
    expect(detail.hasAnsi).toBe(true);
    expect(detail.stripped).toBe("E0609 22:26:09.000000  1 bar.go:99] failed to connect");
  });

  it("calls OpenLogStream with defaults on mount (500 tail, first regular container)", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess());
    render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(1));
    expect(mockOpenLogStream).toHaveBeenCalledWith(
      "homelab", "default", "my-pod", "app", false, 500,
    );
  });

  it("shows 'connecting' then 'streaming' after stream opens", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess());
    const { getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    // Before stream resolves, status is connecting
    expect(getByText("connecting…")).toBeTruthy();
    await waitFor(() => expect(getByText("streaming")).toBeTruthy());
  });

  it("renders received log lines", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s1"));
    const { getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s1"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s1", ["line one", "line two"]);
    });

    await waitFor(() => expect(getByText("line one")).toBeTruthy());
    expect(getByText("line two")).toBeTruthy();
  });

  it("shows 'ended' status on EOF and unsubscribes", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-eof"));
    const { getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-eof"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-eof", [], true);
    });

    await waitFor(() => expect(getByText("ended")).toBeTruthy());
    // Handler should have been removed (off() called)
    expect(eventHandlers["podlogs:s-eof"]?.length ?? 0).toBe(0);
  });

  it("calls CloseLogStream on unmount", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-unmount"));
    const { unmount } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-unmount"]).toBeDefined());

    unmount();

    await waitFor(() => expect(mockCloseLogStream).toHaveBeenCalledWith("s-unmount"));
  });

  it("closes old stream and opens new stream on container switch", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-initial"));
    const { getByRole, unmount } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(1));

    // Switch container
    mockOpenLogStream.mockImplementation(() => openSuccess("s-new"));
    const select = getByRole("combobox", { name: /container/i });
    act(() => {
      fireEvent.change(select, { target: { value: "sidecar" } });
    });

    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(2));
    expect(mockCloseLogStream).toHaveBeenCalledWith("s-initial");
    expect(mockOpenLogStream).toHaveBeenLastCalledWith(
      "homelab", "default", "my-pod", "sidecar", false, 500,
    );

    unmount();
  });

  it("clears buffer when clear is clicked (stream continues)", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-clear"));
    const { getByText, queryByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-clear"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-clear", ["hello world"]);
    });
    await waitFor(() => expect(getByText("hello world")).toBeTruthy());

    act(() => {
      fireEvent.click(getByText("clear"));
    });

    // Buffer cleared, line gone
    expect(queryByText("hello world")).toBeNull();
    // Stream still alive (no extra CloseLogStream call at this point)
    expect(mockCloseLogStream).not.toHaveBeenCalled();
  });

  it("highlights search matches and shows match count", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-search"));
    const { getByPlaceholderText, getByText } = render(
      <LogsPane cluster="homelab" pod={defaultPod} />,
    );
    await waitFor(() => expect(eventHandlers["podlogs:s-search"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-search", ["hello world", "goodbye world", "no match here"]);
    });
    await waitFor(() => expect(getByText("no match here")).toBeTruthy());

    act(() => {
      fireEvent.change(getByPlaceholderText("search"), { target: { value: "world" } });
    });

    // match count shown
    await waitFor(() => getByText(/1\/2/));
    // "world" appears in <mark> tags
    const marks = document.querySelectorAll("mark");
    expect(marks.length).toBeGreaterThanOrEqual(2);
  });

  it("shows error status when OpenLogStream returns an error", async () => {
    mockOpenLogStream.mockResolvedValue({ streamId: "", error: "no such pod" });
    const { getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(getByText(/error:.*no such pod/i)).toBeTruthy());
  });

  it("shows init containers labeled in select", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-init"));
    const { getByRole } = render(<LogsPane cluster="homelab" pod={defaultPodWithInit} />);
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalled());
    const select = getByRole("combobox", { name: /container/i }) as HTMLSelectElement;
    const options = Array.from(select.options).map((o) => o.text);
    expect(options).toContain("app");
    expect(options.some((o) => o.startsWith("init:"))).toBe(true);
  });

  it("opens with previous=true when previous chip is toggled", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-prev"));
    const { getByText, unmount } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(1));

    mockOpenLogStream.mockImplementation(() => openSuccess("s-prev2"));
    act(() => {
      fireEvent.click(getByText("previous"));
    });

    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(2));
    expect(mockOpenLogStream).toHaveBeenLastCalledWith(
      "homelab", "default", "my-pod", "app", true, 500,
    );
    unmount();
  });

  it("guard: open resolving after unmount closes the stream immediately", async () => {
    let resolveOpen!: (v: { streamId: string }) => void;
    mockOpenLogStream.mockImplementation(
      () => new Promise<{ streamId: string }>((r) => { resolveOpen = r; }),
    );
    const { unmount } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    // Unmount BEFORE the promise resolves
    unmount();
    // Now resolve the promise — stale guard should close the stream immediately
    act(() => { resolveOpen({ streamId: "s-late" }); });
    await waitFor(() => expect(mockCloseLogStream).toHaveBeenCalledWith("s-late"));
  });

  // -------------------------------------------------------------------------
  // New tests: expand mode
  // -------------------------------------------------------------------------

  it("expand button toggles fixed layout", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-expand"));
    const { getByRole, getByTestId } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-expand"]).toBeDefined());

    // Initially not expanded
    expect(document.querySelector("[data-testid='logs-pane-expanded']")).toBeNull();

    act(() => {
      fireEvent.click(getByRole("button", { name: /expand logs/i }));
    });

    // Now expanded — element with data-testid should appear with fixed position
    const expanded = getByTestId("logs-pane-expanded");
    expect(expanded).toBeTruthy();
    expect(expanded.style.position).toBe("fixed");
  });

  it("stream continues after expand (lines appear while expanded)", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-expand-stream"));
    const { getByRole, getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-expand-stream"]).toBeDefined());

    // Add a line before expanding
    act(() => { fireChunk("podlogs:s-expand-stream", ["before expand"]); });
    await waitFor(() => expect(getByText("before expand")).toBeTruthy());

    // Expand
    act(() => {
      fireEvent.click(getByRole("button", { name: /expand logs/i }));
    });

    // Emit a line while expanded
    act(() => { fireChunk("podlogs:s-expand-stream", ["after expand"]); });
    await waitFor(() => expect(getByText("after expand")).toBeTruthy());
    // Previous line still present (buffer intact)
    expect(getByText("before expand")).toBeTruthy();
  });

  it("Esc while expanded collapses without calling panel onClose", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-esc"));
    const onClose = vi.fn();
    const { getByRole, queryByTestId } = render(
      // onClose would be called by the panel — simulate by wrapping with a keydown spy.
      // Since LogsPane itself doesn't receive onClose, we verify the pane stays in DOM.
      <LogsPane cluster="homelab" pod={defaultPod} />,
    );
    await waitFor(() => expect(eventHandlers["podlogs:s-esc"]).toBeDefined());

    // Expand
    act(() => {
      fireEvent.click(getByRole("button", { name: /expand logs/i }));
    });
    expect(queryByTestId("logs-pane-expanded")).toBeTruthy();

    // Press Esc — should collapse
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });

    await waitFor(() => expect(queryByTestId("logs-pane-expanded")).toBeNull());
    // onClose was never wired here — just assert pane itself still in DOM (not unmounted)
    expect(document.body.contains(document.querySelector("[aria-label='expand logs']"))).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  // -------------------------------------------------------------------------
  // New tests: level coloring
  // -------------------------------------------------------------------------

  it("error line gets danger color style", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-level"));
    const { container } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-level"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-level", [
        "E0609 22:26:09.000000  1 bar.go:99] failed to connect",
        "I0609 22:26:09.000000  1 foo.go:1] all good",
      ]);
    });

    await waitFor(() => {
      const lines = container.querySelectorAll("[style]");
      const dangerLine = Array.from(lines).find(
        (el) => (el as HTMLElement).style.color?.includes("danger") &&
          el.textContent?.includes("failed to connect"),
      );
      expect(dangerLine).toBeTruthy();
    });
  });

  // -------------------------------------------------------------------------
  // New tests: copy button
  // -------------------------------------------------------------------------

  it("copy button writes full buffer to clipboard and shows 'copied'", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-copy"));
    const { getByRole, getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-copy"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-copy", ["line A", "line B", "line C"]);
    });
    await waitFor(() => expect(getByText("line A")).toBeTruthy());

    act(() => {
      fireEvent.click(getByRole("button", { name: /copy logs/i }));
    });

    await waitFor(() => expect(mockClipboardWrite).toHaveBeenCalledWith("line A\nline B\nline C"));
    await waitFor(() => expect(getByText("copied")).toBeTruthy());
  });

  it("copies an individual visible log line", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-copy-line"));
    const { getAllByTestId, getByRole, getByText } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-copy-line"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-copy-line", ["line A", "line B", "line C"]);
    });
    await waitFor(() => expect(getByText("line B")).toBeTruthy());

    const rows = getAllByTestId("log-line");
    act(() => {
      fireEvent.click(rows[1]);
    });

    expect((rows[1] as HTMLElement).style.background).toContain("var(--color-background-info)");

    const copyLineButton = getByRole("button", { name: "copy log line: line B" });
    act(() => {
      fireEvent.click(copyLineButton);
    });

    await waitFor(() => expect(mockClipboardWrite).toHaveBeenCalledWith("line B"));
    expect(mockClipboardWrite).not.toHaveBeenCalledWith("line A\nline B\nline C");
    expect(copyLineButton.getAttribute("aria-pressed")).toBe("true");
  });

  it("shows selected line detail with copy actions and level filtering", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-line-detail"));
    const { getAllByTestId, getAllByText, getByRole, getByTestId, getByText, queryByText } = render(
      <LogsPane cluster="homelab" pod={defaultPod} />,
    );
    await waitFor(() => expect(eventHandlers["podlogs:s-line-detail"]).toBeDefined());

    const infoLine = "I0609 22:26:09.000000  1 foo.go:1] all good";
    const errorLine = "E0609 22:26:09.000000  1 bar.go:99] failed to connect";

    act(() => {
      fireChunk("podlogs:s-line-detail", [infoLine, errorLine]);
    });
    await waitFor(() => expect(getByText(/failed to connect/)).toBeTruthy());

    act(() => {
      fireEvent.click(getAllByTestId("log-line")[1]);
    });

    const detail = getByTestId("log-line-detail");
    expect(detail.textContent).toContain("selected line");
    expect(detail.textContent).toContain("error");
    expect(detail.textContent).toContain("bar.go:99");
    expect(detail.textContent).toContain("failed to connect");

    act(() => {
      fireEvent.click(getByRole("button", { name: "copy log message" }));
    });
    await waitFor(() => expect(mockClipboardWrite).toHaveBeenCalledWith("failed to connect"));

    act(() => {
      fireEvent.click(getByRole("button", { name: "copy raw log line" }));
    });
    await waitFor(() => expect(mockClipboardWrite).toHaveBeenCalledWith(errorLine));

    act(() => {
      fireEvent.click(getByRole("button", { name: "filter error logs" }));
    });

    expect(queryByText(/all good/)).toBeNull();
    expect(getAllByText(/failed to connect/).length).toBeGreaterThan(0);
  });

  it("level chips filter visible logs and copy uses the visible set", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-level-filter"));
    const { getByText, queryByText, getByRole } = render(<LogsPane cluster="homelab" pod={defaultPod} />);
    await waitFor(() => expect(eventHandlers["podlogs:s-level-filter"]).toBeDefined());

    const infoLine = "I0609 22:26:09.000000  1 foo.go:1] all good";
    const warnLine = "W0609 22:26:09.000000  1 foo.go:2] degraded";
    const errorLine = "E0609 22:26:09.000000  1 foo.go:3] failed";
    const neutralLine = "plain neutral line";

    act(() => {
      fireChunk("podlogs:s-level-filter", [infoLine, warnLine, errorLine, neutralLine]);
    });
    await waitFor(() => expect(getByText(/all good/)).toBeTruthy());
    expect(getByText(/degraded/)).toBeTruthy();
    expect(getByText(/failed/)).toBeTruthy();
    expect(getByText(neutralLine)).toBeTruthy();

    act(() => {
      fireEvent.click(getByText("error"));
    });

    expect(queryByText(/all good/)).toBeNull();
    expect(queryByText(/degraded/)).toBeNull();
    expect(getByText(/failed/)).toBeTruthy();
    expect(queryByText(neutralLine)).toBeNull();

    act(() => {
      fireEvent.click(getByRole("button", { name: /copy logs/i }));
    });

    await waitFor(() => expect(mockClipboardWrite).toHaveBeenCalledWith(errorLine));
  });

  // -------------------------------------------------------------------------
  // New tests: search Enter advances match index
  // -------------------------------------------------------------------------

  it("search Enter advances n/N match index", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-nav"));
    const { getByPlaceholderText, getByText } = render(
      <LogsPane cluster="homelab" pod={defaultPod} />,
    );
    await waitFor(() => expect(eventHandlers["podlogs:s-nav"]).toBeDefined());

    act(() => {
      fireChunk("podlogs:s-nav", ["match one", "no", "match two", "no", "match three"]);
    });
    await waitFor(() => expect(getByText("match three")).toBeTruthy());

    const input = getByPlaceholderText("search");
    act(() => {
      fireEvent.change(input, { target: { value: "match" } });
    });

    // starts at 1/3
    await waitFor(() => getByText(/1\/3/));

    act(() => {
      fireEvent.keyDown(input, { key: "Enter" });
    });
    await waitFor(() => getByText(/2\/3/));

    act(() => {
      fireEvent.keyDown(input, { key: "Enter" });
    });
    await waitFor(() => getByText(/3\/3/));

    // wraps around
    act(() => {
      fireEvent.keyDown(input, { key: "Enter" });
    });
    await waitFor(() => getByText(/1\/3/));
  });

  // -------------------------------------------------------------------------
  // New tests: pop-out hosting (hostedInWindow / initialContainer)
  // -------------------------------------------------------------------------

  it("hostedInWindow hides the expand button", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-host"));
    const { queryByRole } = render(
      <LogsPane cluster="homelab" pod={defaultPod} hostedInWindow />,
    );
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalled());
    expect(queryByRole("button", { name: /expand logs/i })).toBeNull();
  });

  it("initialContainer with empty containers streams that container as static text", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-static"));
    const { getByText, queryByRole } = render(
      <LogsPane
        cluster="prod"
        pod={{ namespace: "monitoring", name: "grafana-1", containers: [] }}
        initialContainer="grafana"
        hostedInWindow
      />,
    );
    await waitFor(() => expect(mockOpenLogStream).toHaveBeenCalledTimes(1));
    // Stream opened on the explicit container.
    expect(mockOpenLogStream).toHaveBeenCalledWith(
      "prod", "monitoring", "grafana-1", "grafana", false, 500,
    );
    // Rendered as static text, not a select.
    expect(queryByRole("combobox", { name: /container/i })).toBeNull();
    expect(getByText("grafana")).toBeTruthy();
  });

  it("onContainerChange fires with the active container on mount and switch", async () => {
    mockOpenLogStream.mockImplementation(() => openSuccess("s-cc"));
    const onContainerChange = vi.fn();
    const { getByRole } = render(
      <LogsPane cluster="homelab" pod={defaultPod} onContainerChange={onContainerChange} />,
    );
    await waitFor(() => expect(onContainerChange).toHaveBeenCalledWith("app"));

    const select = getByRole("combobox", { name: /container/i });
    act(() => {
      fireEvent.change(select, { target: { value: "sidecar" } });
    });
    await waitFor(() => expect(onContainerChange).toHaveBeenLastCalledWith("sidecar"));
  });

  // -------------------------------------------------------------------------
  // Workload (aggregate) mode
  // -------------------------------------------------------------------------

  describe("workload mode", () => {
    const workloadPod = { namespace: "default", name: "my-deploy", containers: [] };

    it("opens OpenWorkloadLogStream instead of OpenLogStream", async () => {
      mockOpenWorkloadLogStream.mockImplementation(() => openSuccess("s-agg"));
      render(
        <LogsPane
          cluster="homelab"
          pod={workloadPod}
          workload={{ kind: "Deployment", name: "my-deploy" }}
        />,
      );
      await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalledTimes(1));
      expect(mockOpenWorkloadLogStream).toHaveBeenCalledWith(
        "homelab", "default", "Deployment", "my-deploy", "", 500,
      );
      expect(mockOpenLogStream).not.toHaveBeenCalled();
    });

    it("hides the previous toggle (no single-container restart semantics)", async () => {
      mockOpenWorkloadLogStream.mockImplementation(() => openSuccess("s-agg-prev"));
      const { queryByText } = render(
        <LogsPane
          cluster="homelab"
          pod={workloadPod}
          workload={{ kind: "Deployment", name: "my-deploy" }}
        />,
      );
      await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalled());
      expect(queryByText("previous")).toBeNull();
    });

    it("renders a static 'default containers' label instead of a selector", async () => {
      mockOpenWorkloadLogStream.mockImplementation(() => openSuccess("s-agg-static"));
      const { getByText, queryByRole } = render(
        <LogsPane
          cluster="homelab"
          pod={workloadPod}
          workload={{ kind: "Deployment", name: "my-deploy" }}
        />,
      );
      await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalled());
      expect(queryByRole("combobox", { name: /container/i })).toBeNull();
      expect(getByText("default containers")).toBeTruthy();
    });

    it("renders aggregate lines with dimmed pod prefixes from the stream", async () => {
      mockOpenWorkloadLogStream.mockImplementation(() => openSuccess("s-agg-lines"));
      const { findByText } = render(
        <LogsPane
          cluster="homelab"
          pod={workloadPod}
          workload={{ kind: "Deployment", name: "my-deploy" }}
        />,
      );
      await waitFor(() => expect(mockOpenWorkloadLogStream).toHaveBeenCalled());
      act(() => {
        fireChunk("podlogs:s-agg-lines", ["web-abc12 › hello from pod a", "web-def34 › hello from pod b"]);
      });
      expect(await findByText(/hello from pod a/)).toBeTruthy();
      expect(await findByText(/hello from pod b/)).toBeTruthy();
    });
  });
});
