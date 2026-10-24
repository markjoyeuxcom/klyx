import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, within } from "@testing-library/react";

const bridge = vi.hoisted(() => ({
  reviewStep: vi.fn(),
  runStep: vi.fn(),
  dismissPlan: vi.fn(),
}));
vi.mock("../bridge/chat", async (orig) => ({ ...(await orig<typeof import("../bridge/chat")>()), ...bridge }));
const cfg = vi.hoisted(() => ({ armProtected: vi.fn() }));
vi.mock("../bridge/configsvc", () => cfg);

import { ActionCard } from "./ActionCard";
import { useFleet } from "../store/fleet";
import type { ActionPlan, ActionStep, StepReview } from "../bridge/chat";

// AI A3 stage 5: the card shows Klyx's decision on each step, offers only
// the next one, and runs it only through Review, the ConfirmDialog and the
// backend's RunStep.

const target = (kind: string, ns: string, name: string, uid: string) => ({ kind, namespace: ns, name, uid, identity: "uid" as const });

function step(over: Partial<ActionStep> = {}): ActionStep {
  return { n: 1, verb: "flux-suspend", target: target("Kustomization", "flux-system", "a1-web", "uid-ks"), why: "the unit keeps reapplying the bad image", cites: ["r3"], state: "offered", danger: true, factsRead: "r3", factsAt: "2026-10-14T12:00:00Z", gvr: "kustomize.toolkit.fluxcd.io/v1/kustomizations", ...over };
}

const twoSteps = (): ActionPlan => ({
  steps: [step(), step({ n: 2, verb: "rollout-undo", target: target("Deployment", "shop", "web", "uid-web"), revision: 4, why: "revision 4 ran", cites: ["r4"], gvr: "apps/v1/deployments" })],
});

const reviewed: StepReview = {
  offered: true, token: "tok-1", expiresAt: "2026-10-14T12:02:00Z", n: 1, verb: "flux-suspend", target: target("Kustomization", "flux-system", "a1-web", "uid-ks"),
  change: "spec.suspend: false → true", consequence: ["Klyx found no GitOps unit managing it"], expected: "effect: spec.suspend true, within 30 s", danger: true,
  observed: [{ what: "Kustomization flux-system/a1-web at generation 2", at: "2026-10-14T12:00:01Z" }],
};

function card(plan: ActionPlan, over: Partial<Parameters<typeof ActionCard>[0]> = {}) {
  const onChanged = vi.fn();
  const openObject = vi.fn();
  const r = render(<ActionCard cluster="kind-klyx" conversationId="c1" exchangeId="e1" plan={plan} protected={false} onChanged={onChanged} openObject={openObject} {...over} />);
  return { ...r, onChanged, openObject };
}

beforeEach(() => {
  for (const f of [...Object.values(bridge), ...Object.values(cfg)]) f.mockReset();
  cfg.armProtected.mockResolvedValue({ ok: true, error: "" });
  useFleet.setState({ actionToasts: [], actionLog: [] });
});

describe("ActionCard", () => {
  it("says who proposed it and words each step from Klyx's facts", () => {
    const { getByTestId, getAllByTestId } = card(twoSteps());
    expect(getByTestId("act-header").textContent).toContain("Proposed by the model. Klyx checked that each step is eligible to run, not that it is the right remedy.");
    const heads = getAllByTestId("act-step-head").map((h) => h.textContent);
    expect(heads[0]).toContain("suspend");
    expect(heads[0]).toContain("Kustomization flux-system/a1-web");
    expect(heads[1]).toContain("to revision 4");
    const first = getAllByTestId("act-step")[0];
    expect(first.textContent).toContain("the model's reading");
    expect(first.textContent).toContain("Klyx read it at");
  });

  it("gives danger its shape, on a destructive step only", () => {
    const { getAllByTestId } = card({ steps: [step(), step({ n: 2, verb: "flux-reconcile", danger: false })] });
    const heads = getAllByTestId("act-step-head");
    expect(within(heads[0]).queryByTestId("act-danger")).toBeTruthy();
    expect(within(heads[1]).queryByTestId("act-danger")).toBeNull();
  });

  it("offers only the next step, and draws the wait into the later one", () => {
    const { getAllByTestId, container } = card(twoSteps());
    expect(getAllByTestId("act-review")).toHaveLength(1);
    const second = container.querySelector('[data-step="2"]');
    // the plain track, unlit; its words are spoken, not shown
    expect(second?.getAttribute("data-segment")).toBe("dependsOn");
    expect(second?.querySelector(".sr-only")?.textContent).toContain("waits for step 1");
  });

  it("opens the object by its chip", () => {
    const { getAllByTitle, openObject } = card(twoSteps());
    fireEvent.click(getAllByTitle("UID uid-web")[0]);
    expect(openObject).toHaveBeenCalledWith(expect.objectContaining({ n: 2, gvr: "apps/v1/deployments" }));
  });

  it("runs a step only through Review, the dialog and its token, and logs it", async () => {
    bridge.reviewStep.mockResolvedValue(reviewed);
    bridge.runStep.mockResolvedValue({ ran: true, result: { ok: true, outcome: "written" } });
    const { getByTestId, findByTestId, getByRole, onChanged } = card(twoSteps());
    fireEvent.click(getByTestId("act-review"));
    const detail = await findByTestId("act-review-detail");
    expect(bridge.reviewStep).toHaveBeenCalledWith("kind-klyx", "c1", "e1", 1);
    expect(getByRole("dialog").textContent).toContain("Run step 1 of the investigation's plan");
    expect(detail.textContent).toContain("spec.suspend: false → true");
    expect(detail.textContent).toContain("Klyx found no GitOps unit managing it");
    expect(detail.textContent).toContain("within 30 s");
    expect(bridge.runStep).not.toHaveBeenCalled();
    fireEvent.click(getByRole("button", { name: "Suspend" }));
    await waitFor(() => expect(bridge.runStep).toHaveBeenCalledWith("kind-klyx", "c1", "e1", 1, "tok-1"));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    const log = useFleet.getState().actionLog[0];
    expect(log.kind).toBe("success");
    expect(log.message).toBe("suspend Kustomization flux-system/a1-web on kind-klyx - from an investigation's plan, step 1: request written");
  });

  it("arms one protected grant per step, before RunStep", async () => {
    bridge.reviewStep.mockResolvedValue(reviewed);
    bridge.runStep.mockResolvedValue({ ran: true, result: { ok: true, outcome: "written" } });
    const { getByTestId, findByRole, getByPlaceholderText, getByRole } = card(twoSteps(), { protected: true });
    expect(getByTestId("act-card").textContent).toContain("Protected: each step needs its own typed confirmation.");
    fireEvent.click(getByTestId("act-review"));
    await findByRole("dialog");
    const confirm = getByRole("button", { name: "Suspend" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.change(getByPlaceholderText("kind-klyx"), { target: { value: "kind-klyx" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.runStep).toHaveBeenCalled());
    expect(cfg.armProtected).toHaveBeenCalledTimes(1);
    expect(cfg.armProtected).toHaveBeenCalledWith("kind-klyx", "kind-klyx");
    expect(cfg.armProtected.mock.invocationCallOrder[0]).toBeLessThan(bridge.runStep.mock.invocationCallOrder[0]);
  });

  it("keeps the step offered on Cancel", async () => {
    bridge.reviewStep.mockResolvedValue(reviewed);
    const { getByTestId, findByRole, getByRole, queryByRole } = card(twoSteps());
    fireEvent.click(getByTestId("act-review"));
    await findByRole("dialog");
    fireEvent.click(getByRole("button", { name: "Cancel" }));
    expect(queryByRole("dialog")).toBeNull();
    expect(bridge.runStep).not.toHaveBeenCalled();
    expect(getByTestId("act-review")).toBeTruthy();
  });

  it("says why Klyx did not offer the step on Review, and reads the plan again", async () => {
    bridge.reviewStep.mockResolvedValue({ offered: false, n: 1, verb: "flux-suspend", target: reviewed.target, danger: true, reason: "Kustomization flux-system/a1-web was replaced (another object holds the name now): nothing was sent" });
    const { getByTestId, findByTestId, queryByRole, onChanged } = card(twoSteps());
    fireEvent.click(getByTestId("act-review"));
    expect((await findByTestId("act-note")).textContent).toContain("was replaced");
    expect(queryByRole("dialog")).toBeNull();
    expect(onChanged).toHaveBeenCalled();
  });

  it("says why RunStep did not run it, and logs that nothing ran", async () => {
    bridge.reviewStep.mockResolvedValue(reviewed);
    bridge.runStep.mockResolvedValue({ ran: false, reason: "it changed since you reviewed it: review it again" });
    const { getByTestId, findByRole, getByRole, findByTestId } = card(twoSteps());
    fireEvent.click(getByTestId("act-review"));
    await findByRole("dialog");
    fireEvent.click(getByRole("button", { name: "Suspend" }));
    expect((await findByTestId("act-note")).textContent).toContain("changed since you reviewed it");
    const log = useFleet.getState().actionLog[0];
    expect(log.kind).toBe("error");
    expect(log.message).toContain("from an investigation's plan, step 1: not run: it changed since you reviewed it");
  });

  it("shows each outcome in words, and lights the segment only past an established effect", () => {
    const established: ActionPlan = { steps: [step({ run: { request: "written", effect: "established", ranAt: "2026-10-14T12:01:00Z" } }), step({ n: 2, verb: "rollout-undo", target: target("Deployment", "shop", "web", "uid-web") })] };
    const a = card(established);
    expect(a.getByTestId("act-request").textContent).toBe("request written");
    expect(a.getByTestId("act-effect").textContent).toBe("effect established");
    expect(a.container.querySelector('[data-step="2"]')?.getAttribute("data-segment")).toBe("completed");
    expect(a.getAllByTestId("act-review")).toHaveLength(1); // step 2 is next
    a.unmount();

    const held: ActionPlan = { status: "held", statusWhy: "step 1 held the plan: its effect is unverified (not established within 30s)", steps: [step({ run: { request: "unverified", effect: "unverified", effectWhy: "not established within 30s" } }), step({ n: 2 })] };
    const b = card(held);
    expect(b.getByTestId("act-request").textContent).toBe("request unverified");
    expect(b.getByTestId("act-effect").textContent).toBe("effect unverified");
    expect(b.getByTestId("act-outcome").textContent).toContain("not established within 30s");
    const seg = b.container.querySelector('[data-step="2"]');
    expect(seg?.getAttribute("data-segment")).toBe("failed");
    expect(seg?.textContent).toContain("step 1 held the plan");
    expect(b.queryByTestId("act-review")).toBeNull();
    expect(b.getByTestId("act-status").textContent).toContain("held");
    expect(b.getByTestId("act-dismiss")).toBeTruthy();
    b.unmount();

    const refused = card({ status: "held", steps: [step({ run: { request: "refused", notSent: true, error: "the cluster is protected: type its name" } })] });
    expect(refused.getByTestId("act-request").textContent).toBe("refused, nothing sent");
    expect(refused.queryByTestId("act-effect")).toBeNull();
    refused.unmount();

    // the card never offers past an unestablished effect, whatever the
    // plan's status says
    const unestablished = card({ steps: [step({ run: { request: "written", effect: "unverified" } }), step({ n: 2 })] });
    expect(unestablished.queryByTestId("act-review")).toBeNull();
  });

  it("offers nothing while a step runs", () => {
    const { queryByTestId } = card({ steps: [step({ run: { request: "written", effect: "pending" } }), step({ n: 2 })] });
    expect(queryByTestId("act-review")).toBeNull();
    expect(queryByTestId("act-dismiss")).toBeNull();
  });

  it("dismisses the plan, and never a superseded, withdrawn or declined one", async () => {
    bridge.dismissPlan.mockResolvedValue({ ran: false, plan: { ...twoSteps(), status: "declined" } });
    const { getByTestId, onChanged, unmount } = card(twoSteps());
    fireEvent.click(getByTestId("act-dismiss"));
    await waitFor(() => expect(bridge.dismissPlan).toHaveBeenCalledWith("kind-klyx", "c1", "e1"));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    unmount();
    for (const status of ["superseded", "withdrawn", "declined"]) {
      const withdrawnStep = step({ state: status === "withdrawn" ? "withdrawn" : "offered", reason: status === "withdrawn" ? "withdrawn: the cluster disconnected" : undefined });
      const c = card({ status, statusWhy: `its unrun steps are ${status}`, steps: [withdrawnStep] });
      expect(c.getByTestId("act-status").textContent).toContain(status);
      expect(c.queryByTestId("act-review")).toBeNull();
      expect(c.queryByTestId("act-dismiss")).toBeNull();
      if (status === "withdrawn") expect(c.getByTestId("act-reason").textContent).toContain("withdrawn: the cluster disconnected");
      c.unmount();
    }
  });

  it("says why Klyx did not offer a step", () => {
    const { getAllByTestId, queryByTestId } = card({ steps: [step({ state: "refused", reason: "it contradicts an earlier step's flux-resume on the same object" })] });
    expect(getAllByTestId("act-reason")[0].textContent).toBe("refused: it contradicts an earlier step's flux-resume on the same object");
    expect(queryByTestId("act-review")).toBeNull();
  });

  it("reads the incident subject by subject, and reminds that a held unit stays suspended", () => {
    const { getByTestId, getAllByTestId } = card({
      steps: [step({ run: { request: "written", effect: "established" } })],
      incident: {
        state: "resolved", why: "every subject recovered or is held as intended",
        subjects: [
          { kind: "Deployment", namespace: "shop", name: "web", uid: "uid-web", reading: "recovered", verdict: "ok", at: "2026-10-14T12:03:00Z" },
          { kind: "Kustomization", namespace: "flux-system", name: "a1-web", uid: "uid-ks", reading: "held by step 1, as intended", verdict: "paused", at: "2026-10-14T12:03:00Z" },
        ],
      },
    });
    expect(getByTestId("act-incident-state").textContent).toBe("resolved");
    const rows = getAllByTestId("act-incident-subject").map((r) => r.textContent);
    expect(rows[0]).toContain("Deployment shop/web: recovered");
    expect(rows[1]).toContain("held by step 1, as intended");
    expect(getByTestId("act-incident").textContent).toContain("A held unit stays suspended until the Git fix lands");
  });

  it("reminds of a held unit only when one is held", () => {
    const { getByTestId } = card({
      steps: [step({ run: { request: "written", effect: "established" } })],
      incident: { state: "resolved", subjects: [{ kind: "Deployment", namespace: "shop", name: "web", uid: "uid-web", reading: "recovered" }] },
    });
    expect(getByTestId("act-incident").textContent).not.toContain("held unit");
  });

  it("shows a plan's note when it holds no steps, and nothing when it holds neither", () => {
    const a = card({ steps: [], note: "action proposals are off in Settings > AI" });
    expect(a.getByTestId("act-plan-note").textContent).toContain("proposals are off");
    a.unmount();
    expect(card({ steps: [] }).container.querySelector('[data-testid="act-card"]')).toBeNull();
  });
});
