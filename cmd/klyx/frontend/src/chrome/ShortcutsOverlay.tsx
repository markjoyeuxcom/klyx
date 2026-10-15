import { useEffect, useId, useRef, useState } from "react";
import { getPaletteOpen } from "./CommandPalette";
import { vh, vw } from "../app/zoom";
import { trapTab } from "./modalFocus";
import { useRestoreFocus } from "./useRestoreFocus";
import { MOD } from "./modKey";

// ShortcutsOverlay - the "?" cheatsheet. Every shortcut in the app was
// previously undiscoverable outside the palette's footer line; this overlay
// is the one place they are all written down.

const OPEN_EVENT = "klyx:open-shortcuts";
export const openShortcutsOverlay = () => window.dispatchEvent(new Event(OPEN_EVENT));

function inEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable === true;
}


const GROUPS: { title: string; rows: [string, string][] }[] = [
  {
    title: "Global",
    rows: [
      [`${MOD} K`, "command palette - jump anywhere, run actions"],
      [`${MOD} 1…9`, "switch to the nth cluster (station order)"],
      [`${MOD} [  ·  ${MOD} ]`, "back / forward between places (never re-runs an action)"],
      [`${MOD} =  ·  ${MOD} -  ·  ${MOD} 0`, "zoom in / out / actual size (80-200%)"],
      ["?", "this cheatsheet"],
      ["Esc", "close panel, dialog, or palette"],
    ],
  },
  {
    title: "Lists",
    rows: [
      ["j / ↓  ·  k / ↑", "move selection"],
      ["Enter", "open the selected row"],
      ["/", "focus the filter box"],
      ["l", "logs for the selected pod / workload"],
      ["x", "select rows for a bulk action (pods)"],
      ["r  ·  s", "a lens's own verbs, such as reconcile and suspend - each lens footer lists them"],
    ],
  },
  {
    title: "Dialogs",
    rows: [
      ["Tab", "cycle fields (focus stays trapped)"],
      ["Esc", "cancel"],
    ],
  },
];

export function ShortcutsOverlay() {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "?" && !e.metaKey && !e.ctrlKey && !e.altKey && !inEditable(e.target) && !getPaletteOpen()) {
        e.preventDefault();
        setOpen((v) => !v);
      } else if (e.key === "Escape" && open) {
        e.preventDefault();
        setOpen(false);
      }
    };
    const onOpen = () => setOpen(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(OPEN_EVENT, onOpen);
    };
  }, [open]);

  if (!open) return null;
  return <ShortcutsDialog onClose={() => setOpen(false)} />;
}

// ShortcutsDialog is the open cheatsheet. It is a real modal: focus moves to
// its Close button, Tab stays inside, and closing returns focus to whatever
// had it (Codex UI review 2026-10-13).
function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  useRestoreFocus();
  useEffect(() => {
    closeRef.current?.focus();
  }, []);

  return (
    <div
      data-testid="shortcuts-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      style={{
        position: "fixed", inset: 0, zIndex: 1500,
        background: "rgba(0,0,0,0.3)",
        display: "flex", justifyContent: "center", alignItems: "center",
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={(e) => {
          // Keys stay in the dialog: list navigation behind it never acts.
          e.stopPropagation();
          if (e.key === "Escape") {
            e.preventDefault();
            onClose();
          } else {
            trapTab(e, dialogRef.current);
          }
        }}
        style={{
          width: 480, maxWidth: vw(90), maxHeight: vh(80), overflowY: "auto",
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 11,
          boxShadow: "0 16px 48px rgba(0,0,0,0.4)",
          padding: "16px 20px 18px",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", marginBottom: 12 }}>
          <div id={titleId} style={{ fontSize: 14, fontWeight: 600 }}>Keyboard shortcuts</div>
          <button ref={closeRef} type="button" className="l-btn" style={{ marginLeft: "auto" }} onClick={onClose}>Close</button>
        </div>
        {GROUPS.map((g) => (
          <div key={g.title} style={{ marginBottom: 12 }}>
            <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.6, color: "var(--color-text-tertiary)", marginBottom: 6 }}>{g.title}</div>
            {g.rows.map(([keys, what]) => (
              <div key={keys} style={{ display: "flex", gap: 14, alignItems: "baseline", padding: "3px 0", fontSize: 12 }}>
                <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-primary)", width: 130, flexShrink: 0 }}>{keys}</span>
                <span style={{ color: "var(--color-text-secondary)" }}>{what}</span>
              </div>
            ))}
          </div>
        ))}
        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginTop: 4 }}>Esc, Close or a click outside closes this</div>
      </div>
    </div>
  );
}
