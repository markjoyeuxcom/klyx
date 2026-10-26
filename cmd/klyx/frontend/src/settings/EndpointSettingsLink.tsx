import { useFleet } from "../store/fleet";
import { anchorOf } from "./registry";

export type EndpointKind = "metrics" | "alertmanager" | "loki";

// mentionsObservability reports a backend reason that sends the person to
// Settings > Observability (a missing endpoint credential, a credential in
// the URL).
export function mentionsObservability(text: string | null | undefined): boolean {
  return !!text && text.includes("Settings > Observability");
}

export function openEndpointSettings(cluster: string, kind: EndpointKind): void {
  useFleet.getState().openSettings("observability", anchorOf(`clusters[].${kind}.endpoint`, cluster));
}

// EndpointSettingsLink follows such a reason with a link to the cluster's
// endpoint in Settings (S1 §1: endpoint credential prompts open
// Observability at the cluster's endpoint). Nothing for any other reason.
export function EndpointSettingsLink({ text, cluster, kind }: { text: string | null | undefined; cluster: string; kind: EndpointKind }) {
  if (!mentionsObservability(text)) return null;
  return (
    <>
      {" "}
      <button type="button" className="l-btn" data-endpoint-link={`${cluster}/${kind}`} onClick={() => openEndpointSettings(cluster, kind)}>
        Open Settings &gt; Observability
      </button>
    </>
  );
}
