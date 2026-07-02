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
});
