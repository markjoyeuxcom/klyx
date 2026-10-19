import { describe, it, expect, vi, beforeEach } from "vitest";
import { useLayoutEffect } from "react";
import { render, renderHook, fireEvent, screen, within, waitFor } from "@testing-library/react";
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
const { AdmissionView, paramRefText, LIMITS, useParamCheck } = await import("./AdmissionView");

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
const hook = (name: string, over: Partial<AdmissionWebhookDTO> = {}): AdmissionWebhookDTO => ({ name, failurePolicy: "Fail", failClosed: true, timeoutSeconds: 10,
  serviceNamespace: "sys", serviceName: "hook", servicePort: 443, caBundle: true, rules: [{ operations: ["CREATE"], groups: ["lab.klyx.dev"], versions: ["v1"], resources: ["widgets"] }],
  ruleCount: 1, backend: { state: "ready", label: "Backend ready", reason: "1 usable endpoint", endpointRouting: 1, clusterRouting: 1, terminating: 0 }, role: "ok", ...over });
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

  // ---- Codex's S3a implementation review ------------------------------------

  it("review: a same-UID generation change re-reads the params, and the old reply never shows", async () => {
    let calls = 0;
    paramsReply = async (c, u) => { calls++; return { cluster: c, policyUid: u, state: "read", checks: [{ binding: "b", state: "not-found", text: "OLD-DENIAL" }] }; };
    const p = policy("p", "ok", "Bound", { generation: 1, paramKind: { apiVersion: "example.com/v1", kind: "Limits", state: "served" } });
    policies = part([p], ["ValidatingAdmissionPolicy"]);
    const v = render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Policies/));
    fireEvent.click(screen.getByText("p"));
    await waitFor(() => expect(screen.getByTestId("admission-params").textContent).toContain("OLD-DENIAL"));
    paramsReply = async (c, u) => { calls++; return { cluster: c, policyUid: u, state: "read", checks: [{ binding: "b", state: "found", text: "NEW-RESULT" }] }; };
    policies = part([{ ...p, generation: 2, failClosed: false, paramKind: { apiVersion: "example.com/v1", kind: "Other", state: "served" } }], ["ValidatingAdmissionPolicy"]);
    v.rerender(<AdmissionView cluster="c" />);
    await waitFor(() => expect(calls).toBe(2));
    await waitFor(() => expect(screen.getByTestId("admission-params").textContent).toContain("NEW-RESULT"));
    expect(screen.getByTestId("admission-params").textContent).not.toContain("OLD-DENIAL");
  });

  it("review: a reply read for another revision is dropped", async () => {
    paramsReply = async (c, u) => ({ cluster: c, policyUid: u, revision: "1|served/true/limits", state: "read", checks: [{ binding: "b", state: "found", text: "STALE" }] });
    policies = part([policy("p", "ok", "Bound", { generation: 2, paramRevision: "2|served/true/limits", paramKind: { apiVersion: "example.com/v1", kind: "Limits", state: "served" } })], ["ValidatingAdmissionPolicy"]);
    render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Policies/));
    fireEvent.click(screen.getByText("p"));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByTestId("admission-params").textContent).not.toContain("STALE");
  });

  it("review: unread binding history never says no effect", () => {
    policies = part([policy("p", "unknown", "Bindings unverified", { bindings: [], bindingsRead: "unread" })], ["ValidatingAdmissionPolicy"]);
    render(<AdmissionView cluster="c" />);
    fireEvent.click(tab(/^Policies/));
    fireEvent.click(screen.getByText("p"));
    const panel = screen.getByTestId("admission-policy-panel");
    expect(panel.textContent).not.toContain("No binding names it: it has no effect");
    expect(within(panel).getByTestId("admission-no-bindings").textContent).toContain("is not known");
  });

  it("review r1: a completed param result is fenced during the first render of a new revision", async () => {
    const rev1 = "1|served/true/limits", rev2 = "2|served/true/others";
    paramsReply = async (c, u) => ({ cluster: c, policyUid: u, revision: rev1, state: "read", checks: [{ binding: "b", state: "not-found", text: "OLD-DENIAL" }] });
    const p1 = policy("p", "ok", "Bound", { generation: 1, paramRevision: rev1, paramKind: { apiVersion: "example.com/v1", kind: "Limits", state: "served" } });
    const commits: { generation: number; text: string }[] = [];
    const h = renderHook(({ p }: { p: AdmissionPolicyDTO }) => {
      const r = useParamCheck("c", p, true);
      useLayoutEffect(() => { commits.push({ generation: p.generation, text: r?.checks[0]?.text ?? "" }); });
      return r;
    }, { initialProps: { p: p1 } });
    await waitFor(() => expect(h.result.current?.checks[0]?.text).toBe("OLD-DENIAL"));
    paramsReply = () => new Promise(() => {});
    h.rerender({ p: { ...p1, generation: 2, paramRevision: rev2, paramKind: { apiVersion: "example.com/v1", kind: "Other", state: "served" } } });
    expect(commits.filter((c) => c.generation === 2).map((c) => c.text)).not.toContain("OLD-DENIAL");
    // and another policy's first commit never shows this one's result
    h.rerender({ p: { ...p1, uid: "uid-other", name: "other" } });
    expect(h.result.current).toBeNull();
  });
});
