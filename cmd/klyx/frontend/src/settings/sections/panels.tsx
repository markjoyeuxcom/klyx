import { useEffect, useRef } from "react";
import { EndpointsPanel } from "../../app/EndpointsPanel";
import { AISettingsPanel, providerLabel } from "../../app/AISettingsPanel";
import { GitSettingsPanel } from "../../app/GitSettingsPanel";
import { NotificationSettingsPanel } from "../../app/NotificationSettingsPanel";
import { AppearancePanel } from "../../app/AppearancePanel";
import type { EndpointsState } from "../../bridge/endpoints";
import type { AISettingsState } from "../../bridge/aiSettings";
import type { GitSettings } from "../../bridge/gitSettings";
import { useHealth } from "../../store/health";
import { useTheme, THEMES } from "../../theme/ThemeProvider";
import { getZoom } from "../../app/zoom";
import { SettingField } from "../SettingField";
import { ENTRIES } from "../registry";
import { Group, bytes, plural, type SectionProps } from "./common";
import { isStorageKey, readStored } from "../storage";

// The sections that hold the existing panels (S1 §1), each with its
// sentence and the read-only rows for the rest of its entries.

// The fields of one kind of endpoint, described once under the panel.
function EndpointFields({ kind, title }: { kind: "metrics" | "alertmanager" | "loki"; title: string }) {
  const ids = ENTRIES.filter((e) => e.id.startsWith(`clusters[].${kind}.`)).map((e) => e.id);
  return (
    <details className="settings-fields" data-fields={kind}>
      <summary>What each {title} field does</summary>
      {ids.map((id) => <SettingField key={id} id={id} source="file" value="per cluster, in fleet.yaml" />)}
    </details>
  );
}

// observabilitySentence says where a source is established - configured,
// or found in the cluster - never that a read succeeded; where the health
// summary has the alerts part's last read, it is said with it.
export function observabilitySentence(rows: EndpointsState | null, alertParts: { cluster: string; coverage: string }[]): string {
  const am = new Set((rows?.rows ?? []).filter((r) => r.kind === "alertmanager").map((r) => r.cluster));
  const loki = new Set((rows?.rows ?? []).filter((r) => r.kind === "loki").map((r) => r.cluster));
  const read = new Set<string>();
  for (const p of alertParts) {
    if (p.coverage === "live" || p.coverage === "stale") am.add(p.cluster);
    if (p.coverage === "live") read.add(p.cluster);
  }
  const amText = am.size === 0 ? "No Alertmanager configured or found" : `Alertmanager configured or found for ${plural(am.size, "cluster")}${read.size > 0 ? ` (alerts read now for ${read.size})` : ""}`;
  const lokiText = loki.size === 0 ? "no Loki configured" : `Loki configured for ${loki.size}`;
  return `${amText}; ${lokiText}`;
}

function useAlertParts(): { cluster: string; coverage: string }[] {
  const summaries = useHealth((s) => s.summaries);
  const out: { cluster: string; coverage: string }[] = [];
  for (const [cluster, sum] of Object.entries(summaries)) {
    for (const task of sum.tasks) for (const p of task.parts) if (p.classifier === "alerts") out.push({ cluster, coverage: p.coverage });
  }
  return out;
}

export function ObservabilitySection({ say, ready }: SectionProps) {
  const parts = useAlertParts();
  const partsKey = parts.map((p) => `${p.cluster}:${p.coverage}`).join(",");
  const last = useRef<EndpointsState | null>(null);
  const onLoaded = (s: EndpointsState) => {
    last.current = s;
    say(observabilitySentence(s, parts));
    ready();
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { if (last.current) say(observabilitySentence(last.current, parts)); }, [partsKey]);
  return (
    <>
      <Group title="External endpoints">
        <EndpointsPanel onLoaded={onLoaded} />
      </Group>
      <Group title="Endpoint fields">
        <p className="settings-group-note">What each field of a cluster&apos;s metrics, alertmanager and loki block does. They are set in fleet.yaml in this version.</p>
        <EndpointFields kind="metrics" title="metrics" />
        <EndpointFields kind="alertmanager" title="Alertmanager" />
        <EndpointFields kind="loki" title="Loki" />
        <SettingField id="clusters[].metrics.token" source="file" value="per cluster: External endpoints says where each token is" />
      </Group>
      <Group title="Installations">
        <p className="settings-group-note">
          An installation is an Alertmanager several clusters share, written once under sources in fleet.yaml; each cluster it serves subscribes to it, unless the cluster has an alertmanager block of its own. They are set in fleet.yaml in this version, and Klyx reads them when it starts. A later version reads alerts through them; until then a subscribed cluster&apos;s alerts are said to be unread.
        </p>
        <InstallationFields />
      </Group>
    </>
  );
}

// The fields of an installation (Settings S3a §2.1), described once.
function InstallationFields() {
  const ids = ENTRIES.filter((e) => e.id.startsWith("sources[].")).map((e) => e.id);
  return (
    <details className="settings-fields" data-fields="sources">
      <summary>What each installation field does</summary>
      {ids.map((id) => <SettingField key={id} id={id} source="file" value="per installation, in fleet.yaml" />)}
    </details>
  );
}

export function aiSentence(s: AISettingsState): string {
  const a = s.active;
  if (a.backend === "off" || !a.backend) return "AI is off";
  if (!a.ready) return `AI is unavailable: ${a.problem ?? "it did not build"}`;
  return `Prompts go to ${a.origin} · ${providerLabel(a.backend)} · ${a.model}`;
}

export function AISection({ say, ready }: SectionProps) {
  return (
    <>
      <Group title="AI in use">
        <AISettingsPanel onState={(s) => { say(aiSentence(s)); ready(); }} />
      </Group>
      <Group title="Kept for older files">
        <SettingField id="env.purpose.ai-key" source="environment" value="read only when the key source is env" />
        <SettingField id="ai.backend" source="file" value="read if present; Save writes the provider" />
        <SettingField id="ai.apiKey" source="file" value="read if present; never shown" />
      </Group>
    </>
  );
}

export function gitSentence(g: GitSettings): string {
  const b = g.binary;
  if (!b.ready) return "Git is starting";
  if (b.off) return `Git is off: ${b.off}`;
  return `git ${b.version} at ${b.path}; ${plural(g.repos.length, "approved repository").replace("repositorys", "repositories")}`;
}

export function GitSection({ say, ready }: SectionProps) {
  return (
    <>
      <Group title="Git history">
        <GitSettingsPanel onSettings={(g) => { say(gitSentence(g)); ready(); }} />
      </Group>
      <Group title="Not yet offered">
        <SettingField id="git.repos[].remoteBases" source="file" value="not supported" />
      </Group>
    </>
  );
}

export function NotificationsSection({ say, ready }: SectionProps) {
  return (
    <Group title="Health notifications">
      <NotificationSettingsPanel onSettings={(n) => { say(n.enabled ? `On, for ${n.scope === "all" ? "every cluster" : "protected clusters"}${n.recoveries ? ", with recoveries" : ""}` : "Off"); ready(); }} />
    </Group>
  );
}

const LAYOUT_STORES = ["store.klyx.layout.v1.inspectorWidth", "store.klyx.layout.v1.lensIndex", "store.klyx-logs-dock-height", "store.klyx.yaml.wrap", "store.klyx-theme-line-migrated", "store.klyx-theme-bench-migrated"];

export function AppearanceSection({ say, ready }: SectionProps) {
  const { theme } = useTheme();
  const label = THEMES.find((t) => t.id === theme)?.label ?? theme;
  useEffect(() => { say(`${label} theme, at ${Math.round(getZoom() * 100)}% zoom`); ready(); });
  return (
    <>
      <Group title="Look">
        <AppearancePanel />
      </Group>
      <Group title="Kept where you use them">
        <p className="settings-group-note">Set by using the app - dragging an edge, toggling the index - and kept in this window&apos;s storage.</p>
        {LAYOUT_STORES.map((id) => <StoreRow key={id} id={id} />)}
      </Group>
    </>
  );
}

// StoreRow shows whether a webview store is kept, and its size.
export function StoreRow({ id }: { id: string }) {
  return <SettingField id={id} source="this window" value={<StoredState id={id} />} />;
}

function StoredState({ id }: { id: string }) {
  const key = id.replace(/^store\./, "");
  if (!isStorageKey(key)) return <>not a store</>;
  const v = readStored(key);
  return <>{v === null ? "nothing kept" : `kept, ${bytes(new Blob([v]).size)}`}</>;
}
