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

  // A3 stage 1 review, finding 2: while what the confirmation rests on is
  // read, neither the button nor Enter in the typed field dispatches, and
  // no grant is armed.
  it("waiting: nothing confirms, not even Enter in the typed field", async () => {
    const onConfirm = vi.fn();
    const props = { title: "roll back", cluster: "prd-we", detail: "deployment shop/web", protected: true, onConfirm, onCancel: () => {} };
    const { getByText, getByPlaceholderText, rerender } = render(<ConfirmDialog {...props} waiting="reading" />);
    const input = getByPlaceholderText("prd-we");
    fireEvent.change(input, { target: { value: "prd-we" } });
    expect((getByText("Confirm") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(input, { key: "Enter" });
    await Promise.resolve();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(armProtected).not.toHaveBeenCalled();
    rerender(<ConfirmDialog {...props} />);
    fireEvent.keyDown(getByPlaceholderText("prd-we"), { key: "Enter" });
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
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

  describe("danger verbs start on Cancel (2026-10-10 accidental suspend)", () => {
    // jsdom does not synthesise a click from Enter on a focused button; the
    // browser does. Model it: Enter on the focused element activates it.
    const pressEnter = () => { const el = document.activeElement as HTMLElement; fireEvent.keyDown(el, { key: "Enter" }); el.click(); };

    it("a shortcut then a stray Enter does not dispatch a danger verb", () => {
      const onConfirm = vi.fn(); const onCancel = vi.fn();
      const { getByRole } = render(<ConfirmDialog title="Suspend reconciliation" cluster="dev" detail="HelmRelease team-b/podinfo" protected={false} danger confirmLabel="Suspend" onConfirm={onConfirm} onCancel={onCancel} />);
      expect(document.activeElement).toBe(getByRole("button", { name: "Cancel" }));
      pressEnter();
      expect(onConfirm).not.toHaveBeenCalled();
      expect(onCancel).toHaveBeenCalledTimes(1);
    });

    it("deliberate Tab to the confirm button then Enter dispatches", async () => {
      const onConfirm = vi.fn();
      const { getByRole } = render(<ConfirmDialog title="Suspend reconciliation" cluster="dev" detail="x" protected={false} danger confirmLabel="Suspend" onConfirm={onConfirm} onCancel={() => {}} />);
      getByRole("button", { name: "Suspend" }).focus();
      pressEnter();
      await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    });

    it("Escape cancels; a non-danger verb keeps Enter-to-confirm", async () => {
      const onCancel = vi.fn();
      const first = render(<ConfirmDialog title="Suspend" cluster="dev" detail="x" protected={false} danger onConfirm={() => {}} onCancel={onCancel} />);
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Escape" });
      expect(onCancel).toHaveBeenCalledTimes(1);
      first.unmount();
      const onConfirm = vi.fn();
      const { getByRole } = render(<ConfirmDialog title="Resume" cluster="dev" detail="x" protected={false} confirmLabel="Resume" onConfirm={onConfirm} onCancel={() => {}} />);
      expect(document.activeElement).toBe(getByRole("button", { name: "Resume" }));
      pressEnter();
      await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    });

    it("focus returns to the invoker on close", () => {
      const invoker = document.createElement("button"); invoker.textContent = "s"; document.body.appendChild(invoker); invoker.focus();
      const { unmount } = render(<ConfirmDialog title="Suspend" cluster="dev" detail="x" protected={false} danger onConfirm={() => {}} onCancel={() => {}} />);
      expect(document.activeElement).not.toBe(invoker);
      unmount();
      expect(document.activeElement).toBe(invoker);
      invoker.remove();
    });
  });

  it("is capped to the window and scrolls a long detail, so Cancel and Confirm stay reachable at any zoom", () => {
    const { getByRole, getByTestId } = render(
      <ConfirmDialog title="Delete 40 pods" cluster="dev-ne" detail={Array.from({ length: 40 }, (_, i) => `shop/pod-${i}`).join("\n")} protected={false} onConfirm={() => {}} onCancel={() => {}} />,
    );
    const dialog = getByRole("dialog");
    expect(dialog.style.maxHeight).toBe("calc(100% - 32px)");
    expect(dialog.style.display).toBe("flex");
    expect(getByTestId("confirm-detail").style.overflowY).toBe("auto");
    expect(getByTestId("confirm-detail").style.minHeight).toBe("0px");
  });
});
