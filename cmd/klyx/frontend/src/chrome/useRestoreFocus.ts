import { useEffect, useState } from "react";

// useRestoreFocus returns focus, when a dialog closes, to whatever had it when
// the dialog FIRST rendered. The invoker is captured during render - before
// the dialog's own autoFocus input or initial-focus effect moves focus inside
// it. Capturing in an effect (as both dialogs did) recorded the dialog's own
// control, so on close focus fell to <body> and a keyboard flow lost its place
// (review 2026-10-03, I).
//
// Focus is restored only when it was LOST (left on <body> by the dialog's
// removal): never stolen from wherever the confirm action deliberately moved
// it, nor during StrictMode's simulated unmount, while the dialog is still
// attached.
export function useRestoreFocus(): void {
  const [invoker] = useState(() =>
    typeof document === "undefined" ? null : (document.activeElement as HTMLElement | null),
  );
  useEffect(
    () => () => {
      const lost = !document.activeElement || document.activeElement === document.body;
      if (lost && invoker && invoker !== document.body && invoker.isConnected) invoker.focus();
    },
    [invoker],
  );
}
