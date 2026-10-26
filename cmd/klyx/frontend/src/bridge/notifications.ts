import { Events } from "@wailsio/runtime";
import { NotificationsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { useFleet } from "../store/fleet";
import { reveal } from "../nav/reveal";
import { askApply, askPlan } from "./plan";

// Health notifications (The Line slice 10, parent §8.6): the settings, the
// app's focus (nothing is sent while the app shows that cluster), faults
// listed once in the app at connect, and a notification's click opening
// its object pinned to its cluster and UID.

export type NotificationSettings = { enabled: boolean; scope: "protected" | "all"; recoveries: boolean };
export type NotificationObject = { cluster: string; task?: string; part?: string; gvr?: string; namespace?: string; name?: string; uid?: string; kind?: string; label?: string };
export type HealthBaseline = { seq: number; cluster: string; reason: "connect" | "after-cap" | "after-gap" | "dropped" | string; count: number; items: NotificationObject[] };

export const BASELINE_EVENT = "health:baseline";
export const OPEN_EVENT = "health:notification-open";

// The settings in use, and whether fleet.yaml's block differs from them
// (Settings slice F §5): a hand edit is drift until "Use the file's
// values" or a restart.
export type NotificationsState = { inUse: NotificationSettings; diverged: boolean; fileError?: string };

export async function getNotificationSettings(): Promise<NotificationsState | null> {
  try {
    const r = (await NotificationsService.Settings()) as NotificationsState;
    return r?.inUse ? r : null;
  } catch {
    return null;
  }
}

// A toggle (Settings slice F §6): a plan from what runs, applied at once
// when it ends nothing and needs no interlock; otherwise its plan comes back
// for review.
export const applyNotificationsQuiet = (s: NotificationSettings) => askApply(() => NotificationsService.ApplyNotificationsQuiet(s));
// "Use the file's values": the file's block, from what runs, writing nothing
export const planNotificationsFromFile = () => askPlan("notifications", "file", () => NotificationsService.PlanNotificationsFromFile());
export const applyNotifications = (digest: string) => askApply(() => NotificationsService.ApplyNotifications(digest));

const nameOf = (o: NotificationObject) => `${o.kind ?? ""} ${o.namespace ? `${o.namespace}/` : ""}${o.name ?? ""}`.trim();

/** baselineMessage: the one line a baseline listing says in the app. */
export function baselineMessage(b: HealthBaseline): string {
  if (b.reason === "dropped") {
    // de-duplication had no room: never evicting a guard, these were not sent (note §3.4)
    return `${b.count === 1 ? "1 notification" : `${b.count} notifications`} on ${b.cluster} not sent: the de-duplication limit is full`;
  }
  const n = b.count === 1 ? "1 fault" : `${b.count} faults`;
  const when = b.reason === "after-gap" ? "found after a gap in coverage" : b.reason === "after-cap" ? "present once the role list was back under its cap" : "present at connect";
  const names = b.items.slice(0, 3).map(nameOf).filter(Boolean);
  const more = b.count > names.length ? `, +${b.count - names.length} more` : "";
  return `${n} ${when} on ${b.cluster}${names.length ? `: ${names.join(", ")}${more}` : ""}`;
}

/** openNotified opens a notified object pinned to its cluster and UID, or a burst's cluster. */
export function openNotified(o: NotificationObject): void {
  if (o.task && o.part && o.name) {
    reveal(o.cluster, o.task, o.part, { gvr: o.gvr ?? "", namespace: o.namespace || undefined, name: o.name, uid: o.uid || undefined });
    return;
  }
  useFleet.getState().openCluster(o.cluster);
}

let started = false;
let lastFocus = "";
// Each listing is said once, whether its event or the start-up read
// delivers it. Until that read has been answered every number is
// remembered: its reply may come after any number of newer events, and its
// older entries must still be said. Once it has, only events arrive, in
// order, so only the LISTED_WINDOW numbers below the highest are kept (an
// older one is taken as said).
const LISTED_WINDOW = 64;
const listed = new Set<number>();
let highest = 0;
let bootstrapped = false;

function prune(): void {
  if (!bootstrapped) return;
  for (const n of listed) if (n <= highest - LISTED_WINDOW) listed.delete(n);
}

function listBaseline(b: HealthBaseline | undefined): void {
  if (!b || b.count <= 0 || listed.has(b.seq) || (bootstrapped && b.seq <= highest - LISTED_WINDOW)) return;
  listed.add(b.seq);
  highest = Math.max(highest, b.seq);
  prune();
  useFleet.getState().setActionStatus({ kind: "info", message: baselineMessage(b) });
}

/** _listedForTest: how many listing numbers are remembered. */
export const _listedForTest = () => listed.size;

function reportFocus(): void {
  const r = useFleet.getState().route;
  const cluster = r.name === "cluster" ? r.cluster : "";
  const focused = typeof document !== "undefined" && document.hasFocus();
  const key = `${focused}:${cluster}`;
  if (key === lastFocus) return;
  lastFocus = key;
  void Promise.resolve().then(() => NotificationsService.SetFocus(cluster, focused)).catch(() => { /* best-effort */ });
}

// startHealthNotifications is idempotent: the first caller starts it for
// the app's lifetime.
export function startHealthNotifications(): void {
  if (started) return;
  started = true;
  Events.On(BASELINE_EVENT, (ev: { data: HealthBaseline }) => listBaseline(ev?.data));
  // subscribe, then read what a cluster that connected before this webview listed
  void Promise.resolve().then(() => NotificationsService.RecentBaselines())
    .then((rs) => { for (const b of (rs ?? []) as HealthBaseline[]) listBaseline(b); })
    .catch(() => { /* the events keep arriving */ })
    .finally(() => { bootstrapped = true; prune(); });
  Events.On(OPEN_EVENT, (ev: { data: NotificationObject }) => {
    if (ev?.data?.cluster) openNotified(ev.data);
  });
  window.addEventListener("focus", reportFocus);
  window.addEventListener("blur", reportFocus);
  useFleet.subscribe((st, prev) => { if (st.route !== prev.route) reportFocus(); });
  reportFocus();
}

/** Tests only. */
export function _resetHealthNotificationsForTest(): void {
  started = false;
  lastFocus = "";
  listed.clear();
  highest = 0;
  bootstrapped = false;
  window.removeEventListener("focus", reportFocus);
  window.removeEventListener("blur", reportFocus);
}
