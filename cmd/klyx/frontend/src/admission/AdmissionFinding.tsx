import { useState } from "react";
import { reveal } from "../nav/reveal";
import { CANDIDATES_LABEL, candidateName, type AdmissionCandidateDTO, type AdmissionFindingDTO } from "./finding";

// Coverage S3b §3, §4: a refused request's finding - what refused it, the
// server's message (or why it is withheld), where the named webhook or
// policy is in Klyx's inventory - and the candidates, labelled always.

const RESOURCE: Record<string, string> = {
  ValidatingWebhook: "validatingwebhookconfigurations", MutatingWebhook: "mutatingwebhookconfigurations",
  ValidatingAdmissionPolicy: "validatingadmissionpolicies", MutatingAdmissionPolicy: "mutatingadmissionpolicies",
};
const SHOWN = 5;

function openCandidate(cluster: string, c: AdmissionCandidateDTO) {
  const webhook = c.kind.endsWith("Webhook");
  reveal(cluster, "admission", webhook ? "webhooks" : "policies", { gvr: `admissionregistration.k8s.io/v1/${RESOURCE[c.kind]}`, name: webhook ? c.config ?? "" : c.name });
}

function inventoryWords(f: AdmissionFindingDTO): string {
  if (f.inventory === "unread") return "Candidates are not established: Klyx has no current read of the cluster's admission configuration.";
  if (f.inventory === "stale") return `Candidates as of ${f.inventoryAsOf ? new Date(f.inventoryAsOf * 1000).toLocaleTimeString() : "the last read"}: the admission read is not current.`;
  return "";
}

export function AdmissionFinding({ cluster, f }: { cluster: string; f: AdmissionFindingDTO }) {
  const [all, setAll] = useState(false);
  const shown = all ? f.candidates : f.candidates.slice(0, SHOWN);
  const hidden = f.candidates.length - shown.length;
  return (
    <div data-testid="admission-finding" style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      <div className="t-body" data-testid="admission-summary" style={{ fontWeight: 500, overflowWrap: "anywhere" }}>{f.summary}</div>
      {f.message && <div className="t-secondary" data-testid="admission-message" style={{ overflowWrap: "anywhere", fontFamily: "var(--font-mono)" }}>{f.message}</div>}
      {f.withheld && <div className="t-secondary" data-testid="admission-withheld">{f.withheld}.</div>}
      <div className="t-secondary" data-testid="admission-correlation">{f.name}: {f.correlation}.</div>
      {f.exempt ? (
        <div className="t-secondary">This request is on an admission configuration kind, which no webhook or policy sees.</div>
      ) : (
        <>
          {inventoryWords(f) && <div className="t-secondary" data-testid="admission-inventory">{inventoryWords(f)}</div>}
          {f.inventory !== "unread" && (
            <div data-testid="admission-candidates">
              <div className="t-secondary">{CANDIDATES_LABEL}</div>
              {shown.map((c) => (
                <div key={`${c.kind}/${c.config ?? ""}/${c.name}`} className="t-body" data-testid="admission-candidate" style={{ overflowWrap: "anywhere" }}>
                  <button type="button" onClick={() => openCandidate(cluster, c)} title={`Open ${c.config ? `the configuration ${c.config}` : c.name} in Admission`}
                    style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit", fontFamily: "var(--font-mono)", textAlign: "left" }}>
                    {candidateName(c)}
                  </button>
                  <span className="t-secondary"> · {c.kind}{c.attributed ? " · named by the refusal" : ""}{c.mayMatch ? ` · ${c.mayMatch}` : ""}</span>
                </div>
              ))}
              {hidden > 0 && <button type="button" onClick={() => setAll(true)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>Show {hidden} more</button>}
              {(f.candidatesOmitted ?? 0) > 0 && <div className="t-secondary">and {f.candidatesOmitted} more not listed</div>}
              {f.candidates.length === 0 && <div className="t-secondary">No webhook or policy Klyx read has rules matching this request.</div>}
              {(f.aliases ?? 0) > 0 && (
                <div className="t-secondary" data-testid="admission-aliases">
                  {f.aliases} more webhook{f.aliases === 1 ? "" : "s"} or polic{f.aliases === 1 ? "y uses" : "ies use"} Equivalent matching on other resources, and could match through an alias the API server registers; Klyx cannot read that registry.
                </div>
              )}
              {f.maybeUnread && <div className="t-secondary">And possibly webhooks or policies Klyx did not read.</div>}
            </div>
          )}
        </>
      )}
    </div>
  );
}
