import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { OperationDialog, OptionTile } from "./OperationDialog";
import { armProtected } from "../bridge/configsvc";

// The composer registers the one-shot backend grant before dispatching; mock
// the bridge so tests control the arm result (same seam as ConfirmDialog's).
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

beforeEach(() => vi.clearAllMocks());

function dialog(over: Partial<React.ComponentProps<typeof OperationDialog>> = {}) {
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  const utils = render(
    <OperationDialog
      title="Drain node"
      cluster="prd-we"
      protected={false}
      confirmLabel="Drain"
      onConfirm={onConfirm}
      onCancel={onCancel}
      {...over}
    >
      <OptionTile title="Force" body="Evict pods without a controller." />
    </OperationDialog>,
  );
  return { onConfirm, onCancel, ...utils };
}

describe("OperationDialog", () => {
  it("renders a labelled dialog and dispatches without arming when not protected", async () => {
    const { onConfirm, getByRole } = dialog();
    expect(getByRole("dialog", { name: "Drain node" })).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Drain" }));
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(armProtected).not.toHaveBeenCalled();
  });

  it("protected: dispatch stays disabled until the cluster name is typed, then arms the backend", async () => {
    const { onConfirm, getByRole, getByPlaceholderText } = dialog({ protected: true });
    const btn = getByRole("button", { name: "Drain" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    fireEvent.change(getByPlaceholderText("prd-we"), { target: { value: "prd-we" } });
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(armProtected).toHaveBeenCalledWith("prd-we", "prd-we");
  });

  it("protected: a failed arm blocks dispatch and surfaces the error", async () => {
    vi.mocked(armProtected).mockResolvedValueOnce({ ok: false, error: "confirmation mismatch" });
    const { onConfirm, getByRole, getByPlaceholderText, getByText } = dialog({ protected: true });
    fireEvent.change(getByPlaceholderText("prd-we"), { target: { value: "prd-we" } });
    fireEvent.click(getByRole("button", { name: "Drain" }));
    await waitFor(() => expect(getByText("confirmation mismatch")).toBeTruthy());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("consumer validity gate (confirmDisabled) blocks dispatch even when armed", () => {
    const { getByRole } = dialog({ confirmDisabled: true });
    expect((getByRole("button", { name: "Drain" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("Escape cancels; overlay click cancels; panel click does not", () => {
    const { onCancel, getByRole } = dialog();
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    fireEvent.click(getByRole("dialog")); // panel click must NOT cancel
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
});
