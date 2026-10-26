import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import { LensIndex } from "../chrome/LensIndex";
import { toggleLensIndex } from "../nav/lensIndexOpen";
import { ShortcutsOverlay } from "../chrome/ShortcutsOverlay";
import { stationOrder } from "../nav/stationOrder";
import { LocationBar } from "../chrome/LocationBar";
import { FleetView } from "../fleet/FleetView";
import { FleetPromotion } from "../fleet/FleetPromotion";
import { FleetExpiry } from "../fleet/FleetExpiry";
import { FleetBackups } from "../fleet/FleetBackups";
import { ForwardsView } from "../fleet/ForwardsView";
import { ActivityView } from "../fleet/ActivityView";
import { SettingsView } from "./SettingsView";
import { ClusterDetail } from "../cluster/ClusterDetail";
import { CommandPalette, getPaletteOpen } from "../chrome/CommandPalette";
import { ActionToast } from "../chrome/ActionToast";
import { ChatDrawer } from "../ai/ChatDrawer";
import { LeaveDialog } from "../settings/LeaveDialog";
import { back, forward } from "../nav/history";
import { handleZoomKey } from "./zoom";
import { startSummaries } from "../bridge/healthRows";
import { startLine } from "../bridge/line";
import { startLineGit } from "../bridge/lineGit";
import { startHealthNotifications } from "../bridge/notifications";
import { startSmokeWhenAsked } from "../smoke/boot";

// AppShell owns the frame (The Line slice 4): an 8px inset, the location
// bar, then the lens index beside the workspace, separated by 4px gaps. The
// geometry lives in layout.css tokens.
// Shell shortcuts yield to whatever owns the keyboard: a handler that already
// took the event, a text field or editor (ctrl+[ is an editing key there), the
// palette, and any open dialog - a confirmation must never lose its cluster
// or its place under a stray shortcut.
function editable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || typeof el.closest !== "function") return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable === true
    || el.closest('[contenteditable="true"], [role="textbox"], [data-shell-keys="ignore"]') !== null;
}
export function shellKeysOwnedElsewhere(e: KeyboardEvent | MouseEvent): boolean {
  if (e.defaultPrevented || getPaletteOpen()) return true;
  if (document.querySelector('[aria-modal="true"], [role="dialog"]')) return true;
  return e instanceof KeyboardEvent && editable(e.target);
}

export function handleShellKey(e: KeyboardEvent): void {
  // App zoom first: it works anywhere, a text field included (app/zoom.ts).
  if (handleZoomKey(e)) return;
  if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
  const history = e.key === "[" || e.key === "]";
  const index = e.key === "\\";
  const settings = e.key === ",";
  const n = Number(e.key);
  const cluster = Number.isInteger(n) && n >= 1 && n <= 9;
  if (!history && !cluster && !index && !settings) return;
  if (shellKeysOwnedElsewhere(e)) return;
  e.preventDefault();
  // mod+, opens Settings at the section this session last showed (S1 §1).
  if (settings) { useFleet.getState().openSettings(); return; }
  // mod+\ shows or hides the lens index (slice 4 §4.3).
  if (index) { toggleLensIndex(); return; }
  // mod+[ / mod+] move through history - places only, never actions.
  if (history) { if (e.key === "[") back(); else forward(); return; }
  // mod+1..9 hops to the nth cluster in station order.
  const c = stationOrder(useFleet.getState().clusters)[n - 1];
  if (c) useFleet.getState().openCluster(c.name);
}

// Mouse back/forward buttons (3/4) follow the same history. Whether the
// webview delivers them is a native check, not something code proves.
export function handleShellMouse(e: MouseEvent): void {
  if (e.button !== 3 && e.button !== 4) return;
  if (shellKeysOwnedElsewhere(e)) return;
  e.preventDefault();
  if (e.button === 3) back(); else forward();
}

export function AppShell() {
  const route = useFleet((s) => s.route);
  // Every cluster's summary from app start (health 1c slice 5 §1): the
  // fleet indicator, the fleet board and the open cluster read them;
  // idempotent.
  useEffect(() => { startSummaries(); }, []);
  // The line from app start (slice 4 §5.1): the station strip reads it.
  useEffect(() => { startLine(); startLineGit(); startHealthNotifications(); startSmokeWhenAsked(); }, []);

  useEffect(() => {
    window.addEventListener("keydown", handleShellKey);
    window.addEventListener("mouseup", handleShellMouse);
    return () => { window.removeEventListener("keydown", handleShellKey); window.removeEventListener("mouseup", handleShellMouse); };
  }, []);

  return (
    <div className="shell">
      <CommandPalette />
      <ShortcutsOverlay />
      <LocationBar />
      <div className="shell-body">
        <LensIndex />
        <div className="shell-main">
          <main className="shell-workspace" aria-label="Workspace">
            <div style={{ flex: 1, minHeight: 0, overflow: "hidden" }}>
              {route.name === "fleet" ? (route.view === "promotion" ? <FleetPromotion /> : route.view === "expiry" ? <FleetExpiry /> : route.view === "backups" ? <FleetBackups /> : <FleetView />) : route.name === "forwards" ? <ForwardsView /> : route.name === "activity" ? <ActivityView /> : route.name === "settings" ? <SettingsView /> : <ClusterDetail />}
            </div>
          </main>
        </div>
      </div>
      <ChatDrawer />
      <ActionToast />
      <LeaveDialog />
    </div>
  );
}
