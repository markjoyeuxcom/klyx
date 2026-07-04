import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { ActionToast } from "./ActionToast";
import { useFleet } from "../store/fleet";

beforeEach(() => {
  useFleet.getState().clearActionStatus();
  useFleet.getState().clearActionLog();
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  useFleet.getState().clearActionStatus();
});

describe("ActionToast", () => {
  it("renders nothing when the queue is empty", () => {
    const { container } = render(<ActionToast />);
    expect(container.firstChild).toBeNull();
  });

  it("renders success message with success colour and polite role", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "success", message: "Reconcile requested" });
    });
    const { getByRole } = render(<ActionToast />);
    const toast = getByRole("status");
    expect(toast.textContent).toBe("Reconcile requested");
    expect(toast.style.color).toContain("success");
  });

  it("renders errors with danger colour and role=alert", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "error", message: "Operation failed" });
    });
    const { getByRole } = render(<ActionToast />);
    const toast = getByRole("alert");
    expect(toast.textContent).toBe("Operation failed");
    expect(toast.style.color).toContain("danger");
  });

  it("stacks concurrent results instead of overwriting", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "success", message: "drain finished" });
      useFleet.getState().setActionStatus({ kind: "error", message: "sync failed" });
    });
    const { getByText } = render(<ActionToast />);
    expect(getByText("drain finished")).toBeTruthy();
    expect(getByText("sync failed")).toBeTruthy();
  });

  it("clicking a toast dismisses only that toast", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "success", message: "first" });
      useFleet.getState().setActionStatus({ kind: "success", message: "second" });
    });
    const { getByText, queryByText } = render(<ActionToast />);
    fireEvent.click(getByText("first"));
    expect(queryByText("first")).toBeNull();
    expect(getByText("second")).toBeTruthy();
  });

  it("success auto-dismisses after 6s, errors linger to 10s", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "success", message: "ok" });
      useFleet.getState().setActionStatus({ kind: "error", message: "bad" });
    });
    const { queryByText } = render(<ActionToast />);
    act(() => {
      vi.advanceTimersByTime(6000);
    });
    expect(queryByText("ok")).toBeNull();
    expect(queryByText("bad")).toBeTruthy();
    act(() => {
      vi.advanceTimersByTime(4000);
    });
    expect(queryByText("bad")).toBeNull();
  });

  it("every result lands in the persisted activity log", () => {
    act(() => {
      useFleet.getState().setActionStatus({ kind: "success", message: "one" });
      useFleet.getState().setActionStatus({ kind: "error", message: "two" });
    });
    const log = useFleet.getState().actionLog;
    expect(log[0].message).toBe("two"); // newest first
    expect(log[1].message).toBe("one");
    expect(JSON.parse(localStorage.getItem("klyx-action-log") ?? "[]").length).toBe(2);
  });
});
