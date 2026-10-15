import { useId } from "react";
import { THEMES, useTheme } from "../theme/ThemeProvider";

// Settings > Appearance (The Line slice 4 §2): the theme's home now that the
// bar's toggle is gone. Night and day are the app's look; classic light and
// dark stay, and bench is listed while it retires. The palette's theme
// commands set the same choice.
export function AppearancePanel() {
  const { theme, setTheme } = useTheme();
  const name = useId();
  return (
    <fieldset className="appearance" aria-describedby={`${name}-note`}>
      <legend className="sr-only">Theme</legend>
      {THEMES.map((t) => (
        <label key={t.id} className="appearance-choice">
          <input type="radio" name={name} value={t.id} checked={theme === t.id} onChange={() => setTheme(t.id)} />
          <span>{t.label}</span>
        </label>
      ))}
      <p id={`${name}-note`} className="appearance-note">
        System follows the operating system into night or day. The command palette sets the same theme ("theme: night", "cycle theme").
      </p>
    </fieldset>
  );
}
