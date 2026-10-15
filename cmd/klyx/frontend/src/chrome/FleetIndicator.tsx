import { useMemo } from "react";
import { IconAntennaBarsOff, IconPlugConnectedX } from "@tabler/icons-react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { useFleetAttention } from "../nav/fleetAttention";
import { AXIS_TITLE, FLEET_AXES, fleetAxes, fleetAxesLabel, type FleetAxes, type FleetAxis } from "../nav/fleetAxes";
import { stationLabel } from "../nav/stationOrder";
import { clusterDisplayName } from "../fleet/clusterName";
import { vh } from "../app/zoom";
import { Aspect } from "./primitives";
import { useMenuPopover } from "./useMenuPopover";

// The fleet indicator (The Line slice 4 §3): per-axis counts of clusters,
// on every route. It replaces the spine's pager: no lamp column, no blink
// (owner trade-off, parent §14.4). Each axis has its own mark, and every
// mark pairs with words in the accessible name.

export function AxisMark({ axis, size = 8 }: { axis: FleetAxis; size?: number }) {
  switch (axis) {
    case "fault": return <Aspect kind="fault" size={size} />;
    case "warning": return <Aspect kind="attention" size={size} />;
    case "unverified": return <Aspect kind="checking" size={size} />;
    case "unreachable": return <IconPlugConnectedX aria-hidden size={size + 5} stroke={1.6} style={{ flexShrink: 0, color: "var(--color-text-secondary)" }} />;
    case "connection": return <IconAntennaBarsOff aria-hidden size={size + 5} stroke={1.6} style={{ flexShrink: 0, color: "var(--color-text-warning)" }} />;
  }
}

// firstAxis is a cluster's mark where one mark stands for it (the station
// switcher): its first axis in chip order, or none.
export function firstAxis(f: FleetAxes, cluster: string): FleetAxis | null {
  return FLEET_AXES.find((a) => f.axes[a].some((e) => e.cluster.name === cluster)) ?? null;
}

export function useFleetAxes(): FleetAxes {
  const clusters = useFleet((s) => s.clusters);
  const models = useFleetAttention();
  return useMemo(() => fleetAxes(clusters, models), [clusters, models]);
}

export function StationName({ c }: { c: ClusterDTO }) {
  const { place, name } = stationLabel(c);
  return (
    <span className="station-name">
      {place && <span className="station-place">{place}</span>}
      <span className="station-cluster" data-bare={place ? undefined : ""}>{clusterDisplayName(name)}</span>
    </span>
  );
}

export function FleetIndicator() {
  const f = useFleetAxes();
  const openCluster = useFleet((s) => s.openCluster);
  const { open, setOpen, close, wrapRef, triggerRef, menuRef } = useMenuPopover();
  if (f.total === 0) return null;
  const label = fleetAxesLabel(f);
  const lit = FLEET_AXES.filter((a) => f.axes[a].length > 0);
  return (
    <div ref={wrapRef} className="locbar-pop">
      <button ref={triggerRef} type="button" className="fleet-ind" aria-label={label} title={label} aria-haspopup="menu" aria-expanded={open}
        aria-controls={open ? "fleet-indicator-list" : undefined} onClick={() => setOpen((o) => !o)}>
        {lit.length === 0
          ? <span className="fleet-ind-total">{f.total} cluster{f.total === 1 ? "" : "s"}</span>
          : lit.map((a) => (
            <span key={a} className="fleet-ind-axis" data-axis={a}>
              <AxisMark axis={a} />
              <span>{f.axes[a].length}</span>
            </span>
          ))}
      </button>
      {open && (
        <div ref={menuRef} id="fleet-indicator-list" role="menu" aria-label="Fleet" className="l-menu fleet-ind-menu" style={{ maxHeight: vh(70) }}>
          {lit.length === 0 && (
            <p className="fleet-ind-none">No cluster has a fault, a warning, a connection problem or an unverified reading.</p>
          )}
          {lit.map((a) => (
            <div key={a} role="group" aria-label={`${AXIS_TITLE[a]}, ${f.axes[a].length}`} className="fleet-ind-group">
              <div className="l-menu-label" aria-hidden>{AXIS_TITLE[a]} · {f.axes[a].length}</div>
              {f.axes[a].map((e) => (
                <button key={e.cluster.name} type="button" role="menuitem" className="l-menu-item fleet-ind-row" title={`${e.cluster.name} - ${e.reason}`}
                  onClick={() => { close(false); openCluster(e.cluster.name); }}>
                  <AxisMark axis={a} />
                  <span className="fleet-ind-text">
                    <StationName c={e.cluster} />
                    <span className="fleet-ind-reason">{e.reason}</span>
                  </span>
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
