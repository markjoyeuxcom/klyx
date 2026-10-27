import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import type { Plan } from "../../bridge/plan";
import type { Installation, SourcesState } from "../../bridge/sources";
import { _resetNavGuardForTest, mustAsk, requestLeave, resolveLeave, useLeaveRequest } from "../navGuard";
import { SourcesPanel } from "./SourcesPanel";

// Settings S3a §8: Observability's alert sources keep S2c's discipline - a
// draft is planned before anything is written, a change withdraws its
// plan, a changed draft or a typed credential is a draft to the leave
// guard, nothing is called current before the readback, and a lost answer
// is never a refusal.

const b = vi.hoisted(() => ({
  getSources: vi.fn(),
  migrationOffers: vi.fn(),
  planSources: vi.fn(),
  planSourcesFromFile: vi.fn(),
  applySources: vi.fn(),
  detect: vi.fn(),
  setSourceCredential: vi.fn(),
  removeSourceCredential: vi.fn(),
  testSource: vi.fn(),
}));
vi.mock("../../bridge/sources", () => b);

const inst = (over: Partial<Installation> = {}): Installation => ({
  id: "shared", revision: "r1", where: "https://am.example:443", credential: "not stored", subscribers: ["dev"],
  endpoint: "https://am.example", authType: "bearer", ...over,
});

const state = (over: Partial<SourcesState> = {}): SourcesState => ({
  sentence: "One installation is in force; dev subscribes to it.",
  installations: [inst()],
  subscriptions: [
    { cluster: "dev", mode: "subscribed", via: "its own block", source: "shared", sentence: "dev reads shared's alerts with cluster=dev.", block: { source: "shared" } },
    { cluster: "prd", mode: "its own Alertmanager", via: "", sentence: "prd reads its own Alertmanager." },
  ],
  fileDiffers: false,
  ...over,
});

const aPlan = (over: Partial<Plan> = {}): Plan => ({
  section: "observability", kind: "save", digest: "d1", fileVersion: "v1", runtime: {},
  changes: [{ id: "sources.shared.endpoint", label: "shared's endpoint", from: "https://am.example:443", to: "https://am.example:443/v2" }],
  ends: [], continues: [], writes: true, activates: true, ...over,
});

function renderPanel(refreshMs = 0) {
  return render(<ThemeProvider><SourcesPanel refreshMs={refreshMs} /></ThemeProvider>);
}

async function editShared() {
  fireEvent.click(await screen.findByRole("button", { name: "edit shared" }));
  return screen.findByTestId("installation-editor");
}

const endpointOf = (ed: HTMLElement) => within(ed).getByLabelText("Installation URL") as HTMLInputElement;

describe("Settings > Observability > Alert sources (S3a §8)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    _resetNavGuardForTest();
    b.getSources.mockResolvedValue(state());
    b.migrationOffers.mockResolvedValue([]);
  });
  afterEach(() => vi.useRealTimers());

  it("edits an installation through its plan: planned, then applied, then read back", async () => {
    b.planSources.mockResolvedValue(aPlan());
    b.applySources.mockResolvedValue({ ok: true });
    renderPanel();
    const ed = await editShared();
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    expect(b.planSources).toHaveBeenCalledWith({ installation: { op: "edit", source: { id: "shared", endpoint: "https://am.example/v2", authType: "bearer" } }, subscriptions: undefined });
    expect(b.applySources).not.toHaveBeenCalled(); // nothing is written before Apply
    expect(screen.getByRole("region", { name: "Review the changes to shared" })).toBeTruthy();
    const reads = b.getSources.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(b.applySources).toHaveBeenCalledWith("d1"));
    expect((await screen.findByTestId("sources-banner")).textContent).toContain("Applied: fleet.yaml says so");
    expect(b.getSources.mock.calls.length).toBe(reads + 1);
    expect(screen.queryByTestId("installation-editor")).toBeNull();
    expect(screen.queryByTestId("settings-plan")).toBeNull();
  });

  it("withdraws a plan when its draft changes, and never shows a plan for an older draft", async () => {
    let answer: (p: Plan) => void = () => {};
    b.planSources.mockResolvedValueOnce(aPlan()).mockImplementationOnce(() => new Promise<Plan>((r) => { answer = r; }));
    renderPanel();
    const ed = await editShared();
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v3" } });
    expect(screen.queryByTestId("settings-plan")).toBeNull();
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await waitFor(() => expect(b.planSources).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(endpointOf(ed).disabled).toBe(true)); // busy while it plans
    // the draft is discarded while its plan is asked for: that plan is never shown
    requestLeave(() => {});
    await act(async () => { await resolveLeave("discard"); });
    expect(screen.queryByTestId("installation-editor")).toBeNull();
    await act(async () => { answer(aPlan({ digest: "old" })); });
    expect(screen.queryByTestId("settings-plan")).toBeNull();
  });

  it("names a changed draft and a typed credential to the leave guard, and discard drops both", async () => {
    renderPanel();
    const ed = await editShared();
    expect(mustAsk()).toBe(false); // an editor opened unchanged is no draft
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.change(screen.getByLabelText("shared token"), { target: { value: "s3cret" } });
    expect(mustAsk()).toBe(true);
    requestLeave(() => {});
    const req = useLeaveRequest.getState().request;
    expect(req?.unsaved).toEqual(["a change to the alert sources not yet applied"]);
    expect(req?.credentials).toEqual(["an unsaved shared token"]);
    expect(req?.settings).toBe(false); // Save never applies a plan, nor stores a credential
    await act(async () => { await resolveLeave("discard"); });
    expect(screen.queryByTestId("installation-editor")).toBeNull();
    expect((screen.getByLabelText("shared token") as HTMLInputElement).value).toBe("");
    expect(mustAsk()).toBe(false);
  });

  it("cannot apply a refused plan, and shows a replacement plan with the reason it replaced the first", async () => {
    b.planSources.mockResolvedValueOnce(aPlan({ digest: "", refused: "fleet.yaml no longer has the installation shared" }));
    renderPanel();
    const ed = await editShared();
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-refused")).textContent).toContain("no longer has the installation shared");
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();

    b.planSources.mockResolvedValueOnce(aPlan());
    b.applySources.mockResolvedValueOnce({ ok: false, error: "the installations in force moved", plan: aPlan({ digest: "d2", changes: [{ id: "sources.shared.tenant", label: "shared's tenant", from: "none", to: "ops" }] }) });
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v3" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d1"));
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d2"));
    expect(screen.getByTestId("sources-banner").textContent).toContain("Not applied: the installations in force moved");
    expect(screen.getByTestId("installation-editor")).toBeTruthy(); // the draft is kept
  });

  for (const reread of [true, false]) {
    it(`says a lost answer is not known, never refused, and reads back (${reread ? "the read works" : "the read fails"})`, async () => {
      b.planSources.mockResolvedValue(aPlan());
      b.applySources.mockResolvedValue({ ok: false, unknown: true, error: "the call timed out" });
      renderPanel();
      const ed = await editShared();
      fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
      fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
      await screen.findByTestId("settings-plan");
      if (!reread) b.getSources.mockResolvedValue(null);
      fireEvent.click(screen.getByRole("button", { name: "Apply" }));
      await waitFor(() => expect(screen.getByTestId("sources-banner").textContent).toMatch(reread ? /what is in force now/ : /may not be what is in force/));
      const text = screen.getByTestId("sources-banner").textContent ?? "";
      expect(text).toContain("whether it applied is not known");
      expect(text).not.toContain("Not applied");
      expect(text).not.toContain("Applied:");
      expect(screen.getByTestId("installation-editor")).toBeTruthy(); // nothing says the draft is in force
      if (!reread) expect(screen.getByTestId("sources-reread-failed")).toBeTruthy();
    });
  }

  it("never says only Applied when the readback fails", async () => {
    b.planSources.mockResolvedValue(aPlan());
    b.applySources.mockResolvedValue({ ok: true });
    renderPanel();
    const ed = await editShared();
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    b.getSources.mockResolvedValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.getByTestId("sources-banner").textContent).toContain("Reading the alert sources again failed: what is shown may be out of date."));
  });

  it("keeps the drafts across live refreshes, and drops a credential typed for a revision that is gone", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderPanel(30_000);
    const ed = await editShared();
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.change(screen.getByLabelText("shared token"), { target: { value: "typed" } });
    await act(async () => { vi.advanceTimersByTime(30_000); });
    await waitFor(() => expect(b.getSources).toHaveBeenCalledTimes(2));
    expect(endpointOf(screen.getByTestId("installation-editor")).value).toBe("https://am.example/v2");
    expect((screen.getByLabelText("shared token") as HTMLInputElement).value).toBe("typed");
    // the installation was replaced meanwhile: what was typed was for the old one
    b.getSources.mockResolvedValue(state({ installations: [inst({ revision: "r2" })] }));
    await act(async () => { vi.advanceTimersByTime(30_000); });
    await waitFor(() => expect((screen.getByLabelText("shared token") as HTMLInputElement).value).toBe(""));
    expect(endpointOf(screen.getByTestId("installation-editor")).value).toBe("https://am.example/v2");
    // and is gone, not hidden: the revision coming back does not bring it back
    b.getSources.mockResolvedValue(state());
    await act(async () => { vi.advanceTimersByTime(30_000); });
    await waitFor(() => expect(b.getSources).toHaveBeenCalledTimes(4));
    expect((screen.getByLabelText("shared token") as HTMLInputElement).value).toBe("");
  });

  it("stores a credential for the installation's revision as shown, and clears what was typed", async () => {
    b.setSourceCredential.mockResolvedValue({ ok: true, message: "Stored the shared token in the keychain." });
    renderPanel();
    fireEvent.change(await screen.findByLabelText("shared token"), { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Store the shared credential" }));
    await waitFor(() => expect(b.setSourceCredential).toHaveBeenCalledWith("shared", "r1", "s3cret"));
    expect((screen.getByLabelText("shared token") as HTMLInputElement).value).toBe("");
    expect((await screen.findByTestId("sources-banner")).textContent).toContain("Stored the shared token");
    expect(mustAsk()).toBe(false);
  });

  it("removes an installation with its subscribers' own blocks, as one plan", async () => {
    b.planSources.mockResolvedValue(aPlan({ changes: [{ id: "sources.shared", label: "Installation shared", from: "https://am.example:443", to: "removed" }] }));
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "remove shared" }));
    await screen.findByTestId("settings-plan");
    expect(b.planSources).toHaveBeenCalledWith({ installation: { op: "remove", source: { id: "shared" } }, subscriptions: [{ cluster: "dev", block: null }] });
  });

  it("says a failed first read is a failure, with Retry, never an empty list", async () => {
    b.getSources.mockResolvedValueOnce(null);
    renderPanel();
    expect((await screen.findByTestId("sources-load-failed")).textContent).toContain("Could not read the alert sources");
    expect(screen.queryByText(/No installation is in force/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByTestId("sources-sentence")).toBeTruthy();
  });

  it("edits a cluster's own block: a match a line, a bad line blocks review, and removing the block is a plan", async () => {
    b.planSources.mockResolvedValue(aPlan());
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "edit dev's alert subscription" }));
    const ed = await screen.findByTestId("subscription-editor");
    const match = within(ed).getByLabelText("Alertmanager match");
    const review = within(ed).getByRole("button", { name: "Review and apply" }) as HTMLButtonElement;
    fireEvent.change(match, { target: { value: "team=platform\nnot a pair" } });
    expect(review.disabled).toBe(true);
    expect(ed.textContent).toContain("Each line needs key=value: not a pair");
    fireEvent.change(match, { target: { value: "team=platform\nenv=dev" } });
    fireEvent.click(review);
    await waitFor(() => expect(b.planSources).toHaveBeenCalledWith({ subscriptions: [{ cluster: "dev", block: { source: "shared", match: { team: "platform", env: "dev" } } }] }));
    fireEvent.click(within(ed).getByRole("button", { name: "Remove its own block…" }));
    await waitFor(() => expect(b.planSources).toHaveBeenLastCalledWith({ subscriptions: [{ cluster: "dev", block: null }] }));
  });

  it("wraps long names and URLs, and titles an id cut short", async () => {
    const id = "a-very-long-installation-id-that-keeps-going-far-past-any-column";
    const where = "https://alertmanager.observability.platform.example.internal:9443/a/long/path/prefix/that/must/wrap";
    b.getSources.mockResolvedValue(state({ installations: [inst({ id, where, name: "x".repeat(120) })] }));
    renderPanel();
    const row = await screen.findByRole("group", { name: `installation ${id}` });
    expect(within(row).getByTitle(id).textContent).toBe(id);
    expect(within(row).getByText(where).style.overflowWrap).toBe("anywhere");
  });

  it("works from the keyboard: an editor opens at its first control, a plan takes the focus, and Cancel gives it back", async () => {
    b.planSources.mockResolvedValue(aPlan());
    renderPanel();
    const edit = await screen.findByRole("button", { name: "edit shared" });
    edit.focus();
    fireEvent.click(edit);
    const ed = await screen.findByTestId("installation-editor");
    await waitFor(() => expect(document.activeElement).toBe(within(ed).getByLabelText("Installation name"))); // the id is fixed
    fireEvent.change(endpointOf(ed), { target: { value: "https://am.example/v2" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    const planRegion = await screen.findByTestId("settings-plan");
    await waitFor(() => expect(document.activeElement?.contains(planRegion)).toBe(true));
    fireEvent.click(within(ed).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(document.activeElement).toBe(edit));
  });

  it("opens Detect's proposal as an editor that lists the subscriptions it carries", async () => {
    b.detect.mockResolvedValue({
      clusters: [{ cluster: "prd", outcome: "found" }],
      proposals: [{
        host: "prd", where: "Service monitoring/alertmanager:9093 in prd", sample: "12 alerts", memberLabel: "cluster", membersSeen: ["dev", "prd"], notes: ["a proposal, never proof the installation is shared"],
        draft: { installation: { op: "add", source: { id: "prd-am", host: "prd", serviceRef: { namespace: "monitoring", name: "alertmanager", port: "9093" }, memberLabel: "cluster" } }, subscriptions: [{ cluster: "dev", block: { source: "prd-am" } }] },
      }],
    });
    renderPanel();
    fireEvent.change(await screen.findByLabelText("what Detect looks in"), { target: { value: "c:prd" } });
    fireEvent.click(screen.getByRole("button", { name: "Detect" }));
    const proposal = await screen.findByTestId("detect-proposal");
    expect(b.detect).toHaveBeenCalledWith({ cluster: "prd" });
    expect(proposal.textContent).toContain("never proof");
    fireEvent.click(within(proposal).getByRole("button", { name: "Edit and review…" }));
    const ed = await screen.findByTestId("installation-editor");
    expect((within(ed).getByLabelText("Installation id") as HTMLInputElement).value).toBe("prd-am");
    expect(within(ed).getByTestId("installation-editor-subscriptions").textContent).toContain("dev: subscribed to prd-am");
  });
});
