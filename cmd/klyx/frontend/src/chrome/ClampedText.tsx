import { useState, type CSSProperties, type ReactNode } from "react";

// ClampedText shows a long controller or kubelet message as a few lines, with
// an explicit "Show full message" control (UI review 2026-10-13: inspectors
// dumped raw messages as long red paragraphs). The whole text stays in the
// page - selectable, copyable and findable - only its height is clipped.
// Nothing is summarised or rewritten.

const CLAMP_CHARS = 180;

export function ClampedText({ text, lines = 3, prefix, style, className }: {
  text: string;
  lines?: number;
  prefix?: ReactNode;
  style?: CSSProperties;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const long = text.length > CLAMP_CHARS || text.split("\n").length > lines;
  return (
    <span className={className} style={{ display: "block", ...style }}>
      <span
        data-testid="clamped-text"
        style={{ color: style?.color, ...(long && !open ? { display: "-webkit-box", WebkitLineClamp: lines, WebkitBoxOrient: "vertical", overflow: "hidden" } : {}) }}
      >
        {prefix}{text}
      </span>
      {long && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          style={{ background: "none", border: 0, padding: 0, marginTop: 2, font: "inherit", fontSize: 11, color: "var(--color-text-info)", cursor: "pointer" }}
        >
          {open ? "Show less" : "Show full message"}
        </button>
      )}
    </span>
  );
}
