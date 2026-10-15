import { useCallback, useEffect, useRef, useState } from "react";

// useMenuPopover is the location bar's popover behaviour, shared by the
// recent places, the station switcher and the fleet indicator: opening
// focuses the first menu item; arrows, Home and End rove over the items;
// Escape closes and returns focus to the trigger; Tab and an outside click
// close. Keys are taken in the capture phase so a list behind the popover
// never sees them. A menu rendered in a portal still counts as inside.
export function useMenuPopover() {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const close = useCallback((refocus = true) => {
    setOpen(false);
    if (refocus) triggerRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (wrapRef.current?.contains(t) || menuRef.current?.contains(t)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setOpen(false); triggerRef.current?.focus(); return; }
      if (e.key === "Tab") { setOpen(false); return; }
      const list = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
      if (list.length === 0) return;
      const i = list.indexOf(document.activeElement as HTMLElement);
      let n = -1;
      if (e.key === "ArrowDown") n = i < 0 ? 0 : (i + 1) % list.length;
      else if (e.key === "ArrowUp") n = i < 0 ? list.length - 1 : (i - 1 + list.length) % list.length;
      else if (e.key === "Home") n = 0;
      else if (e.key === "End") n = list.length - 1;
      if (n >= 0) { e.preventDefault(); e.stopPropagation(); list[n].focus(); }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey, true); };
  }, [open]);

  return { open, setOpen, close, wrapRef, triggerRef, menuRef };
}
