import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import { Sidebar } from "../chrome/Sidebar";
import { ShortcutsOverlay } from "../chrome/ShortcutsOverlay";
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
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <CommandPalette />
      <ShortcutsOverlay />
      <TopBar />
      <div style={{ flex: 1, display: "flex", minHeight: 0 }}>
        <FleetSpine />
        <Sidebar />
        <div style={{ flex: 1, display: "flex", flexDirection: "column", minWidth: 0, background: "var(--color-background-primary)" }}>
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
