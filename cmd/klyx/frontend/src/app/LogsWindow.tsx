import { useEffect } from "react";
import { LogsPane } from "../cluster/LogsPane";
import { LogHistoryPane } from "../cluster/LogHistoryPane";
import { PopoutBar } from "../chrome/PopoutBar";
import { handleZoomKey } from "./zoom";

// LogsWindow is the root rendered in a pop-out log window (URL flag logswin=1).
// It boots NONE of the normal app: no fleet subscriptions, no sidebar, no
// command palette. It reads the target from the query string and hosts a single
// LogsPane filling the window. The OS window provides the close button, so there
// is no in-app close affordance.
//
// params defaults to window.location.search but is injectable for tests.
export function LogsWindow({ params }: { params?: URLSearchParams }) {
  const q = params ?? new URLSearchParams(window.location.search);
  const cluster = q.get("cluster") ?? "";
  const namespace = q.get("ns") ?? "";
  // mode=workload pops an AGGREGATE tail: name/kind identify the workload and
  // the pane opens OpenWorkloadLogStream instead of a single-pod stream.
  const isWorkload = q.get("mode") === "workload";
  const name = (isWorkload ? q.get("name") : q.get("pod")) ?? "";
  const kind = q.get("kind") ?? "";
  const container = q.get("container") ?? "";
  // mode=history: the pod's log history from Loki, pinned to its UID (S8b)
  const isHistory = q.get("mode") === "history";
  const uid = q.get("uid") ?? "";
  const created = Number(q.get("created") ?? "") || undefined;
  const gone = q.get("gone") === "1";

  // App zoom (initialised in main.tsx for both roots) - only the zoom keys,
  // never the main window's navigation shortcuts.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => void handleZoomKey(e);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div
      data-testid="logs-window"
      style={{
        display: "flex",
        flexDirection: "column",
        // 100% of the html/body/#root chain, never 100vh: viewport units are
        // not zoom-compensated, so 100vh overflows a zoomed window.
        height: "100%",
        boxSizing: "border-box",
        background: "var(--color-background-primary)",
        color: "var(--color-text-primary)",
      }}
    >
      {/* The pop-out mast: the main mast's draggable strip clear of the
          traffic lights, without the main window's navigation. */}
      <PopoutBar title={isHistory ? "Log history" : "Logs"} />

      {/* Identity line below the bar - the popout's equivalent of the main
          window's breadcrumb zone. */}
      <div style={{ display: "flex", alignItems: "baseline", gap: 8, flexShrink: 0, padding: "10px 12px 0" }}>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 500 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{namespace}</span>/{name}
        </span>
        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>{cluster}</span>
      </div>

      {/* LogsPane fills the rest. containers:[] + initialContainer drives the
          static-container path (the window has no pod summary). */}
      <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: "8px 12px 12px" }}>
        {isHistory && uid ? (
          <LogHistoryPane cluster={cluster} target={{ namespace, name, uid, createdUnix: created, gone, containers: [] }} />
        ) : (
          <LogsPane
            cluster={cluster}
            pod={{ namespace, name, containers: [] }}
            workload={isWorkload ? { kind, name } : undefined}
            initialContainer={container}
            hostedInWindow
          />
        )}
      </div>
    </div>
  );
}
