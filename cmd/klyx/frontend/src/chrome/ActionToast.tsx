import { useEffect, useRef } from "react";
import { useFleet } from "../store/fleet";
import type { ActionEvent } from "../store/fleet";

const SUCCESS_DISMISS_MS = 6000;
const ERROR_DISMISS_MS = 10000;

/**
 * ActionToast — global fixed-position action feedback.
 *
 * Mount once inside AppShell. Renders the store's actionToasts queue as a
 * bottom-center stack (newest at the bottom), so a drain finishing while a
 * sync fails shows both instead of the second overwriting the first. Each
 * toast auto-dismisses on its own timer - errors linger longer and announce
 * via role=alert. Clicking a toast dismisses it. The full history stays in
 * the Activity view.
 */
export function ActionToast() {
  const toasts = useFleet((s) => s.actionToasts);
  if (toasts.length === 0) return null;

  return (
    <div
      style={{
        position: "fixed",
        bottom: 20,
        left: "50%",
        transform: "translateX(-50%)",
        zIndex: 2000,
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        gap: 8,
      }}
    >
      {toasts.map((t) => (
        <ToastCard key={t.id} toast={t} />
      ))}
    </div>
  );
}

function ToastCard({ toast }: { toast: ActionEvent }) {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isError = toast.kind === "error";

  useEffect(() => {
    timerRef.current = setTimeout(
      () => useFleet.getState().dismissActionToast(toast.id),
      isError ? ERROR_DISMISS_MS : SUCCESS_DISMISS_MS,
    );
    return () => {
      if (timerRef.current !== null) clearTimeout(timerRef.current);
    };
  }, [toast.id, isError]);

  return (
    <div
      role={isError ? "alert" : "status"}
      onClick={() => useFleet.getState().dismissActionToast(toast.id)}
      style={{
        padding: "8px 16px",
        borderRadius: 6,
        fontSize: 12,
        fontFamily: "var(--font-sans, sans-serif)",
        cursor: "pointer",
        border: "0.5px solid var(--color-border-secondary)",
        background: "var(--color-background-primary)",
        color: isError ? "var(--color-text-danger)" : "var(--color-text-success)",
        boxShadow: "0 2px 8px rgba(0,0,0,0.18)",
        maxWidth: 480,
        wordBreak: "break-word",
        textAlign: "center",
      }}
    >
      {toast.message}
    </div>
  );
}
