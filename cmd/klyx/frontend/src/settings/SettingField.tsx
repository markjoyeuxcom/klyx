import type { ReactNode } from "react";
import { anchorOf, settingDomId, settingEntry, type SettingEntry } from "./registry";

// SettingField renders one setting, whichever control it has (Settings S1
// §4): its label, associated with the control; its effect, always visible
// and wired by aria-describedby; a source chip for the effective value; a
// takes-effect chip when it is not immediate; the default and validation
// in a disclosure; or, for a file-only or fixed entry, a read-only row with
// its effective value and where to change it. The text is the registry's.

export type ValueSource = "file" | "default" | "inferred" | "discovered" | "environment" | "this window" | "running";

export type ControlIds = { id: string; describedBy: string };

type Props = {
  id: string;
  // the instance key of a repeated row (a cluster's name, a repository key)
  instance?: string;
  // a visible label for this instance or variant, when the registry's is
  // not specific enough ("prd metrics token", "Server URL")
  label?: string;
  source?: ValueSource;
  // a group of controls (radios) is a fieldset with the label as legend
  group?: boolean;
  // the control; a read-only row has none and shows value instead
  children?: (ids: ControlIds) => ReactNode;
  value?: ReactNode;
  // extra text under the effect (a state, a warning)
  note?: ReactNode;
};

const TAKES_EFFECT: Record<string, string> = {
  "on Save": "on Save",
  "at reconnect": "at reconnect",
  "at restart": "at restart",
  "on Test or a credential action": "on Test or a credential action",
};

function whereToChange(e: SettingEntry): string {
  if (e.disposition === "fixed") return "Fixed: listed, never offered.";
  if (e.disposition === "deferred") return `Not editable yet. ${e.save}`;
  return e.save;
}

export function SettingField({ id, instance, label, source, group, children, value, note }: Props) {
  const e = settingEntry(id);
  if (!e) {
    // an unregistered id is a bug the coverage tests catch; render nothing
    // that could pass as described
    return <div data-setting-unregistered={id} />;
  }
  const dom = settingDomId(id, instance);
  const effectId = `${dom}-effect`;
  const labelId = `${dom}-label`;
  const name = label ?? e.label;
  const readOnly = !children;
  const chips = (
    <span className="setting-chips">
      {source && <span className="setting-chip" title="where the value in use comes from">{source}</span>}
      {TAKES_EFFECT[e.takesEffect] && <span className="setting-chip" title="when a change takes effect">{TAKES_EFFECT[e.takesEffect]}</span>}
      {readOnly && (e.disposition === "fixed" || e.disposition === "deferred" || e.disposition === "effective-file-only") && (
        <span className="setting-chip">{e.disposition === "fixed" ? "fixed" : e.disposition === "deferred" ? "not yet editable" : "file only"}</span>
      )}
    </span>
  );
  const body = (
    <div className="setting-body">
      {readOnly ? (
        <div className="setting-control">
          <span className="setting-readonly-value" aria-labelledby={labelId}>{value ?? "—"}</span>
        </div>
      ) : (
        <div className="setting-control">{children({ id: dom, describedBy: effectId })}</div>
      )}
      <p id={effectId} className="setting-effect">{e.effect}{readOnly ? <> <span className="setting-where">{whereToChange(e)}</span></> : null}</p>
      {note && <div className="setting-note">{note}</div>}
      <details className="setting-more">
        <summary>Default and allowed values</summary>
        <dl>
          <dt>Default</dt><dd>{e.default}</dd>
          <dt>Allowed</dt><dd>{e.validation}</dd>
          {e.test && <><dt>Test</dt><dd>{e.test}</dd></>}
          {!readOnly && <><dt>Saved</dt><dd>{e.save}</dd></>}
        </dl>
      </details>
    </div>
  );
  const common = {
    className: "setting-field",
    "data-setting": id,
    "data-instance": instance,
    "data-anchor": anchorOf(id, instance),
    id: `${dom}-row`,
  };
  if (group && !readOnly) {
    return (
      // the legend must be the fieldset's first child to name the group; the
      // visible label beside the chips repeats it for sight only
      <fieldset {...common} className="setting-fieldset" aria-describedby={effectId}>
        <legend className="sr-only">{name}</legend>
        <div className="setting-field">
          <div className="setting-head"><span className="setting-label" aria-hidden="true">{name}</span>{chips}</div>
          {body}
        </div>
      </fieldset>
    );
  }
  return (
    <div {...common} tabIndex={readOnly ? -1 : undefined} aria-labelledby={readOnly ? labelId : undefined} role={readOnly ? "group" : undefined}>
      <div className="setting-head">
        {readOnly
          ? <span id={labelId} className="setting-label">{name}</span>
          : <label id={labelId} htmlFor={dom} className="setting-label">{name}</label>}
        {chips}
      </div>
      {body}
    </div>
  );
}
