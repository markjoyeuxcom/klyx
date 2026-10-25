import { useId, useMemo, useState } from "react";
import { useFleet } from "../store/fleet";
import { SECTION_TITLES, SETTINGS_SECTIONS, isSettingsSection, searchSettings, type SettingEntry, type SettingsSection } from "./registry";

// The Settings rail (S1 §1): the lens index's slot and component, showing
// the sections instead of lenses, with a search field at its top. Each entry
// is a button with aria-current; the index's width, mod+\ and keys hold.
export function SettingsRail() {
  const route = useFleet((s) => s.route);
  const current: SettingsSection = route.name === "settings" && isSettingsSection(route.section) ? route.section : "fleet";
  const [query, setQuery] = useState("");
  const results = useMemo(() => searchSettings(query), [query]);
  const head = useId();
  return (
    <>
      <div className="settings-search">
        <input
          type="search"
          className="l-input"
          aria-label="Search settings"
          placeholder="Search settings"
          data-setting-exempt="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Escape" && query) { e.stopPropagation(); setQuery(""); } }}
        />
      </div>
      {query.trim() ? <Results results={results} /> : (
        <section className="lens-index-section" aria-labelledby={head}>
          <h2 id={head} className="lens-index-head">Settings</h2>
          {SETTINGS_SECTIONS.map((s) => (
            <button key={s} type="button" className="lens-entry" data-section={s} aria-current={s === current ? "page" : undefined}
              onClick={() => useFleet.getState().openSettings(s)}>
              <span className="lens-entry-label">{SECTION_TITLES[s]}</span>
            </button>
          ))}
        </section>
      )}
    </>
  );
}

// Results lists matches by section; choosing one opens its section at the
// entry's anchor (a repeated field opens its list, every instance marked).
function Results({ results }: { results: SettingEntry[] }) {
  const head = useId();
  if (results.length === 0) return <p className="lens-index-absent" role="status">No setting matches.</p>;
  return (
    <>
      <p className="lens-index-absent" role="status">{results.length} setting{results.length === 1 ? "" : "s"}</p>
      {SETTINGS_SECTIONS.filter((s) => results.some((r) => r.section === s)).map((s) => (
        <section key={s} className="lens-index-section" aria-labelledby={`${head}-${s}`}>
          <h2 id={`${head}-${s}`} className="lens-index-head">{SECTION_TITLES[s]}</h2>
          {results.filter((r) => r.section === s).map((r) => (
            <button key={r.id} type="button" className="lens-entry settings-result" data-result={r.id}
              onClick={() => useFleet.getState().openSettings(r.section, r.id)}>
              <span className="lens-entry-label">{r.label}</span>
              <span className="lens-entry-note">{r.effect}</span>
            </button>
          ))}
        </section>
      ))}
    </>
  );
}
