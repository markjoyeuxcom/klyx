import { Events } from "@wailsio/runtime";
import { useFleet, EventsResultDTO } from "../store/fleet";
import { EventsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// liveOpenRetryMs paces re-attempts when OpenLiveEvents reports the cluster is
// not connected yet (app launch race: the view can mount before the registry
// finishes connecting).
export const liveOpenRetryMs = 3000;

export async function fetchEventsSnapshot(cluster: string, namespace: string): Promise<EventsResultDTO> {
  const r = (await EventsService.ListEvents(cluster, namespace)) as EventsResultDTO;
  return r ?? { namespaces: [], events: [] };
}

export async function listEvents(cluster: string, namespace: string): Promise<void> {
  useFleet.getState().setEventsLoading(cluster, namespace);
  try {
    const r = await fetchEventsSnapshot(cluster, namespace);
    // Drop a stale response if the user changed cluster OR namespace while in flight.
    const cur = useFleet.getState().events;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setEvents(cluster, namespace, r ?? { namespaces: [], events: [] });
  } catch {
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    if (useFleet.getState().events.cluster === cluster) {
      useFleet.setState((s) => ({ events: { ...s.events, loading: false } }));
    }
  }
}

// openLiveEvents subscribes to live event updates for a cluster+namespace.
// The backend fires an immediate emit so the view receives data without calling
// listEvents first. Returns a cleanup function to be called on unmount or ns change.
export function openLiveEvents(cluster: string, namespace: string): () => void {
  // Claim the store slice BEFORE anything can emit: the data handler and
  // setEventsLive both guard on store cluster+namespace, which the previous
  // unmount's clearEvents left null - without this the first emit after mount
  // is silently dropped. Also flips the empty state to "Loading…".
  useFleet.getState().setEventsLoading(cluster, namespace);

  const dataEvent = "liveEvents:" + cluster + ":" + namespace;
  const statusEvent = "liveEventsStatus:" + cluster + ":" + namespace;

  const offData = Events.On(dataEvent, (ev: { data: EventsResultDTO }) => {
    const cur = useFleet.getState().events;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setEvents(cluster, namespace, ev.data ?? { namespaces: [], events: [] });
  });

  const offStatus = Events.On(statusEvent, (ev: { data: { live: boolean } }) => {
    useFleet.getState().setEventsLive(cluster, namespace, ev.data?.live ?? false);
  });

  // Open the live sub. "cluster not connected" comes back as ok:false (a value,
  // not a throw - the app-launch race). Degrade honestly and retry until the
  // cluster connects or the view unmounts.
  let closed = false;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  function degrade() {
    useFleet.getState().setEventsLive(cluster, namespace, false);
    void listEvents(cluster, namespace);
    retryTimer = setTimeout(tryOpen, liveOpenRetryMs);
  }
  function tryOpen() {
    EventsService.OpenLiveEvents(cluster, namespace)
      .then((r) => {
        if (closed || (r as { ok?: boolean } | undefined)?.ok) return;
        degrade();
      })
      .catch(() => {
        if (!closed) degrade();
      });
  }
  tryOpen();

  return () => {
    closed = true;
    if (retryTimer !== undefined) clearTimeout(retryTimer);
    if (typeof offData === "function") offData();
    if (typeof offStatus === "function") offStatus();
    EventsService.CloseLiveEvents(cluster, namespace).catch(() => undefined);
  };
}
