import { useFleet } from "../store/fleet";
import { btn } from "../chrome/primitives";
import { wellKnownResourceRef } from "./resourceRefs";

// ManifestButton is the shared "one manifest surface" affordance: every lens
// panel links its selected object to the SINGLE guarded editor in Resources
// (InstanceDetail's YAML tab - dry-run, Force:false conflict surfacing,
// authoritative GitOps-ownership warning, prd arming) instead of growing its own
// apply path. Renders nothing when the kind is not in the ref table - an honest
// degrade, never a dead button.
export function ManifestButton({ group, kind, namespace, name }: { group: string; kind: string; namespace: string; name: string }) {
  const ref = wellKnownResourceRef(group, kind);
  if (!ref) return null;
  return (
    <button
      onClick={() => {
        const nav = useFleet.getState();
        nav.openResource(ref);
        nav.openInstance(ref.scope === "Namespaced" ? namespace : "", name);
      }}
      style={btn}
      title={`Open the ${kind} manifest (YAML, edit) in Resources`}
    >manifest</button>
  );
}
