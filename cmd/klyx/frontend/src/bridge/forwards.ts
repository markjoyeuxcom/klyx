import { Events } from "@wailsio/runtime";
import { useFleet, ForwardDTO } from "../store/fleet";
import { ForwardsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const FORWARDS_CHANGED = "forwards:changed";

type StartForwardResultDTO = { forward: ForwardDTO | null; error: string };
type ActionResultDTO = { ok: boolean; error: string };

// initForwardsBridge seeds the forwards slice from Go and subscribes to the
// "forwards:changed" push event (one full-list replace per mutation). Installed
// once from App bootstrap; returns an unsubscribe for the unmount path. Mirrors
// initFleetBridge.
export async function initForwardsBridge(): Promise<() => void> {
  try {
    const seed = await ForwardsService.ListForwards();
    useFleet.getState().setForwards((seed ?? []) as unknown as ForwardDTO[]);
  } catch (e) {
    console.error("forwards seed", e);
  }

  const off = Events.On(FORWARDS_CHANGED, (ev) => {
    const data = (ev.data ?? []) as ForwardDTO[];
    // A forward that flips active→broken died in the background. Without a
    // toast the only signal is a chip changing colour in a view nobody is
    // looking at - surface it through the action queue (and activity log).
    const prev = useFleet.getState().forwards;
    for (const f of data) {
      const before = prev.find((p) => p.id === f.id);
      if (before && before.status === "active" && f.status === "broken") {
        useFleet.getState().setActionStatus({
          kind: "error",
          message: `port-forward localhost:${f.localPort} → ${f.targetName}:${f.targetPort} broke`,
        });
      }
    }
    useFleet.getState().setForwards(data);
  });

  return typeof off === "function" ? off : () => {};
}

// startForward begins a port-forward to a Pod or Service. On success a toast
// announces the bound localhost port; on failure the error surfaces in the toast.
// The forwards list updates via the push event, not this return value.
export async function startForward(
  cluster: string,
  namespace: string,
  kind: "Pod" | "Service",
  name: string,
  localPort: number,
  targetPort: number,
): Promise<void> {
  try {
    const r = (await ForwardsService.StartForward(
      cluster,
      namespace,
      kind,
      name,
      localPort,
      targetPort,
    )) as StartForwardResultDTO;
    if (r.forward) {
      useFleet.getState().setActionStatus({
        kind: "success",
        message: `forwarding localhost:${r.forward.localPort} → ${name}:${r.forward.targetPort}`,
      });
    } else {
      useFleet.getState().setActionStatus({ kind: "error", message: r.error || "Port-forward failed" });
    }
  } catch (e) {
    useFleet.getState().setActionStatus({ kind: "error", message: String(e) });
  }
}

// stopForward tears down one forward. Idempotent on the Go side; the list
// updates via the push event.
export async function stopForward(id: string): Promise<void> {
  try {
    (await ForwardsService.StopForward(id)) as ActionResultDTO;
  } catch (e) {
    console.error("stopForward", e);
  }
}

// stopAllForwards tears down every active forward.
export async function stopAllForwards(): Promise<void> {
  try {
    await ForwardsService.StopAll();
  } catch (e) {
    console.error("stopAllForwards", e);
  }
}
