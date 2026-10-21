import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { failureStatus, successStatus, validActivityAdmission, type AdmissionFindingDTO } from "./finding";
import { AdmissionFinding } from "./AdmissionFinding";
import { useFleet } from "../store/fleet";

// Coverage S3b §4, §8: the toast's line, a composite action's steps, the
// Activity log's entry, the finding's block, and no "not drift" wording.

const finding = (over: Partial<AdmissionFindingDTO> = {}): AdmissionFindingDTO => ({
  envelope: "webhook-denied", name: "deny.lab.klyx.dev", code: 403, request: { operation: "UPDATE", kind: "Deployment", namespace: "lab", name: "api" },
  what: 'denied by admission webhook "deny.lab.klyx.dev"', summary: 'update Deployment lab/api was refused by admission before it was stored: denied by admission webhook "deny.lab.klyx.dev"',
  message: "widgets labelled deny are refused", nothingWritten: true, correlation: "a webhook named deny.lab.klyx.dev in lab-a",
  candidates: [{ kind: "ValidatingWebhook", config: "lab-a", name: "deny.lab.klyx.dev", attributed: true }, { kind: "ValidatingAdmissionPolicy", name: "deny-labelled" }],
  inventory: "live", ...over,
});

describe("the toast and the Activity entry", () => {
  it("an attributed refusal says what refused it, and the entry keeps its line and candidates, never its message", () => {
    const s = failureStatus({ ok: false, error: "scale Deployment lab/api: ... - nothing was written", admission: finding() }, "Scale failed");
    expect(s.message).toBe('Scale refused: denied by admission webhook "deny.lab.klyx.dev"');
    expect(s.admission).toEqual([{ summary: finding().summary, candidates: ["lab-a/deny.lab.klyx.dev", "deny-labelled"] }]);
    expect(JSON.stringify(s)).not.toContain("widgets labelled deny");
    expect(failureStatus({ ok: false, error: "boom" }, "Scale failed", "c1: ")).toEqual({ kind: "error", message: "c1: boom" });
  });
  it("a composite action says each step, the refused source beside the written target", () => {
    const steps = [{ verb: "reconcile", subject: "GitRepository flux-system/flux-system", written: false, error: "x", admission: finding({ what: 'denied by admission webhook "src"' }) },
      { verb: "reconcile", subject: "Kustomization flux-system/app", written: true }];
    const s = successStatus({ ok: true, error: "", steps }, "Reconcile (with source) requested for flux-system/app");
    expect(s.kind).toBe("info");
    expect(s.message).toBe('Reconcile (with source) requested for flux-system/app; not every step was written (GitRepository flux-system/flux-system: refused - denied by admission webhook "src"; Kustomization flux-system/app: written)');
    expect(s.admission?.length).toBe(1);
    expect(successStatus({ ok: true, error: "" }, "ok")).toEqual({ kind: "success", message: "ok" });
  });
  it("both steps refused: the Activity entry keeps each request's finding once (Codex's implementation review, finding 4)", () => {
    const src = finding({ what: 'denied by admission webhook "src"', summary: "update GitRepository config-source/source was refused" });
    const tgt = finding({ summary: "update Kustomization config-target/target was refused" });
    const steps = [{ verb: "reconcile", subject: "GitRepository config-source/source", written: false, refused: true, error: "x", admission: src },
      { verb: "reconcile", subject: "Kustomization config-target/target", written: false, refused: true, error: "y", admission: tgt }];
    const s = failureStatus({ ok: false, error: "reconcile ...", admission: tgt, steps }, "Reconcile failed");
    expect(s.admission?.map((a) => a.summary)).toEqual([src.summary, tgt.summary]);
  });
  it("a lost answer is unverified, never 'not written' (finding 5)", () => {
    const steps = [{ verb: "reconcile", subject: "GitRepository flux-system/flux-system", written: false, error: "context deadline exceeded" },
      { verb: "reconcile", subject: "Kustomization flux-system/app", written: true }];
    const s = successStatus({ ok: true, error: "", steps }, "Reconcile (with source) requested for flux-system/app");
    expect(s.message).toBe("Reconcile (with source) requested for flux-system/app; not every step was confirmed (GitRepository flux-system/flux-system: outcome unverified - context deadline exceeded; Kustomization flux-system/app: written)");
    const refused = successStatus({ ok: true, error: "", steps: [{ ...steps[0], refused: true, error: "forbidden" }, steps[1]] }, "ok");
    expect(refused.message).toBe("ok; not every step was written (GitRepository flux-system/flux-system: not written - forbidden; Kustomization flux-system/app: written)");
  });
  it("stored entries: older ones and malformed findings read as before", () => {
    expect(validActivityAdmission(undefined)).toBeUndefined();
    expect(validActivityAdmission([{ summary: 1 }])).toBeUndefined();
    expect(validActivityAdmission([{ summary: "s", candidates: ["a"] }])).toEqual([{ summary: "s", candidates: ["a"] }]);
  });
});

describe("AdmissionFinding", () => {
  beforeEach(() => useFleet.setState({ navIntent: null }));
  it("shows the finding, the labelled candidates (five, then the rest on demand), the alias line, and opens a candidate's row", () => {
    const many = Array.from({ length: 7 }, (_, i) => ({ kind: "ValidatingWebhook", config: `c${i}`, name: `w${i}` }));
    render(<AdmissionFinding cluster="k" f={finding({ candidates: many, aliases: 2, maybeUnread: true })} />);
    expect(screen.getByTestId("admission-summary").textContent).toContain("was refused by admission before it was stored");
    expect(screen.getAllByTestId("admission-candidate")).toHaveLength(5);
    fireEvent.click(screen.getByText("Show 2 more"));
    expect(screen.getAllByTestId("admission-candidate")).toHaveLength(7);
    expect(screen.getByTestId("admission-aliases").textContent).toContain("2 more webhooks or policies use Equivalent matching");
    expect(screen.getByTestId("admission-candidates").textContent).toContain("possibly webhooks or policies Klyx did not read");
    fireEvent.click(screen.getByText("c0/w0"));
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "reveal", task: "admission", part: "webhooks", ref: { gvr: "admissionregistration.k8s.io/v1/validatingwebhookconfigurations", name: "c0" } });
  });
  it("a protected subject's message is withheld; an unread inventory establishes no candidate; a stale one is dated", () => {
    const { rerender } = render(<AdmissionFinding cluster="k" f={finding({ message: undefined, withheld: "the server's message is withheld for a Secret: it can echo submitted values" })} />);
    expect(screen.queryByTestId("admission-message")).toBeNull();
    expect(screen.getByTestId("admission-withheld").textContent).toContain("withheld for a Secret");
    rerender(<AdmissionFinding cluster="k" f={finding({ inventory: "unread", candidates: [] })} />);
    expect(screen.queryByTestId("admission-candidates")).toBeNull();
    expect(screen.getByTestId("admission-inventory").textContent).toContain("not established");
    rerender(<AdmissionFinding cluster="k" f={finding({ inventory: "stale", inventoryAsOf: 1760000000 })} />);
    expect(screen.getByTestId("admission-inventory").textContent).toMatch(/^Candidates as of /);
  });
});

describe("drift wording", () => {
  it("no surface calls a difference 'not drift' (a source scan)", () => {
    const sources = import.meta.glob<string>(["../**/*.{ts,tsx}", "!../**/*.test.{ts,tsx}"], { query: "?raw", import: "default", eager: true });
    const offenders = Object.entries(sources).filter(([, text]) => /\bnot drift\b|\bisn't drift\b/i.test(text)).map(([p]) => p);
    expect(Object.keys(sources).length).toBeGreaterThan(100);
    expect(offenders).toEqual([]);
  });
});
