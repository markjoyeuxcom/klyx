import { useLayoutEffect, useState } from "react";
import { createPortal } from "react-dom";
import { IconSelector } from "@tabler/icons-react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { AXIS_TITLE } from "../nav/fleetAxes";
import { stationLabel, stationOrder } from "../nav/stationOrder";
import { vh } from "../app/zoom";
import { AxisMark, StationName, firstAxis, useFleetAxes } from "./FleetIndicator";
import { useMenuPopover } from "./useMenuPopover";
import { MOD } from "./modKey";

// The station crumb and its switcher (The Line slice 4 §2.1): the fleet
// indicator lists only affected clusters, so the crumb keeps the spine's
// one-click reach to every cluster - in station order, each with its
// indicator mark, mod+1..9 hinted beside the first nine.
export function StationSwitcher({ current }: { current: ClusterDTO }) {
  const clusters = useFleet((s) => s.clusters);
  const openCluster = useFleet((s) => s.openCluster);
  const f = useFleetAxes();
  const { open, setOpen, close, wrapRef, triggerRef, menuRef } = useMenuPopover();
  const order = stationOrder(clusters);
  // The crumbs clip to truncate, so the menu hangs from a portal, placed
  // under the crumb; a resize or scroll closes it rather than leave it adrift.
  const [at, setAt] = useState<{ top: number; left: number } | null>(null);
  useLayoutEffect(() => {
    if (!open) return;
    const b = triggerRef.current?.getBoundingClientRect();
    setAt(b ? { top: b.bottom + 6, left: b.left } : null);
    const shut = () => setOpen(false);
    window.addEventListener("resize", shut);
    return () => window.removeEventListener("resize", shut);
  }, [open, triggerRef, setOpen]);
  return (
    <div ref={wrapRef} className="locbar-pop">
      <button ref={triggerRef} type="button" className="locbar-crumb locbar-station" aria-haspopup="menu" aria-expanded={open}
        aria-label={`Station ${[stationLabel(current).place, current.name].filter(Boolean).join(", ")}. Switch cluster`}
        title={`${[stationLabel(current).place, current.name].filter(Boolean).join(" ")} - switch cluster`} onClick={() => setOpen((o) => !o)}>
        <StationName c={current} />
        <IconSelector aria-hidden size={12} stroke={1.6} style={{ flexShrink: 0, color: "var(--color-text-tertiary)" }} />
      </button>
      {open && createPortal((
        <div ref={menuRef} role="menu" aria-label="Stations" className="l-menu station-menu" style={{ position: "fixed", top: at?.top ?? 0, left: at?.left ?? 0, maxHeight: vh(70), visibility: at ? undefined : "hidden" }}>
          {order.map((c, i) => {
            const axis = firstAxis(f, c.name);
            const here = c.name === current.name;
            return (
              <button key={c.name} type="button" role="menuitem" className="l-menu-item station-row" aria-current={here ? "page" : undefined}
                aria-label={`${c.name}${axis ? `, ${AXIS_TITLE[axis].toLowerCase()}` : ""}${here ? ", current" : ""}`}
                onClick={() => { close(false); if (!here) openCluster(c.name); }}>
                <span className="station-row-mark">{axis && <AxisMark axis={axis} />}</span>
                <StationName c={c} />
                {i < 9 && <kbd className="station-row-key" aria-hidden>{MOD}{i + 1}</kbd>}
              </button>
            );
          })}
        </div>
      ), document.body)}
    </div>
  );
}
