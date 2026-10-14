import { useEffect, useRef, useState } from "react";
import { IconCheck, IconMoon, IconSun } from "@tabler/icons-react";
import { THEMES, Theme, useTheme } from "../theme/ThemeProvider";

export function ThemeToggle() {
  const { theme, effectiveTheme, setTheme } = useTheme();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const label = THEMES.find((t) => t.id === theme)?.label ?? "Theme";

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const choose = (next: Theme) => {
    setTheme(next);
    setOpen(false);
  };

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button
        onClick={() => setOpen((v) => !v)}
        aria-label={`Theme: ${label}`}
        aria-haspopup="menu"
        aria-expanded={open}
        title="Theme"
        style={{
          display: "inline-flex", alignItems: "center", justifyContent: "center",
          width: 28, height: 28, padding: 0,
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: "var(--border-radius-md)",
          color: "var(--color-text-primary)", cursor: "pointer",
        }}
      >
        {effectiveTheme === "light" || effectiveTheme === "day" || effectiveTheme === "bench-light" ? <IconSun size={16} stroke={1.5} /> : <IconMoon size={16} stroke={1.5} />}
      </button>

      {open && (
        <div
          role="menu"
          aria-label="theme choices"
          style={{
            position: "absolute",
            top: 32,
            right: 0,
            zIndex: 60,
            width: 170,
            maxHeight: 330,
            overflowY: "auto",
            padding: 5,
            background: "var(--color-background-primary)",
            border: "0.5px solid var(--color-border-tertiary)",
            borderRadius: 5,
            boxShadow: "0 10px 24px rgba(0,0,0,0.18)",
          }}
        >
          {THEMES.map((option) => {
            const active = option.id === theme;
            return (
              <button
                key={option.id}
                role="menuitemradio"
                aria-checked={active}
                onClick={() => choose(option.id)}
                style={{
                  display: "grid",
                  gridTemplateColumns: "14px 1fr 14px",
                  alignItems: "center",
                  gap: 8,
                  width: "100%",
                  padding: "6px 7px",
                  border: 0,
                  borderRadius: 4,
                  background: active ? "var(--color-background-info)" : "transparent",
                  color: active ? "var(--color-text-info)" : "var(--color-text-primary)",
                  cursor: "pointer",
                  textAlign: "left",
                  fontSize: 12,
                }}
              >
                <span aria-hidden style={{ ...swatch(option.id), width: 10, height: 10, borderRadius: 10, border: "0.5px solid var(--color-border-secondary)" }} />
                <span>{option.label}</span>
                {active && <IconCheck size={13} stroke={1.7} />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function swatch(theme: Theme): React.CSSProperties {
  if (theme === "night") return { background: "#0E1316" };
  if (theme === "day") return { background: "#F1F2EE" };
  if (theme === "bench") return { background: "#1c1e21" };
  if (theme === "bench-light") return { background: "#eceae5" };
  if (theme === "light") return { background: "#f5f4ed" };
  if (theme === "dark") return { background: "#262624" };
  return { background: "linear-gradient(135deg, #F1F2EE 0 50%, #0E1316 50% 100%)" };
}
