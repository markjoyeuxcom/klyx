import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within, waitFor } from "@testing-library/react";
import type { AdmissionPartRows } from "../health/useAdmissionRows";
import type { AdmissionConfigDTO, AdmissionParamsDTO, AdmissionPolicyDTO, AdmissionWebhookDTO } from "../bridge/admission";
import { useFleet } from "../store/fleet";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[]): AdmissionPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null });
let webhooks: AdmissionPartRows<AdmissionConfigDTO>;
let policies: AdmissionPartRows<AdmissionPolicyDTO>;
vi.mock("../health/useAdmissionRows", () => ({ useAdmissionWebhooks: () => webhooks, useAdmissionPolicies: () => policies }));
let paramsReply: (cluster: string, uid: string) => Promise<AdmissionParamsDTO>;
vi.mock("../bridge/admission", () => ({ checkAdmissionParams: (c: string, u: string) => paramsReply(c, u) }));
const { AdmissionView, paramRefText, LIMITS } = await import("./AdmissionView");

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
const hook = (name: string, over: Partial<AdmissionWebhookDTO> = {}): AdmissionWebhookDTO => ({ name, failurePolicy: "Fail", failClosed: true, timeoutSeconds: 10,
  serviceNamespace: "sys", serviceName: "hook", servicePort: 443, caBundle: true, rules: [{ operations: ["CREATE"], groups: ["lab.klyx.dev"], versions: ["v1"], resources: ["widgets"] }],
  ruleCount: 1, backend: { state: "ready", label: "Backend ready", reason: "1 usable endpoints", endpointRouting: 1, clusterRouting: 1, terminating: 0 }, role: "ok", ...over });
const config = (name: string, role: string, label: string, ws: AdmissionWebhookDTO[], over: Partial<AdmissionConfigDTO> = {}): AdmissionConfigDTO => ({
  uid: `uid-${name}`, gvr: "admissionregistration.k8s.io/v1/validatingwebhookconfigurations", name, kind: "Validating", generation: 1, webhookCount: ws.length,
  webhooks: ws, unavailable: 0, unverified: 0, rule: rule(role, label), ...over });
const policy = (name: string, role: string, label: string, over: Partial<AdmissionPolicyDTO> = {}): AdmissionPolicyDTO => ({ entity: "policy", uid: `uid-${name}`,
  gvr: "admissionregistration.k8s.io/v1/validatingadmissionpolicies", name, kind: "Validating", generation: 1, failClosed: true, matchRules: [], matchRuleCount: 0,
  warningFields: [], bindings: [], rule: rule(role, label), ...over });
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "Admission objects" })).getByRole("button", { name });
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("AdmissionView (coverage S3a)", () => {
  beforeEach(() => {
    webhooks = part([], ["ValidatingWebhookConfiguration"]);
    policies = part([], ["ValidatingAdmissionPolicy"]);
    paramsReply = async (c, u) => ({ cluster: c, policyUid: u, state: "read", checks: [] });
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", admissionPresent: true } as never] });
  });

  it("configurations are broken first; the inspector draws each webhook's backend chain and says the limits", () => {
    webhooks = part([
      config("healthy", "ok", "Backends ready", [hook("a.lab")]),
      config("broken", "fault", "Backend unavailable", [hook("a.lab"), hook("b.lab", { role: "fault", reason: "Service sys/late not found; matching requests may fail",
        backend: { state: "unavailable", label: "Backend unavailable", reason: "Service sys/late not found", endpointRouting: 0, clusterRouting: 0, terminating: 0 } })],
        { unavailable: 1, reason: "Backend unavailable: b.lab: Service sys/late not found; matching requests may fail" }),
    ], ["ValidatingWebhookConfiguration"]);
    render(<AdmissionView cluster="c" />);
    expect(rowTexts()[0]).toContain("broken");
    expect(rowTexts()[0]).toContain("1 unavailable");
    fireEvent.click(screen.getByText("broken"));
    const panel = screen.getByTestId("admission-config-panel");
    expect(within(panel).getAllByTestId("admission-chain").length).toBe(2);
    expect(within(panel).getByTestId("admission-limits").textContent).toContain(LIMITS[0]);
    expect(within(panel).getByTestId("admission-limits").textContent).toContain("routing mode");
    expect(panel.textContent).toContain("rules alone do not say which objects it blocks");
    expect(panel.textContent).toContain("matching requests may fail");
    expect(panel.textContent).not.toMatch(/blocks these objects/);
  });

  it("a policy's historical warnings never read as current, and its params are read on open", async () => {
    paramsReply = async (c, u) => ({ cluster: c, policyUid: u, state: "read", checks: [{ binding: "b1", state: "not-found", text: "not found: a request this binding matches is denied (no params found)" }] });
    policies = part([policy("p", "progressing", "Type checking pending", { behind: true, warningCount: 2, warningFields: ["spec.validations[0].expression"],
      paramKind: { apiVersion: "example.com/v1", kind: "Limits", state: "served", namespaced: true }, bindings: [{ uid: "b", name: "b1", policyName: "p", actions: ["Deny"], paramRef: { name: "x", namespace: "team", notFoundAction: "Deny" } }] })],
    ["ValidatingAdmissionPolicy"]);
    render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Policies/));
    fireEvent.click(screen.getByText("p"));
    const panel = screen.getByTestId("admission-policy-panel");
    expect(within(panel).getByTestId("admission-warnings").textContent).toContain("Historical");
    expect(within(panel).getByTestId("admission-warnings").textContent).toContain("text is not shown");
    await waitFor(() => expect(within(panel).getByTestId("admission-params").textContent).toContain("is denied (no params found)"));
  });

  it("a param reply for another policy or cluster is dropped", async () => {
    paramsReply = async () => ({ cluster: "other", policyUid: "uid-p", state: "read", checks: [{ binding: "b1", state: "found", text: "found" }] });
    policies = part([policy("p", "ok", "Bound", { paramKind: { apiVersion: "example.com/v1", kind: "Limits", state: "served" } })], ["ValidatingAdmissionPolicy"]);
    render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Policies/));
    fireEvent.click(screen.getByText("p"));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByTestId("admission-params").textContent).toContain("Reading the params");
  });

  it("bindings are facts: no verdict, and a dangling one says it has no effect", () => {
    policies = part([{ ...policy("dangling", "ok", "x"), entity: "binding", rule: undefined, gvr: "admissionregistration.k8s.io/v1/validatingadmissionpolicybindings",
      binding: { uid: "b", name: "dangling", policyName: "gone", policyState: "not found", actions: ["Deny"] } }], ["ValidatingAdmissionPolicy"]);
    render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Bindings/));
    expect(rowTexts()[0]).toContain("not found: no effect");
    fireEvent.click(screen.getByText("dangling"));
    expect(screen.getByTestId("admission-binding-panel").textContent).toContain("its policy's verdict covers it");
  });

  it("paramRefText words a reference without its selector", () => {
    expect(paramRefText({ uid: "", name: "b", policyName: "p", actions: [], paramRef: { namespace: "team", selector: true, notFoundAction: "Deny" } })).toBe("team/a selector · Deny if missing");
    expect(paramRefText({ uid: "", name: "b", policyName: "p", actions: [] })).toBe("none");
  });
});
