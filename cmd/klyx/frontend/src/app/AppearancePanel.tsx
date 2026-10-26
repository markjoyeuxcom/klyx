import { useEffect, useState } from "react";
import { THEMES, useTheme } from "../theme/ThemeProvider";
import { SettingField } from "../settings/SettingField";
import { ZOOM_EVENT, ZOOM_STEPS, getZoom, setZoom, zoomReset } from "./zoom";
import { MOD } from "../chrome/modKey";

// Settings > Appearance (The Line slice 4 §2; Settings S1 §1): the theme's
// home now that the bar's toggle is gone, and zoom with its reset, from the
// existing zoom store (no new preference). Night and day are the app's look;
// classic light and dark stay, and bench is listed while it retires. The
// palette's theme commands set the same choice.
export function AppearancePanel() {
  const { theme, setTheme } = useTheme();
  const zoom = useZoom();
  return (
    <>
      <SettingField id="store.klyx-theme" group source="this window"
        note={'System follows the operating system into night or day. The command palette sets the same theme ("theme: night", "cycle theme").'}>
        {() => (
          <div className="appearance">
            {THEMES.map((t) => (
              <label key={t.id} className="appearance-choice">
                <input type="radio" name="klyx-theme" value={t.id} checked={theme === t.id} onChange={() => setTheme(t.id)} />
                <span>{t.label}</span>
              </label>
            ))}
          </div>
        )}
      </SettingField>
      <SettingField id="store.klyx.layout.v1.zoom" source={zoom === 1 ? "default" : "this window"}
        note={`${MOD} = and ${MOD} - step it anywhere; ${MOD} 0 resets it.`}>
        {(ids) => (<>
          <select id={ids.id} aria-describedby={ids.describedBy} className="l-input" style={{ flex: "0 0 auto", width: 120 }} value={String(zoom)} onChange={(e) => setZoom(Number(e.target.value))}>
            {ZOOM_STEPS.map((z) => <option key={z} value={String(z)}>{Math.round(z * 100)}%</option>)}
          </select>
          <button type="button" className="l-btn" disabled={zoom === 1} onClick={() => zoomReset()}>Reset to 100%</button>
        </>)}
      </SettingField>
    </>
  );
}

// useZoom follows the zoom, whichever window or key changed it.
function useZoom(): number {
  const [z, setZ] = useState(getZoom);
  useEffect(() => {
    const on = () => setZ(getZoom());
    window.addEventListener(ZOOM_EVENT, on);
    return () => window.removeEventListener(ZOOM_EVENT, on);
  }, []);
  return z;
}
