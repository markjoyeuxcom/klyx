import { useId, useState, type ReactNode } from "react";
import { btn, ellipsis } from "../../chrome/primitives";

// What every section is given: say sets the section's sentence (the page
// shows it under the title), ready tells the page the section has rendered
// what an anchor can name.
export type SectionProps = { say: (sentence: string) => void; ready: () => void };

export function Group({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id}>
      <h3 id={id} className="settings-group-head">{title}</h3>
      {children}
    </section>
  );
}

export function PathRow({ label, path }: { label: string; path: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 6 }}>
      <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", width: 70 }}>{label}</span>
      <span style={{ fontFamily: "var(--font-mono)", fontSize: 12, ...ellipsis }} title={path}>{path || "—"}</span>
      {path && (
        <button
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(path);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            } catch { /* clipboard unavailable */ }
          }}
          style={{ ...btn, padding: "1px 7px", fontSize: 11 }}
          aria-label={`copy ${label} path`}
        >
          {copied ? "copied" : "copy"}
        </button>
      )}
    </div>
  );
}

export function plural(n: number, w: string): string {
  return `${n} ${w}${n === 1 ? "" : "s"}`;
}

export function bytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KiB`;
  return `${n} bytes`;
}
