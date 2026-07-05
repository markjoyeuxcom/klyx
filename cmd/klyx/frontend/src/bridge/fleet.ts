import { useFleet, ClusterDTO } from "../store/fleet";
import { Events } from "@wailsio/runtime";
import { FleetService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { restoreRouteOnBoot } from "../store/persist";

const FLEET_UPDATED = "fleet:updated";

export async function initFleetBridge(): Promise<() => void> {
  // Seed the store with the current fleet from Go.
  // GetFleet returns ClusterDTO class instances from the generated binding;
  // they have the same JSON shape as the store's plain ClusterDTO type.
  const seed = ((await FleetService.GetFleet()) ?? []) as unknown as ClusterDTO[];
  useFleet.getState().setClusters(seed);
  // Session restore: land where the last run ended, now that the cluster list
  // can vouch for the saved cluster still existing.
  restoreRouteOnBoot(seed.map((c) => c.name));

  // Subscribe to live push updates.
  const off = Events.On(FLEET_UPDATED, (ev) => {
    const data = (ev.data ?? []) as ClusterDTO[];
    useFleet.getState().setClusters(data);
  });

  return typeof off === "function" ? off : () => {};
}
