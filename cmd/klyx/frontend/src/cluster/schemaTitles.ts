import { useEffect, useState } from "react";
import { getCRDDefinition } from "../bridge/crd";
import type { CRDDefinitionDTO, ResourceRef, SchemaNodeDTO } from "../store/fleet";
import type { TitleOf } from "../chrome/ObjectTree";

// Field titles for a custom resource's tree (The Line slice 15c): its CRD's
// schema for the instance's own requested version, keyed by segment path -
// so dots and brackets in names are never ambiguous (design review 1).

// The groups the kube-apiserver serves itself: no CRD, no schema to read.
export const BUILTIN_GROUPS = new Set([
  "", "apps", "batch", "autoscaling", "policy", "networking.k8s.io", "rbac.authorization.k8s.io", "storage.k8s.io",
  "scheduling.k8s.io", "coordination.k8s.io", "node.k8s.io", "discovery.k8s.io", "certificates.k8s.io",
  "admissionregistration.k8s.io", "apiextensions.k8s.io", "apiregistration.k8s.io", "flowcontrol.apiserver.k8s.io",
  "events.k8s.io", "resource.k8s.io", "storagemigration.k8s.io", "internal.apiserver.k8s.io",
]);

// schemaTitle: one field's hover text - its type, what it carries and what
// the tree does not draw, with truncations said.
export function schemaTitle(n: SchemaNodeDTO): string {
  const type = [n.type || (n.intOrString ? "int-or-string" : ""), n.format].filter(Boolean).join(", ");
  const bits = [
    type,
    n.required ? "required" : "",
    n.intOrString && n.type ? "int-or-string" : "",
    n.preserveUnknown ? "keeps unknown fields" : "",
    n.embeddedResource ? "an embedded resource" : "",
    n.enum?.length ? `one of ${n.enum.join(", ")}${n.enumMore ? ` and ${n.enumMore} more` : ""}` : "",
    n.default !== undefined && n.default !== "" ? `default ${n.default}${n.defaultTruncated ? "…" : ""}` : "",
    n.constraints?.length ? `constraints not shown: ${n.constraints.join(", ")}` : "",
    n.deeper ? "nested deeper than the schema view" : "",
  ].filter(Boolean);
  const head = bits.join(" · ");
  const desc = n.description ? `${n.description}${n.descriptionTruncated ? "…" : ""}` : "";
  return [head, desc].filter(Boolean).join("\n");
}

export function titlesFrom(def: CRDDefinitionDTO): TitleOf {
  const m = new Map<string, string>();
  for (const n of def.schema ?? []) m.set(JSON.stringify(n.path), schemaTitle(n));
  return (path) => m.get(JSON.stringify(path));
}

const cache = new Map<string, TitleOf | null>();

export function useSchemaTitles(cluster: string, resource: ResourceRef): TitleOf | undefined {
  const custom = !BUILTIN_GROUPS.has(resource.group);
  const key = `${cluster}\u0000${resource.plural}.${resource.group}\u0000${resource.version}`;
  const [titles, setTitles] = useState<TitleOf | null>(() => cache.get(key) ?? null);
  useEffect(() => {
    if (!custom) return;
    if (cache.has(key)) { setTitles(cache.get(key) ?? null); return; }
    let live = true;
    void getCRDDefinition(cluster, `${resource.plural}.${resource.group}`, resource.version).then((def) => {
      const t = def && !def.error && def.schemaVersion === resource.version ? titlesFrom(def) : null;
      if (!def?.error) cache.set(key, t);
      if (live) setTitles(t);
    });
    return () => { live = false; };
  }, [key, custom, cluster, resource.plural, resource.group, resource.version]);
  return custom && titles ? titles : undefined;
}
