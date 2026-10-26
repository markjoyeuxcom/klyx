import { useEffect, useRef, useState } from "react";
import type { CancellablePromise } from "@wailsio/runtime";
import { checkVersions, getDiagnostics, type Diagnostics, type ToolVersion } from "../../bridge/settings";
import { SettingField } from "../SettingField";
import { ENTRIES } from "../registry";
import { Group, PathRow, bytes, plural, type SectionProps } from "./common";

// Settings > Diagnostics (S1 §5), read-only: the fleet file's state, each
// tool as the resolver finds it without running it, the launch environment
// by name, and the fixed bounds. "Check versions" runs the tools' client-only
// version commands, contained, only when asked.

const SOURCE_WORDS: Record<string, string> = {
  override: "the override, chosen",
  path: "found on PATH",
  fallback: "a platform or package-manager location",
  "not found": "not found on PATH or in the usual locations",
};

const TOOL_ENV = new Set(["env.KLYX_GIT_PATH", "env.KLYX_KUBECTL_PATH", "env.KLYX_HELM_PATH", "env.KLYX_FLUX_PATH"]);

export function diagnosticsSentence(d: Diagnostics): string {
  const f = d.fleet;
  const where = f.fromEnv ? "set by KLYX_CONFIG" : "the default location";
  if (f.state === "startup-error") return `fleet.yaml did not load when Klyx started (${where})`;
  if (f.state === "not-loading-now") return `fleet.yaml does not load as it is now; Klyx runs what it read at startup (${where})`;
  const differs = f.drifted > 0 || f.fileOnly.length > 0 ? `; ${[f.drifted > 0 ? plural(f.drifted, "cluster") + " differ" : null, f.fileOnly.length > 0 ? `${f.fileOnly.length} not running` : null].filter(Boolean).join(", ")}` : "";
  return `fleet.yaml loaded (${where})${differs}`;
}

function boundValue(value: number, unit: string): string {
  if (unit === "bytes") return bytes(value);
  return `${value.toLocaleString("en-GB")} ${unit}`;
}

export function DiagnosticsSection({ say, ready }: SectionProps) {
  const [d, setD] = useState<Diagnostics | null | undefined>(undefined);
  const [versions, setVersions] = useState<ToolVersion[] | null>(null);
  const [checking, setChecking] = useState(false);
  const running = useRef<CancellablePromise<ToolVersion[]> | null>(null);
  const load = () => void getDiagnostics().then((v) => { setD(v); if (v) say(diagnosticsSentence(v)); ready(); });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, []);
  useEffect(() => () => { running.current?.cancel(); }, []);

  const check = async () => {
    setChecking(true);
    const p = checkVersions();
    running.current = p;
    try {
      setVersions(await p);
    } catch {
      setVersions(null);
    } finally {
      if (running.current === p) running.current = null;
      setChecking(false);
    }
  };

  if (d === undefined) return <p className="settings-group-note">Reading Diagnostics…</p>;
  if (d === null) {
    return (
      <p className="settings-group-note" role="alert">
        Diagnostics could not be read. <button type="button" className="l-btn" onClick={load}>Retry</button>
      </p>
    );
  }
  const set = new Set(d.envSet);
  const envEntries = ENTRIES.filter((e) => e.id.startsWith("env.") && !e.id.startsWith("env.family.") && !e.id.startsWith("env.purpose."));
  const product = envEntries.filter((e) => e.disposition !== "fixed" && !TOOL_ENV.has(e.id) && e.id !== "env.KLYX_CONFIG");
  const harness = envEntries.filter((e) => e.disposition === "fixed");
  const families = ENTRIES.filter((e) => e.id.startsWith("env.family."));
  const bounds = ENTRIES.filter((e) => e.id.startsWith("bound."));
  const byBound = new Map(d.bounds.map((b) => [`bound.${b.id}`, b]));
  const f = d.fleet;
  return (
    <>
      <Group title="The fleet file">
        <PathRow label="file" path={f.path} />
        <div role="status" className="settings-group-note" style={{ color: f.state === "loaded" ? undefined : "var(--color-text-warning)" }}>
          {f.state === "loaded" ? "Loaded when Klyx started." : f.state === "startup-error" ? "It did not load when Klyx started, so Klyx connected no cluster: " : "It does not load as it is now, so Klyx keeps running what it read at startup: "}
          {f.loadError || f.fileError ? <span style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>{f.loadError || f.fileError}</span> : null}
          {f.drifted > 0 && ` ${plural(f.drifted, "running cluster")} differ from the file; Fleet names them.`}
          {f.fileOnly.length > 0 && ` In the file but not running: ${f.fileOnly.join(", ")}.`}
        </div>
        {f.warnings.map((w, i) => <div key={i} className="settings-group-note" style={{ color: "var(--color-text-warning)" }}>▲ {w}</div>)}
        <SettingField id="env.KLYX_CONFIG" source={f.fromEnv ? "environment" : "default"} value={f.fromEnv ? "set: this is where the file is read" : "not set: ~/.config/klyx/fleet.yaml"} />
      </Group>

      <Group title="Tools">
        <p className="settings-group-note">Where Klyx finds each tool, without running it. A missing override falls back to PATH or a usual location, and says so.</p>
        <div role="table" aria-label="Tools" className="diag-tools">
          <div role="row" className="diag-tools-row diag-tools-head">
            <span role="columnheader">tool</span><span role="columnheader">path</span><span role="columnheader">found by</span><span role="columnheader">version</span>
          </div>
          {d.tools.map((t) => {
            const v = versions?.find((x) => x.name === t.name);
            return (
              <div role="row" key={t.name} className="diag-tools-row" data-tool={t.name}>
                <span role="cell" className="diag-tool-name">{t.name}</span>
                <span role="cell" className="diag-mono">{t.path ?? "—"}</span>
                <span role="cell">
                  {SOURCE_WORDS[t.source] ?? t.source}
                  {t.overrideMissing && <span className="diag-warn"> · {t.override} is set to {t.overrideMissing}, which is not an executable</span>}
                </span>
                <span role="cell" className="diag-mono">
                  {v ? (v.version ? `${v.version}${v.reused ? " (the Git service's)" : ""}` : <span className="diag-warn">{v.failure}{v.exit !== undefined ? ` (status ${v.exit})` : ""}</span>) : "—"}
                </span>
              </div>
            );
          })}
        </div>
        <div className="diag-check">
          {d.canCheck ? (
            <>
              <button type="button" className="l-btn" disabled={checking} onClick={() => void check()}>{checking ? "Checking…" : "Check versions"}</button>
              {checking && <button type="button" className="l-btn" onClick={() => running.current?.cancel()}>Cancel</button>}
              <span className="settings-group-note" style={{ margin: 0 }}>
                Runs these programs on this machine: kubectl version --client, helm version --short and flux version --client, each with an empty scratch kubeconfig, so none reads a cluster. git is not run: its version is the one the Git service checked. Nothing in Klyx&apos;s configuration or state changes; what an installed wrapper does beyond these arguments is the wrapper&apos;s.
              </span>
            </>
          ) : <span className="settings-group-note" role="status">{d.cannotCheck}</span>}
        </div>
        {[...TOOL_ENV].map((id) => {
          const key = id.replace(/^env\./, "");
          return <SettingField key={id} id={id} source="environment" value={set.has(key) ? "set" : "not set"} />;
        })}
      </Group>

      <Group title="The launch environment">
        <p className="settings-group-note">
          {d.proxies.length > 0 ? `Proxy variables set: ${d.proxies.join(", ")}. ` : "No proxy variable is set. "}
          The AI clients honour them; the observability transports deliberately do not. A GUI launch may not see variables exported in your shell, which matters for an AI key read from the environment. Klyx shows names, never values.
        </p>
        {product.map((e) => <SettingField key={e.id} id={e.id} source="environment" value={set.has(e.id.replace(/^env\./, "")) ? "set" : "not set"} />)}
        {families.map((e) => <SettingField key={e.id} id={e.id} value="fixed by Klyx" />)}
        <details className="settings-fields">
          <summary>Build harness variables ({harness.length}), not product settings</summary>
          {harness.map((e) => <SettingField key={e.id} id={e.id} value="read only by its harness" />)}
        </details>
      </Group>

      <Group title="Fixed bounds">
        <p className="settings-group-note">Safety ceilings Klyx keeps: listed, never raised. They are not the configured limits of Data and storage.</p>
        {bounds.map((e) => {
          const b = byBound.get(e.id);
          return <SettingField key={e.id} id={e.id} value={b ? boundValue(b.value, b.unit) : e.default} />;
        })}
      </Group>
    </>
  );
}
