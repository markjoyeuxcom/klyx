import { describe, it, expect, vi } from "vitest";
import { useState } from "react";
import { render, fireEvent, act } from "@testing-library/react";
import { ConfirmDialog } from "./ConfirmDialog";
import { OperationDialog } from "./OperationDialog";

vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

// A trigger button that opens the dialog; closing returns focus to it.
function Harness({ kind, isProtected }: { kind: "confirm" | "operation"; isProtected: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button onClick={() => setOpen(true)}>open</button>
      <button>elsewhere</button>
      {open && kind === "confirm" && (
        <ConfirmDialog title="Delete" cluster="prd" detail="pod x" protected={isProtected} onConfirm={() => setOpen(false)} onCancel={() => setOpen(false)} />
      )}
      {open && kind === "operation" && (
        <OperationDialog title="Scale" cluster="prd" protected={isProtected} confirmLabel="Scale" onConfirm={() => setOpen(false)} onCancel={() => setOpen(false)}>
          body
        </OperationDialog>
      )}
    </div>
  );
}

// Review 2026-10-03 (I): both dialogs recorded their OWN control (the
// autofocused input or the initially focused confirm button) as the element to
// restore, so on close focus fell to <body>.
describe("dialog focus restore", () => {
  for (const kind of ["confirm", "operation"] as const) {
    for (const isProtected of [false, true]) {
      it(`${kind} dialog (protected=${isProtected}) returns focus to its invoker on close`, async () => {
        const { getByText } = render(<Harness kind={kind} isProtected={isProtected} />);
        const trigger = getByText("open");
        trigger.focus();
        fireEvent.click(trigger);
        // Focus moved INTO the dialog (input or confirm button).
        expect(document.activeElement).not.toBe(trigger);
        await act(async () => {
          fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
        });
        expect(document.activeElement).toBe(trigger);
      });
    }
  }
});
