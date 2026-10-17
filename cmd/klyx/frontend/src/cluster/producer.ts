import type { SourceProducerDTO } from "../bridge/fluxcomposition";

// An ExternalArtifact's producer in words (coverage S1c §2): who the
// evidence says it is, and whether its health may be shown. Only a
// verified, identity-found or named generator carries a health; the
// producer never stands in for the artifact's own verdict.

const ref = (p: { kind?: string; namespace?: string; name?: string }) => `${p.kind ?? "ArtifactGenerator"} ${p.namespace ?? ""}/${p.name ?? ""}`;

// attributed: the producer's health may be shown.
export const attributed = (p: SourceProducerDTO) => p.state === "verified" || p.state === "by identity" || p.state === "named";

export function producerText(p: SourceProducerDTO): string {
  switch (p.state) {
    case "verified":
      return `produced by ${ref(p)}`;
    case "by identity":
      return `produced by ${ref(p)}, found by the identity the artifact records`;
    case "named":
      return `named by the artifact: ${ref(p)}; its identity is not recorded`;
    case "declared":
      return `produced by ${ref(p)}; Klyx does not read that kind's health`;
    case "disagree": {
      const recorded = p.recordedName ? `; the recorded producer is ${p.recordedNamespace}/${p.recordedName}`
        : p.recordedGone ? "; the recorded producer no longer exists"
        : p.recordedLookup ? `; the recorded producer is ${p.recordedLookup === "incomplete" ? "not in the retained read (capped)" : "not readable"}` : "";
      return `the producer reference and the recorded identity disagree: ${p.namespace}/${p.name} is not the generator this artifact records${recorded}`;
    }
    case "gone":
      return "the recorded producer no longer exists";
    case "not found":
      return `${ref(p)} is not found`;
    case "incomplete":
      return "the generators' read is capped, so the producer is not known";
    case "not readable":
      return "the generators could not be read, so the producer is not known";
    case "none":
      return "no producer recorded";
  }
  return `producer: ${p.state}`;
}
