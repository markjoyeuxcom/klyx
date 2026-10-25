import { useCallback, useEffect, useRef, useState } from "react";
import { IconAlertTriangle, IconSettings } from "@tabler/icons-react";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout } from "../layout/LensLayout";
import { getFleetConfig } from "../bridge/configsvc";
import type { FleetConfigDTO } from "../bridge/configsvc";
import { useFleet } from "../store/fleet";
import { SECTION_TITLES, isSettingsSection, parseAnchor, settingEntry, type SettingsSection } from "../settings/registry";
import { sayInSettings, useSettingsNotice } from "../settings/notice";
import { FleetSection, sentenceForFleet } from "../settings/sections/FleetSection";
import { AISection, AppearanceSection, GitSection, NotificationsSection, ObservabilitySection } from "../settings/sections/panels";
import { DataSection } from "../settings/sections/DataSection";
import { DiagnosticsSection } from "../settings/sections/DiagnosticsSection";

// a configuration problem, said before anything else on the page
const problem: React.CSSProperties = { display: "flex", gap: 8, alignItems: "flex-start", fontSize: 12, padding: "8px 10px", marginBottom: 12, border: "0.5px solid currentColor", borderRadius: "var(--border-radius-md)" };

// SettingsView is Settings (S1 §1): one section at a time, under its
// sentence, addressed by the route's section and anchor. The rail beside it
// (the lens index's slot) lists the sections and searches every setting.

const quote = (s: string) => `"${s.replace(/["\\]/g, "\\$&")}"`;

// applyAnchor scrolls to the setting an anchor names and focuses its
// control, or its read-only row; a repeated field's id marks every
// instance's row. It returns what to say when the anchor names nothing here.
function applyAnchor(root: HTMLElement, anchor: string): string | null {
  root.querySelectorAll("[data-anchored]").forEach((el) => el.removeAttribute("data-anchored"));
  const { id, instance } = parseAnchor(anchor);
  const e = settingEntry(id);
  if (!e) return "That setting is not in this version of Klyx.";
  const exact = instance !== undefined ? Array.from(root.querySelectorAll<HTMLElement>(`[data-anchor=${quote(anchor)}]`)) : [];
  const all = Array.from(root.querySelectorAll<HTMLElement>(`[data-setting=${quote(id)}]`));
  const targets = instance !== undefined ? exact : all;
  if (targets.length === 0) {
    all.forEach((el) => el.setAttribute("data-anchored", ""));
    all[0]?.scrollIntoView?.({ block: "center" });
    return instance !== undefined ? `${e.label} for ${instance} is no longer there.` : `${e.label} is not shown here right now.`;
  }
  targets.forEach((el) => {
    el.setAttribute("data-anchored", "");
    for (let p = el.parentElement; p && p !== root; p = p.parentElement) if (p instanceof HTMLDetailsElement) p.open = true;
  });
  const first = targets[0];
  first.scrollIntoView?.({ block: "center" });
  const control = first.querySelector<HTMLElement>(".setting-control input:not([type=hidden]), .setting-control select, .setting-control textarea");
  (control ?? first).focus();
  return null;
}

export function SettingsView() {
  const route = useFleet((s) => s.route);
  const section: SettingsSection = route.name === "settings" && isSettingsSection(route.section) ? route.section : "fleet";
  const anchor = route.name === "settings" ? route.anchor : undefined;
  const notice = useSettingsNotice((s) => s.notice);
  const [cfg, setCfg] = useState<FleetConfigDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const page = useRef<HTMLDivElement | null>(null);
  const shown = useRef<SettingsSection | null>(null);
  // Each showing of a section is a generation. A section's sentence and its
  // readiness count only for the generation that said them: a child's
  // effects run before this component's, so nothing is reset here after a
  // child spoke (the native smoke found a reset clearing a ready Fleet).
  const generation = useRef({ section: null as SettingsSection | null, n: 0 });
  if (generation.current.section !== section) generation.current = { section, n: generation.current.n + 1 };
  const gen = generation.current.n;
  const [sentence, setSentence] = useState<{ gen: number; text: string }>({ gen: 0, text: "" });
  const [readyGen, setReadyGen] = useState(0);

  const load = async () => {
    setLoading(true);
    const dto = await getFleetConfig();
    setCfg(dto);
    setLoading(false);
  };
  useEffect(() => {
    void load();
  }, []);

  // a notice belongs to the section it was said on
  useEffect(() => {
    if (shown.current !== null && shown.current !== section) sayInSettings(null);
    shown.current = section;
  }, [section]);
  // the same sentence keeps the same state, or a section that says it on
  // every render would render forever
  const say = useCallback((text: string) => setSentence((cur) => (cur.gen === gen && cur.text === text ? cur : { gen, text })), [gen]);
  const ready = useCallback(() => setReadyGen(gen), [gen]);

  // an anchor applies once its section has rendered what it can name, and
  // then leaves the route (it is where in the section, not a place)
  useEffect(() => {
    if (!anchor || readyGen !== gen || !page.current) return;
    // applied in the effect itself, after the commit: never on an animation
    // frame, which a hidden or background window may not run (the native
    // smoke's window is hidden)
    const missing = applyAnchor(page.current, anchor);
    if (missing) sayInSettings(missing);
    useFleet.setState((s) => (s.route.name === "settings" && s.route.anchor === anchor ? { route: { name: "settings", section } } : {}));
  }, [anchor, readyGen, gen, section]);

  const fleetSentence = cfg ? sentenceForFleet(cfg) : "";
  const summary = section === "fleet" ? fleetSentence : sentence.gen === gen ? sentence.text : "";
  const content = (() => {
    switch (section) {
      case "fleet":
        return cfg ? <FleetSection cfg={cfg} reload={() => void load()} say={say} ready={ready} /> : null;
      case "observability":
        return <ObservabilitySection say={say} ready={ready} />;
      case "ai":
        return <AISection say={say} ready={ready} />;
      case "git":
        return <GitSection say={say} ready={ready} />;
      case "notifications":
        return <NotificationsSection say={say} ready={ready} />;
      case "appearance":
        return <AppearanceSection say={say} ready={ready} />;
      case "data":
        return <DataSection say={say} ready={ready} />;
      case "diagnostics":
        return <DiagnosticsSection say={say} ready={ready} />;
    }
  })();

  const frame = (state: "loading" | "unreadable" | "ready", stateView: React.ReactNode, body: React.ReactNode) => (
    <LensLayout
      label="Settings"
      title={SECTION_TITLES[section]}
      summary={summary ? <span className="t-secondary" data-testid="settings-sentence">{summary}</span> : undefined}
      headerEnd={section === "fleet" ? <button className="l-btn" onClick={() => void load()} disabled={loading}>{loading ? "Reading…" : "Re-scan kubeconfig contexts"}</button> : undefined}
      state={state}
      stateView={stateView}
      body={body}
    />
  );
  if (section === "fleet" && loading && !cfg) return frame("loading", <SkeletonRows rows={4} label="reading the fleet configuration" />, null);
  if (section === "fleet" && !cfg) return frame("unreadable", <EmptyState icon={<IconSettings size={28} stroke={1.2} />} title="Could not load the fleet configuration." action={{ label: "Retry", onClick: () => void load() }} />, null);

  return frame("ready", null, (
    <div ref={page} className="ov-page settings-page" data-section={section}>
      {notice && <p className="settings-notice" role="status" data-testid="settings-notice">{notice}</p>}
      {cfg?.loadError && (
        // Settings review slice 0: never an empty fleet without saying why
        <div data-testid="settings-load-error" role="alert" style={{ ...problem, color: "var(--color-text-danger)" }}>
          <IconAlertTriangle size={13} stroke={1.7} style={{ flexShrink: 0, marginTop: 2 }} aria-hidden />
          <div>
            <div style={{ fontWeight: 600 }}>fleet.yaml did not load when Klyx started, so Klyx connected no cluster.</div>
            <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere", margin: "4px 0" }}>{cfg.loadError}</div>
            <div>Fix the file in an editor, then restart Klyx. Settings cannot repair it, and adding clusters waits until it loads.</div>
          </div>
        </div>
      )}
      {cfg && !cfg.loadError && cfg.fileError && (
        <div data-testid="settings-file-error" role="status" style={{ ...problem, color: "var(--color-text-warning)" }}>
          <IconAlertTriangle size={13} stroke={1.7} style={{ flexShrink: 0, marginTop: 2 }} aria-hidden />
          <div>
            fleet.yaml does not load as it is now, so Klyx keeps running the fleet it read at startup. Fix it before Klyx restarts:{" "}
            <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere" }}>{cfg.fileError}</span>
          </div>
        </div>
      )}
      {content}
    </div>
  ));
}
