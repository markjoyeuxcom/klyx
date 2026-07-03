import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { ConfirmDialog } from "./ConfirmDialog";
import { armProtected } from "../bridge/configsvc";

// The protected confirm registers a one-shot grant with the backend before
// dispatching; mock the bridge so tests control the arm result.
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

beforeEach(() => vi.clearAllMocks());

describe("ConfirmDialog", () => {
  it("non-protected: confirm enabled immediately and fires onConfirm", async () => {
    const onConfirm = vi.fn();
    const { getByText } = render(
      <ConfirmDialog title="Reconcile" cluster="dev-ne" detail="Kustomization flux-system/app" protected={false} onConfirm={onConfirm} onCancel={() => {}} />,
    );
    const btn = getByText("Confirm") as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    // Non-protected confirms never round-trip to the backend.
    expect(armProtected).not.toHaveBeenCalled();
  });

  it("protected: confirm disabled until the cluster name is typed, then arms the backend", async () => {
    const onConfirm = vi.fn();
    const { getByText, getByPlaceholderText } = render(
      <ConfirmDialog title="Suspend" cluster="prd-we" detail="Kustomization flux-system/app" protected={true} onConfirm={onConfirm} onCancel={() => {}} />,
    );
    expect(getByText("prd lock")).toBeTruthy();
    const btn = getByText("Confirm") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    fireEvent.change(getByPlaceholderText("prd-we"), { target: { value: "prd-we" } });
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(armProtected).toHaveBeenCalledWith("prd-we", "prd-we");
  });

  it("protected: a failed arm blocks onConfirm and surfaces the error", async () => {
    vi.mocked(armProtected).mockResolvedValueOnce({ ok: false, error: "confirmation mismatch" });
    const onConfirm = vi.fn();
    const { getByText, getByPlaceholderText } = render(
      <ConfirmDialog title="Suspend" cluster="prd-we" detail="x" protected={true} onConfirm={onConfirm} onCancel={() => {}} />,
    );
    fireEvent.change(getByPlaceholderText("prd-we"), { target: { value: "prd-we" } });
    fireEvent.click(getByText("Confirm"));
    await waitFor(() => expect(getByText("confirmation mismatch")).toBeTruthy());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("cancel fires onCancel", () => {
    const onCancel = vi.fn();
    const { getByText } = render(
      <ConfirmDialog title="Reconcile" cluster="dev-ne" detail="x" protected={false} onConfirm={() => {}} onCancel={onCancel} />,
    );
    fireEvent.click(getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  // --- Dialog semantics + keyboard ---

  it("has role=dialog labelled by the title, and focuses confirm when not protected", () => {
    const { getByRole, getByText } = render(
      <ConfirmDialog title="Reconcile" cluster="dev-ne" detail="x" protected={false} onConfirm={() => {}} onCancel={() => {}} />,
    );
    const dialog = getByRole("dialog");
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    const labelId = dialog.getAttribute("aria-labelledby");
    expect(labelId).toBeTruthy();
    expect(getByText("Reconcile").id).toBe(labelId);
    // Initial focus lands on the confirm button so Escape/Enter act in the dialog.
    expect(document.activeElement).toBe(getByText("Confirm"));
  });

  it("Escape inside the dialog calls onCancel", () => {
    const onCancel = vi.fn();
    const { getByRole } = render(
      <ConfirmDialog title="Reconcile" cluster="dev-ne" detail="x" protected={false} onConfirm={() => {}} onCancel={onCancel} />,
    );
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("protected: Enter in the typed input confirms when armed and not before", async () => {
    const onConfirm = vi.fn();
    const { getByPlaceholderText } = render(
      <ConfirmDialog title="Suspend" cluster="prd-we" detail="x" protected={true} onConfirm={onConfirm} onCancel={() => {}} />,
    );
    const input = getByPlaceholderText("prd-we");
    // Not armed yet: Enter must do nothing - no arm round-trip, no dispatch.
    fireEvent.keyDown(input, { key: "Enter" });
    expect(armProtected).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
    // Armed: Enter arms the backend and dispatches.
    fireEvent.change(input, { target: { value: "prd-we" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(armProtected).toHaveBeenCalledWith("prd-we", "prd-we");
  });
});
