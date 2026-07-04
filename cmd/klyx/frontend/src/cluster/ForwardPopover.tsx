import { useState } from "react";
import { IconX } from "@tabler/icons-react";
import { startForward } from "../bridge/forwards";
import { btn } from "../chrome/primitives";

// PortSuggestion is a declared container/service port offered as a one-click
// target. name is the spec port name ("http", "metrics"), "" when unnamed.
export type PortSuggestion = { name: string; port: number };

// ForwardPopover is a tiny inline form (mirrors ScalePopover from DD3-T1): a
// target-port input (prefilled when known), a local-port input (placeholder
// "auto" => ephemeral), and a start button. On start it dispatches the bridge
// call and closes; the resulting localhost port surfaces via the action toast.
// When the caller knows the declared ports they render as clickable chips so
// nobody has to open the YAML to find a containerPort. A single declared port
// also prefills the input.
export function ForwardPopover({
  cluster, namespace, kind, name, prefillTargetPort, ports, onClose,
}: {
  cluster: string;
  namespace: string;
  kind: "Pod" | "Service";
  name: string;
  prefillTargetPort?: number;
  ports?: PortSuggestion[];
  onClose: () => void;
}) {
  const suggestions = dedupePorts(ports ?? []);
  const initialTarget = prefillTargetPort ?? (suggestions.length === 1 ? suggestions[0].port : undefined);
  const [target, setTarget] = useState(initialTarget ? String(initialTarget) : "");
  const [local, setLocal] = useState("");

  const tp = parseInt(target, 10);
  const lp = local.trim() === "" ? 0 : parseInt(local, 10);
  const targetValid = !Number.isNaN(tp) && tp > 0 && tp <= 65535;
  const localValid = local.trim() === "" || (!Number.isNaN(lp) && lp >= 0 && lp <= 65535);
  const valid = targetValid && localValid;

  const submit = () => {
    if (!valid) return;
    void startForward(cluster, namespace, kind, name, lp, tp);
    onClose();
  };

  return (
    <span
      onClick={(e) => e.stopPropagation()}
      data-testid="forward-popover"
      style={{ display: "inline-flex", alignItems: "center", gap: 4, flexWrap: "wrap", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, padding: "2px 6px", background: "var(--color-background-primary)" }}
    >
      {suggestions.map((p) => (
        <button
          key={p.port}
          onClick={() => setTarget(String(p.port))}
          aria-label={`use port ${p.port}`}
          title={p.name ? `declared port ${p.name}` : "declared port"}
          style={{
            fontSize: 10, padding: "1px 7px", borderRadius: 11, cursor: "pointer",
            border: target === String(p.port) ? "0.5px solid var(--color-text-info)" : "0.5px solid var(--color-border-tertiary)",
            background: "transparent",
            color: target === String(p.port) ? "var(--color-text-info)" : "var(--color-text-tertiary)",
            fontFamily: "var(--font-mono)",
          }}
        >
          {p.port}{p.name ? ` ${p.name}` : ""}
        </button>
      ))}
      <input
        aria-label="target port"
        type="number"
        min={1}
        max={65535}
        value={target}
        onChange={(e) => setTarget(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") submit(); else if (e.key === "Escape") onClose(); }}
        placeholder="port"
        autoFocus
        style={inputStyle}
      />
      <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>→</span>
      <input
        aria-label="local port"
        type="number"
        min={0}
        max={65535}
        value={local}
        onChange={(e) => setLocal(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") submit(); else if (e.key === "Escape") onClose(); }}
        placeholder="auto"
        style={inputStyle}
      />
      <button onClick={submit} disabled={!valid} data-testid="forward-start" style={{ ...btn, padding: "2px 7px", opacity: valid ? 1 : 0.4, cursor: valid ? "pointer" : "not-allowed" }}>forward</button>
      <button onClick={onClose} aria-label="close forward popover" style={{ ...btn, padding: "2px 6px", display: "inline-flex", alignItems: "center" }}><IconX size={12} stroke={1.5} /></button>
    </span>
  );
}

// dedupePorts collapses the same port declared by multiple containers, keeping
// the first non-empty name, sorted ascending.
function dedupePorts(ports: PortSuggestion[]): PortSuggestion[] {
  const byPort = new Map<number, PortSuggestion>();
  for (const p of ports) {
    const cur = byPort.get(p.port);
    if (!cur) byPort.set(p.port, p);
    else if (!cur.name && p.name) byPort.set(p.port, p);
  }
  return [...byPort.values()].sort((a, b) => a.port - b.port);
}

const inputStyle: React.CSSProperties = {
  width: 56, fontSize: 11, padding: "2px 4px", fontFamily: "var(--font-mono)",
  background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 3, color: "var(--color-text-primary)",
};
