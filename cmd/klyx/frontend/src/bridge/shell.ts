import { Call } from "@wailsio/runtime";
import { useFleet } from "../store/fleet";
import type { Route } from "../store/fleet";

// Native-shell bridge: window title, dock/taskbar badge, OS notifications.
//
// Calls go through Call.ByID with hand-computed method IDs (FNV-1a 32 of
// "github.com/moomora/klyx/internal/appbridge.ShellService.<Method>" - the
// same scheme `wails3 generate bindings` uses) instead of the generated
// bindings, so the bridge works on checkouts that have not regenerated
// bindings. Everything here is best-effort: shell chrome must never break
// the app, so failures are swallowed.

const ID_SET_MAIN_TITLE = 203311944; // ...ShellService.SetMainTitle
const ID_SET_BADGE = 1372028942; // ...ShellService.SetBadge
const ID_NOTIFY = 2387866864; // ...ShellService.Notify

export function setMainTitle(title: string): void {
  try {
    void Call.ByID(ID_SET_MAIN_TITLE, title).catch(() => {});
  } catch {
    // no Wails runtime (tests, vite dev in a plain browser)
  }
}

export function setDockBadge(count: number): void {
  try {
    void Call.ByID(ID_SET_BADGE, count).catch(() => {});
  } catch {
    // best-effort
  }
}

export function notifyOS(title: string, body: string): void {
  try {
    void Call.ByID(ID_NOTIFY, title, body).catch(() => {});
  } catch {
    // best-effort
  }
}

// windowTitleFor renders the route as the OS window title, so ⌘-Tab and the
// taskbar say where you are. Pure + exported for tests.
export function windowTitleFor(route: Route): string {
  if (route.name === "cluster") {
    const section = route.section.charAt(0).toUpperCase() + route.section.slice(1);
    return `${route.cluster} · ${section} — Klyx`;
  }
  if (route.name === "forwards") return "Port-forwards — Klyx";
  if (route.name === "settings") return "Settings — Klyx";
  if (route.name === "activity") return "Activity — Klyx";
  if (route.name === "fleet" && route.view === "promotion") return "Promotion tracker — Klyx";
  if (route.name === "fleet" && route.view === "expiry") return "Expiry horizon — Klyx";
  return "Klyx";
}

// initShellSync mirrors app state into the native shell:
//  - route changes retitle the window
//  - the count of clusters in trouble badges the dock/taskbar icon
//  - error outcomes that land while the window is unfocused raise an OS
//    notification (focused errors already have the in-app toast)
// Returns the unsubscriber. Installed once from App bootstrap.
export function initShellSync(): () => void {
  let lastRoute: Route | undefined;
  let lastBadge = -1;
  let lastToastId = 0;

  // Seed lastToastId so notifications only fire for NEW errors, not the
  // backlog restored from the activity log.
  const seedToasts = useFleet.getState().actionToasts;
  if (seedToasts.length > 0) lastToastId = seedToasts[seedToasts.length - 1].id;

  return useFleet.subscribe((s) => {
    if (s.route !== lastRoute) {
      lastRoute = s.route;
      setMainTitle(windowTitleFor(s.route));
    }

    const badge = s.clusters.filter((c) => c.state === "Failed" || c.state === "Degraded" || c.state === "Unconnected").length;
    if (badge !== lastBadge) {
      lastBadge = badge;
      setDockBadge(badge);
    }

    const newest = s.actionToasts[s.actionToasts.length - 1];
    if (newest && newest.id > lastToastId) {
      lastToastId = newest.id;
      if (newest.kind === "error" && !document.hasFocus()) {
        notifyOS("Klyx", newest.message);
      }
    }
  });
}
