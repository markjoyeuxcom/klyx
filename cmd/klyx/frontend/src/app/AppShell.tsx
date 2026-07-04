import { useFleet } from "../store/fleet";
import { Sidebar } from "../chrome/Sidebar";
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
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <CommandPalette />
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
