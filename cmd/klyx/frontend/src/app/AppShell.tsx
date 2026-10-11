import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import { NavBand } from "../chrome/NavBand";
import { ShortcutsOverlay } from "../chrome/ShortcutsOverlay";
import { FleetSpine, spineOrder } from "../chrome/FleetSpine";
import { TopBar } from "../chrome/TopBar";
import { TriageTape } from "../chrome/TriageTape";
import { FleetView } from "../fleet/FleetView";
import { ForwardsView } from "../fleet/ForwardsView";
import { ActivityView } from "../fleet/ActivityView";
import { SettingsView } from "./SettingsView";
import { ClusterDetail } from "../cluster/ClusterDetail";
import { CommandPalette, getPaletteOpen } from "../chrome/CommandPalette";
import { ActionToast } from "../chrome/ActionToast";
import { back, forward } from "../nav/history";

// AppShell owns the frame (redesign spec §1): an 8px inset, the 36px mast,
// then the fleet spine beside the 32px navigation band and the workspace,
// separated by 4px band gaps. The geometry lives in layout.css tokens so the
// first table row of a standard lens lands at y=168.
export function AppShell() {
  const route = useFleet((s) => s.route);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      // mod+[ / mod+] move through history - places only, never actions.
      // Not while the palette or a modal dialog owns the keyboard.
      if (e.key === "[" || e.key === "]") {
        if (getPaletteOpen() || document.querySelector('[aria-modal="true"]')) return;
        e.preventDefault();
        if (e.key === "[") back(); else forward();
        return;
      }
      // mod+1..9 hops to the nth cluster in the spine's visual order.
      const n = Number(e.key);
      if (!Number.isInteger(n) || n < 1 || n > 9) return;
      const c = spineOrder(useFleet.getState().clusters)[n - 1];
      if (!c) return;
      e.preventDefault();
      useFleet.getState().openCluster(c.name);
    };
    // Mouse back/forward buttons (3/4) follow the same history.
    const onMouse = (e: MouseEvent) => {
      if (e.button !== 3 && e.button !== 4) return;
      if (getPaletteOpen() || document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      if (e.button === 3) back(); else forward();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mouseup", onMouse);
    return () => { window.removeEventListener("keydown", onKey); window.removeEventListener("mouseup", onMouse); };
  }, []);

  return (
    <div className="shell">
      <CommandPalette />
      <ShortcutsOverlay />
      <TopBar />
      <div className="shell-body">
        <FleetSpine />
        <div className="shell-main">
          <NavBand />
          <main className="shell-workspace" aria-label="Workspace">
            {route.name === "cluster" && <TriageTape cluster={route.cluster} />}
            <div style={{ flex: 1, minHeight: 0, overflow: route.name === "cluster" ? "hidden" : "auto" }}>
              {route.name === "fleet" ? <FleetView /> : route.name === "forwards" ? <ForwardsView /> : route.name === "activity" ? <ActivityView /> : route.name === "settings" ? <SettingsView /> : <ClusterDetail />}
            </div>
          </main>
        </div>
      </div>
      <ActionToast />
    </div>
  );
}
