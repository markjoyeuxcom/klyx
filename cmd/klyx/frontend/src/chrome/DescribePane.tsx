import { useEffect, useId, useRef, useState } from "react";
import { IconX } from "@tabler/icons-react";
import { describeResource, type DescribeResultDTO } from "../bridge/exec";
import { CopyButton } from "./CopyButton";
import { well } from "./primitives";
import { vh, vw } from "../app/zoom";

// DescribePane is the read-only `kubectl describe` overlay shared by the pod,
// node, and generic instance panels. Same module shell as OperationDialog but
// with no verb to dispatch: title, copy-all, an inset well holding the raw
// kubectl output, Escape/✕ to close. Errors (kubectl missing, object gone,
// RBAC denial) render in the pane, never as silently empty output.
export function DescribePane({
  cluster,
  kindArg,
  namespace,
  name,
  onClose,
}: {
  cluster: string;
  // kindArg is the kubectl resource argument: "pod"/"node" for the core
  // panels, plural.group (plural alone when group is "") for typed instances.
  kindArg: string;
  // namespace is "" for cluster-scoped objects (nodes, clusterroles, ...).
  namespace: string;
  name: string;
  onClose: () => void;
}) {
  const [result, setResult] = useState<DescribeResultDTO | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();

  useEffect(() => {
    let cancelled = false;
    void describeResource(cluster, kindArg, namespace, name).then((r) => {
      if (!cancelled) setResult(r);
    });
    return () => {
      cancelled = true;
    };
  }, [cluster, kindArg, namespace, name]);

  // Focus the dialog so Escape works immediately; restore focus to whatever
  // opened the pane once it closes (same discipline as OperationDialog).
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    dialogRef.current?.focus();
    return () => {
      prev?.focus?.();
    };
  }, []);

  const qualified = namespace ? `${namespace}/${name}` : name;

  return (
    <div
      onClick={onClose}
      style={{ position: "fixed", inset: 0, zIndex: 1000, background: "rgba(0,0,0,0.48)", display: "flex", alignItems: "center", justifyContent: "center", padding: 24 }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          // Swallow keys at the dialog boundary so list navigation never acts
          // behind the overlay; Escape closes.
          e.stopPropagation();
          if (e.key === "Escape") onClose();
        }}
        style={{
          width: `min(760px, calc(${vw(100)} - 48px))`,
          height: `min(560px, calc(${vh(100)} - 64px))`,
          display: "flex",
          flexDirection: "column",
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 6,
          boxShadow: "0 18px 50px rgba(0,0,0,0.35)",
          color: "var(--color-text-primary)",
          outline: "none",
        }}
      >
        {/* Header */}
        <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "12px 16px", borderBottom: "0.5px solid var(--color-border-tertiary)", flexShrink: 0 }}>
          <span
            id={titleId}
            title={`describe · ${qualified}`}
            style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 600, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
          >
            <span style={{ color: "var(--color-text-tertiary)", fontWeight: 400 }}>describe · </span>
            {qualified}
          </span>
          <div style={{ flex: 1 }} />
          {result && result.output !== "" && <CopyButton text={result.output} label={`copy describe output for ${qualified}`} />}
          <button
            aria-label="close describe"
            onClick={onClose}
            style={{ border: "0.5px solid var(--color-border-tertiary)", background: "transparent", color: "var(--color-text-secondary)", borderRadius: 4, width: 24, height: 24, cursor: "pointer", flexShrink: 0, display: "inline-flex", alignItems: "center", justifyContent: "center" }}
          >
            <IconX size={14} stroke={1.5} />
          </button>
        </div>

        {/* Body: the kubectl output in an inset well */}
        <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: 12 }}>
          {!result ? (
            <div style={{ ...well, flex: 1, display: "flex", alignItems: "center", justifyContent: "center", fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)" }}>
              running kubectl describe…
            </div>
          ) : (
            <div style={{ ...well, flex: 1, minHeight: 0, overflow: "auto", padding: 10 }}>
              {result.error !== "" && (
                <div role="alert" style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-danger)", whiteSpace: "pre-wrap", marginBottom: result.output ? 8 : 0 }}>
                  {result.error}
                </div>
              )}
              {result.output !== "" && (
                <pre style={{ margin: 0, fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: 1.5, whiteSpace: "pre-wrap", wordBreak: "break-word", userSelect: "text", color: "var(--color-text-primary)" }}>
                  {result.output}
                </pre>
              )}
              {result.error === "" && result.output === "" && (
                <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)" }}>kubectl produced no output.</div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
