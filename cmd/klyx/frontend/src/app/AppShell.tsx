import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import { Sidebar } from "../chrome/Sidebar";
import { ShortcutsOverlay } from "../chrome/ShortcutsOverlay";
import { frame } from "../chrome/primitives";

// The content module: the machined face the active view renders inside.
const moduleShell: React.CSSProperties = { ...frame, overflow: "hidden" };
import { FleetSpine } from "../chrome/FleetSpine";
import { TopBar } from "../chrome/TopBar";
import { Header } from "../chrome/Header";
import { TriageTape } from "../chrome/TriageTape";
import { FleetView } from "../fleet/FleetView";
import { ForwardsView } from "../fleet/ForwardsView";
import { ActivityView } from "../fleet/ActivityView";
import { SettingsView } from "./SettingsView";
import { ClusterDetail } from "../cluster/ClusterDetail";
import { CommandPalette } from "../chrome/CommandPalette";
import { ActionToast } from "../chrome/ActionToast";

export function AppShell() {
  const route = useFleet((s) => s.route);

  // mod+1..9 hops to the nth cluster in fleet-spine order - the fastest
  // cluster switch for a keyboard-first operator.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      const n = Number(e.key);
      if (!Number.isInteger(n) || n < 1 || n > 9) return;
      const c = useFleet.getState().clusters[n - 1];
      if (!c) return;
      e.preventDefault();
      useFleet.getState().openCluster(c.name);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    // The bench (Signal Bench): the outermost surface is the DESK
    // (background-tertiary); every region floats on it as a machined module
    // with 8px gutters instead of being joined by hairlines. On legacy themes
    // the machining tokens fall back to hairline borders - same composition,
    // flat finish.
    <div style={{ display: "flex", flexDirection: "column", height: "100%", gap: 8, padding: 8, boxSizing: "border-box", background: "var(--color-background-tertiary)" }}>
      <CommandPalette />
      <ShortcutsOverlay />
      <TopBar />
      <div style={{ flex: 1, display: "flex", minHeight: 0, gap: 8 }}>
        <FleetSpine />
        <Sidebar />
        <div style={{ ...moduleShell, flex: 1, display: "flex", flexDirection: "column", minWidth: 0 }}>
          <Header />
          {route.name === "cluster" && <TriageTape cluster={route.cluster} />}
          <div style={{ flex: 1, minHeight: 0, overflow: route.name === "cluster" ? "hidden" : "auto" }}>
            {route.name === "fleet" ? <FleetView /> : route.name === "forwards" ? <ForwardsView /> : route.name === "activity" ? <ActivityView /> : route.name === "settings" ? <SettingsView /> : <ClusterDetail />}
          </div>
        </div>
      </div>
      <ActionToast />
    </div>
  );
}
