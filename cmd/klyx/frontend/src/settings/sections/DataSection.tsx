import { useEffect, useState } from "react";
import { getGitSettings } from "../../bridge/gitSettings";
import { getIndexSettings, type IndexSettings } from "../../bridge/settings";
import { STORAGE_KEYS } from "../storageKeys";
import { readStored } from "../storage";
import { SettingField } from "../SettingField";
import { StoreRow } from "./panels";
import { Group, bytes, plural, type SectionProps } from "./common";

// Settings > Data and storage (S1 §1): what Klyx keeps, read-only, and the
// search index's kinds and budgets as the index runs with them (file-only;
// editable in S6). Actions come in S6.

const WINDOW_STORES = ["store.klyx-route", "store.klyx-view-prefs", "store.klyx-view-search", "store.klyx-palette-frecency", "store.klyx-action-log"];

function windowBytes(): number {
  let n = 0;
  for (const k of Object.keys(STORAGE_KEYS) as (keyof typeof STORAGE_KEYS)[]) {
    const v = readStored(k);
    if (v !== null) n += new Blob([v]).size;
  }
  return n;
}

export function DataSection({ say, ready }: SectionProps) {
  const [cache, setCache] = useState<{ bytes: number; repos: number } | null | undefined>(undefined);
  const [index, setIndex] = useState<IndexSettings | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    void Promise.all([getGitSettings(), getIndexSettings()]).then(([g, ix]) => {
      if (!live) return;
      setCache(g ? { bytes: g.repos.reduce((n, r) => n + (r.size || 0), 0), repos: g.repos.length } : null);
      setIndex(ix);
      ready();
    });
    return () => { live = false; };
  }, [ready]);
  useEffect(() => {
    const w = `this window keeps ${bytes(windowBytes())}`;
    say(cache ? `The Git cache holds ${bytes(cache.bytes)} for ${plural(cache.repos, "repository").replace("repositorys", "repositories")}; ${w}` : `Klyx keeps its files beside fleet.yaml; ${w}`);
  }, [cache, say]);
  const n = (v: number | undefined) => (v === undefined ? "reading…" : v.toLocaleString("en-GB"));
  return (
    <>
      <Group title="Beside fleet.yaml">
        <SettingField id="native.window.json" source="running" value="kept by Klyx" />
        <SettingField id="native.claude-builds.json" source="running" value="kept by Klyx" />
        <SettingField id="native.git-cache" source="running" value={cache === undefined ? "reading…" : cache === null ? "unknown: Git is off or unreadable" : `${bytes(cache.bytes)} for ${plural(cache.repos, "repository").replace("repositorys", "repositories")}`} />
      </Group>
      <Group title="In this window's storage">
        <p className="settings-group-note">Per-viewer conveniences, kept in the webview&apos;s storage and never sent anywhere. Credentials are in the OS keychain, never here: Settings &gt; AI and Observability say whether each is stored.</p>
        {WINDOW_STORES.map((id) => <StoreRow key={id} id={id} />)}
      </Group>
      <Group title="The search index">
        <p className="settings-group-note">Configured limits, as the index runs with them; a change in fleet.yaml applies at restart. They are not the fixed ceilings Diagnostics lists.</p>
        <SettingField id="index.kinds" source="running" value={index === undefined ? "reading…" : index === null ? "unknown" : index.kinds.length === 0 ? "none: nothing is indexed" : index.kinds.join(", ")} />
        <SettingField id="index.entriesPerCluster" source="running" value={index === null ? "unknown" : n(index?.entriesPerCluster)} />
        <SettingField id="index.mibPerCluster" source="running" value={index === null ? "unknown" : index === undefined ? "reading…" : `${index.mibPerCluster} MiB`} />
        <SettingField id="index.entriesFleet" source="running" value={index === null ? "unknown" : n(index?.entriesFleet)} />
        <SettingField id="index.mibFleet" source="running" value={index === null ? "unknown" : index === undefined ? "reading…" : `${index.mibFleet} MiB`} />
      </Group>
    </>
  );
}
