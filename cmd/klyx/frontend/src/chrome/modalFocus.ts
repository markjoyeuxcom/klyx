// Shared modal focus handling: every Klyx dialog keeps keyboard focus inside
// itself while open (a basic focus trap - no dependency needed). Initial focus
// and restoring focus on close stay with each dialog (useRestoreFocus).

export const FOCUSABLE =
  'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

// trapTab wraps Tab and Shift+Tab between the container's first and last
// focusable elements. It also pulls focus back in when it has escaped (a click
// on the backdrop, or a control removed under the cursor).
export function trapTab(e: { key: string; shiftKey: boolean; preventDefault: () => void }, container: HTMLElement | null): void {
  if (e.key !== "Tab" || !container) return;
  const focusable = Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE));
  if (focusable.length === 0) {
    e.preventDefault();
    return;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  const active = document.activeElement;
  if (!active || !container.contains(active)) {
    e.preventDefault();
    (e.shiftKey ? last : first).focus();
  } else if (e.shiftKey && active === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}
