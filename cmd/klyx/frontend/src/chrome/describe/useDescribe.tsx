import { useEffect, useState, type ReactNode } from "react";
import type { MenuItem } from "../../layout/InspectorLayout";
import { DescribePane } from "../DescribePane";
import { describeArg } from "./targets";

export { describeArg };

// A lens object: its kind, where it is, and the UID the inspector shows -
// the sheet says so when kubectl's report names another object.
export type DescribeTarget = { kind: string; namespace: string; name: string; uid?: string };

// useDescribe gives an inspector its "Describe" More item and the sheet. Pass
// null for an object Describe must not read: a removed or unverified one
// (Describe reads the live object by name), or a Helm release, which is not
// a Kubernetes object.
export function useDescribe(cluster: string, target: DescribeTarget | null): { menu: MenuItem[]; sheet: ReactNode } {
  // The sheet is open for one object: another object never inherits it, not
  // even for a render (it would start a run of its own).
  const [openFor, setOpenFor] = useState<string | null>(null);
  const arg = target ? describeArg(target.kind) : null;
  const key = target ? [cluster, target.kind, target.namespace, target.name, target.uid ?? ""].join("\u0000") : "";
  // Another object closes it for good: coming back never reopens it.
  useEffect(() => {
    if (openFor !== null && openFor !== key) setOpenFor(null);
  }, [key, openFor]);
  if (!target || !arg) return { menu: [], sheet: null };
  return {
    menu: [{ label: "Describe", hint: "kubectl describe, read-only", onSelect: () => setOpenFor(key) }],
    sheet: openFor === key ? (
      <DescribePane cluster={cluster} kindArg={arg} kind={target.kind} namespace={target.namespace} name={target.name} expectUid={target.uid || undefined} onClose={() => setOpenFor(null)} />
    ) : null,
  };
}
