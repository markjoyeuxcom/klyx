import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

const mockGetDebugCommand = vi.fn();
const mockOpenDebugTerminal = vi.fn();

// Mock bindings and runtime before importing the bridge.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  ExecService: {
    GetDebugCommand: (...args: unknown[]) => mockGetDebugCommand(...args),
    OpenDebugTerminal: (...args: unknown[]) => mockOpenDebugTerminal(...args),
    GetExecCommand: vi.fn(),
    OpenExecTerminal: vi.fn(),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Clipboard: {
    SetText: vi.fn().mockResolvedValue(undefined),
  },
}));

import { ExecService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Clipboard } from "@wailsio/runtime";
import { copyExecCommand, openExecTerminal } from "./exec";

beforeEach(() => {
  useFleet.getState().clearActionStatus();
  vi.clearAllMocks();
});

// --- copyExecCommand ---

describe("copyExecCommand", () => {
  it("calls GetExecCommand with correct args and copies Command to clipboard", async () => {
    (ExecService.GetExecCommand as ReturnType<typeof vi.fn>).mockResolvedValue({
      command: "kubectl --context prod -n default exec -it web-abc -c web -- /bin/sh -c 'command -v bash >/dev/null && exec bash || exec sh'",
      argv: ["kubectl", "--context", "prod", "-n", "default", "exec", "-it", "web-abc", "-c", "web", "--", "/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"],
      error: "",
    });

    const result = await copyExecCommand("prod", "default", "web-abc", "web");

    expect(result).toBe("copied");
    expect(ExecService.GetExecCommand).toHaveBeenCalledWith("prod", "default", "web-abc", "web");
    expect(Clipboard.SetText).toHaveBeenCalledWith(
      "kubectl --context prod -n default exec -it web-abc -c web -- /bin/sh -c 'command -v bash >/dev/null && exec bash || exec sh'",
    );
  });

  it("returns error when bridge returns error field", async () => {
    (ExecService.GetExecCommand as ReturnType<typeof vi.fn>).mockResolvedValue({
      command: "",
      argv: [],
      error: "cluster not connected: prod",
    });

    const result = await copyExecCommand("prod", "default", "web-abc", "");
    expect(result).toBe("error");
    expect(Clipboard.SetText).not.toHaveBeenCalled();
    // The reason reaches the action toast - "copy error" alone cannot say why.
    expect(useFleet.getState().actionStatus?.kind).toBe("error");
    expect(useFleet.getState().actionStatus?.message).toBe("cluster not connected: prod");
  });

  it("returns error when bridge throws", async () => {
    (ExecService.GetExecCommand as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("network error"));

    const result = await copyExecCommand("prod", "default", "web-abc", "");
    expect(result).toBe("error");
  });
});

// --- openExecTerminal ---

describe("openExecTerminal", () => {
  it("calls OpenExecTerminal with correct args and sets no actionStatus on success", async () => {
    (ExecService.OpenExecTerminal as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: true, error: "" });

    await openExecTerminal("prod", "default", "web-abc", "web");

    expect(ExecService.OpenExecTerminal).toHaveBeenCalledWith("prod", "default", "web-abc", "web");
    expect(useFleet.getState().actionStatus).toBeNull();
  });

  it("sets error actionStatus when service returns error", async () => {
    (ExecService.OpenExecTerminal as ReturnType<typeof vi.fn>).mockResolvedValue({
      ok: false,
      error: "open-terminal not supported on this platform yet - use copy command",
    });

    await openExecTerminal("prod", "default", "web-abc", "");

    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("error");
    expect(status?.message).toContain("open-terminal not supported");
  });

  it("sets error actionStatus when bridge throws", async () => {
    (ExecService.OpenExecTerminal as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("rpc error"));

    await openExecTerminal("prod", "default", "web-abc", "web");

    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("error");
  });
});

describe("debug shell bridge", () => {
  beforeEach(() => {
    mockGetDebugCommand.mockReset();
    mockOpenDebugTerminal.mockReset();
  });

  it("copyDebugCommand copies the kubectl debug string", async () => {
    mockGetDebugCommand.mockResolvedValue({ command: "kubectl --context ctx -n ns debug -it p --image=busybox:1.36 --target=c -- sh", argv: [], error: "" });
    const { copyDebugCommand } = await import("./exec");
    const r = await copyDebugCommand("cl", "ns", "p", "c");
    expect(r).toBe("copied");
    expect(mockGetDebugCommand).toHaveBeenCalledWith("cl", "ns", "p", "c");
  });

  it("openDebugTerminal surfaces errors via the action toast", async () => {
    mockOpenDebugTerminal.mockResolvedValue({ ok: false, error: "no kubectl" });
    const { openDebugTerminal } = await import("./exec");
    await openDebugTerminal("cl", "ns", "p", "uid-p", "c");
    // the pod OBJECT the debug shell was armed against (design §5b)
    expect(mockOpenDebugTerminal).toHaveBeenCalledWith("cl", "ns", "p", "uid-p", "c");
    expect(useFleet.getState().actionStatus?.kind).toBe("error");
    expect(useFleet.getState().actionStatus?.message).toBe("no kubectl");
  });
});
