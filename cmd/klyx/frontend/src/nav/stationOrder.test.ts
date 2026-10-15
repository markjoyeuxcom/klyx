import { describe, it, expect } from "vitest";
import type { ClusterDTO } from "../store/fleet";
import { stationGroups, stationLabel, stationOrder } from "./stationOrder";

// The Line slice 4 §2.2: station order is env rank, then fleet order - the
// spine's order, kept so mod+1..9 keep their targets.

const cluster = (name: string, env = "", region = ""): ClusterDTO => ({ name, env, region, state: "Synced", reason: "" } as ClusterDTO);

describe("station order", () => {
  it("groups dev → test → stg → prd, config order within, unknown then untagged last", () => {
    const cs = [cluster("aks-prd-weu", "PRD"), cluster("aks-dev-weu", "dev"), cluster("lab"), cluster("aks-stg-weu", "staging"), cluster("aks-prd-neu", "prod"), cluster("aks-dev-neu", "dev"), cluster("edge", "edge")];
    expect(stationGroups(cs).map((g) => [g.env, g.clusters.map((c) => c.name)])).toEqual([
      ["dev", ["aks-dev-weu", "aks-dev-neu"]],
      ["staging", ["aks-stg-weu"]],
      ["prd", ["aks-prd-weu"]],
      ["prod", ["aks-prd-neu"]],
      ["edge", ["edge"]],
      ["other", ["lab"]],
    ]);
    expect(stationOrder(cs).map((c) => c.name)).toEqual(["aks-dev-weu", "aks-dev-neu", "aks-stg-weu", "aks-prd-weu", "aks-prd-neu", "edge", "lab"]);
  });

  it("no environment tags, or one environment for all: one flat list in fleet order", () => {
    const untagged = [cluster("b"), cluster("a")];
    expect(stationGroups(untagged)).toEqual([{ env: null, clusters: untagged }]);
    const homelab = [cluster("b", "homelab"), cluster("a", "homelab")];
    expect(stationGroups(homelab)).toEqual([{ env: null, clusters: homelab }]);
    expect(stationOrder(homelab).map((c) => c.name)).toEqual(["b", "a"]);
  });

  it("a station's label: env · region when either is set, then its name", () => {
    expect(stationLabel(cluster("aks-prd-weu", "prd", "westeurope"))).toEqual({ place: "prd · West Europe", name: "aks-prd-weu" });
    expect(stationLabel(cluster("nelli", "homelab"))).toEqual({ place: "homelab", name: "nelli" });
    expect(stationLabel(cluster("kind-klyx"))).toEqual({ place: "", name: "kind-klyx" });
  });
});
