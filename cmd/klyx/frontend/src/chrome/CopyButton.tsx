import { useState } from "react";
import { IconCheck, IconCopy } from "@tabler/icons-react";
import { copyText } from "../bridge/crd";

/**
 * CopyButton — the one copy-to-clipboard affordance. A platform engineer
 * copies resource names into a terminal all day; every list row and detail
 * surface uses this instead of manual text selection.
 *
 * With `appearOnRowHover` the button is invisible until its row (any ancestor
 * carrying data-rowhost) is hovered, or the button itself is focused - the
 * reveal rule lives in tokens.css. Click never bubbles to the row.
 */
export function CopyButton({
  text,
  label,
  size = 12,
  appearOnRowHover = false,
  style,
}: {
  text: string;
  label: string;
  size?: number;
  appearOnRowHover?: boolean;
  style?: React.CSSProperties;
}) {
  const [copied, setCopied] = useState(false);

  return (
    <button
      aria-label={label}
      title={copied ? "Copied" : label}
      data-rowcopy={appearOnRowHover ? "true" : undefined}
      onClick={(e) => {
        e.stopPropagation();
        copyText(text)
          .then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          })
          .catch(() => {
            // clipboard unavailable - the title flip just never happens
          });
      }}
      style={{
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        width: size + 8,
        height: size + 8,
        padding: 0,
        border: "none",
        background: "transparent",
        cursor: "pointer",
        color: copied ? "var(--color-text-success)" : "var(--color-text-tertiary)",
        borderRadius: 3,
        flexShrink: 0,
        ...style,
      }}
    >
      {copied ? <IconCheck size={size} stroke={1.5} /> : <IconCopy size={size} stroke={1.5} />}
    </button>
  );
}
