import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, act } from "@testing-library/react";
import gateway from "./describe/fixtures/gateway.txt?raw";

vi.mock("../bridge/exec", () => ({
  describeResource: vi.fn(),
}));
// CopyButton copies via the crd bridge; mock it so no bindings chain loads.
vi.mock("../bridge/crd", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));
import { describeResource, type DescribeResultDTO } from "../bridge/exec";
import { copyText } from "../bridge/crd";
import { DescribePane, findHits } from "./DescribePane";

const OUTPUT = `${gateway}\n`;
const CMD = "/opt/homebrew/bin/kubectl --kubeconfig /k/lab.yaml --context kind-klyx describe gateways.gateway.networking.k8s.io -n infra -- internal";
const ok = (output = OUTPUT): DescribeResultDTO => ({ ok: true, output, error: "", command: CMD, commandError: "" });

// deferred: a describe call the test settles by hand, with the generated
// call's cancel().
function deferred() {
  let resolve!: (r: DescribeResultDTO) => void;
  let reject!: (e: unknown) => void;
  const p = new Promise<DescribeResultDTO>((res, rej) => { resolve = res; reject = rej; });
  const cancel = vi.fn(() => reject(new Error("cancelled")));
  return { p: Object.assign(p, { cancel }), resolve, cancel };
}
const mocked = () => vi.mocked(describeResource) as unknown as ReturnType<typeof vi.fn>;

function sheet(props: Partial<Parameters<typeof DescribePane>[0]> = {}) {
  const onClose = props.onClose ?? vi.fn();
  const r = render(<DescribePane cluster="kind-klyx" kindArg="gateways.gateway.networking.k8s.io" namespace="infra" name="internal" kind="Gateway" onClose={onClose} {...props} />);
  return { ...r, onClose };
}

describe("DescribePane", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocked().mockImplementation(() => Promise.resolve(ok()));
  });

  it("runs the describe for its object and heads the sheet with the kind, ns / name and the command as run", async () => {
    const { getByText, findByTestId, getByRole } = sheet();
    expect(describeResource).toHaveBeenCalledWith("kind-klyx", "gateways.gateway.networking.k8s.io", "infra", "internal");
    expect(getByText("Running kubectl describe…")).toBeTruthy();
    await findByTestId("describe-formatted");
    expect(getByRole("dialog").getAttribute("aria-labelledby")).toBeTruthy();
    expect(document.querySelector(".describe-kind")?.textContent).toBe("Gateway");
    expect(getByText("infra / internal")).toBeTruthy();
    expect(getByText(CMD)).toBeTruthy();
  });

  it("reads the report into sections, with the conditions as a table and the recorded fault marked", async () => {
    // the lab Gateway: Accepted True, Programmed False (AddressNotAssigned)
    const { findByTestId, getByRole } = sheet();
    const f = await findByTestId("describe-formatted");
    const index = getByRole("navigation", { name: "Sections" });
    expect([...index.querySelectorAll(".describe-index-label")].map((b) => b.textContent)).toEqual(["Overview", "Metadata", "Spec", "Status", "Events"]);
    // Events <none>: no rows, said as a count
    expect(index.querySelector("button:last-child .describe-index-count")?.textContent).toBe("0");
    const table = f.querySelector("table[data-kind='conditions']")!;
    expect([...table.querySelectorAll("th")].map((t) => t.textContent)).toEqual(["", "Type", "Status", "Reason", "Last Transition Time", "Observed Generation", "Message"]);
    const rows = [...table.querySelectorAll("tbody tr")];
    expect(rows.map((r) => r.querySelectorAll("td")[1]?.textContent)).toEqual(["Accepted", "Programmed"]);
    expect(rows[0].querySelector("[data-aspect]")).toBeNull();
    expect(rows[1].querySelector("[data-aspect='fault']")?.getAttribute("aria-label")).toBe("Programmed False");
    expect(index.querySelector("[data-aspect='fault']")?.getAttribute("aria-label")).toBe("1 condition at fault");
    // ages beside times, outside kubectl's words
    expect(rows[0].textContent).toMatch(/2026-10-10T18:34:40Z · \d+[smhd] ago/);
  });

  it("marks nothing when every recorded condition is in its good state", async () => {
    const good = gateway.replace("Status:                False\n    Type:                  Programmed", "Status:                True\n    Type:                  Programmed");
    expect(good).not.toBe(gateway);
    mocked().mockImplementation(() => Promise.resolve(ok(good)));
    const { findByTestId, getByRole } = sheet();
    const f = await findByTestId("describe-formatted");
    expect(f.querySelector("[data-aspect]")).toBeNull();
    expect(f.querySelector("table[data-kind='conditions'] th")?.textContent).toBe("Type");
    expect(getByRole("navigation", { name: "Sections" }).querySelector("[data-aspect]")).toBeNull();
  });

  it("shows kubectl's text exactly in the Text view", async () => {
    const { findByTestId, getByRole } = sheet();
    await findByTestId("describe-formatted");
    fireEvent.click(getByRole("button", { name: "Text" }));
    const pre = (await findByTestId("describe-text")).querySelector("pre")!;
    expect(pre.textContent).toBe(OUTPUT);
  });

  it("copies kubectl's output and the pinned command", async () => {
    const { findByTestId, getByRole } = sheet();
    await findByTestId("describe-formatted");
    fireEvent.click(getByRole("button", { name: "Copy" }));
    expect(copyText).toHaveBeenCalledWith(OUTPUT);
    fireEvent.click(getByRole("button", { name: "Copy command" }));
    expect(copyText).toHaveBeenCalledWith(CMD);
  });

  it("disables Copy command with the reason when there is no command", async () => {
    mocked().mockImplementation(() => Promise.resolve({ ...ok(), command: "", commandError: "cluster kubeconfig is a path list, which cannot be passed to a Windows terminal" }));
    const { findByTestId, getByRole } = sheet();
    await findByTestId("describe-formatted");
    const b = getByRole("button", { name: "Copy command" }) as HTMLButtonElement;
    expect(b.disabled).toBe(true);
    expect(b.title).toMatch(/path list/);
  });

  it("finds across the formatted report, cycles, opens a collapsed section, and Escape clears then closes", async () => {
    const { findByTestId, getByLabelText, getByText, onClose } = sheet();
    const f = await findByTestId("describe-formatted");
    fireEvent.click(f.querySelector<HTMLElement>("[data-section='s1'] .describe-section-head")!);
    expect(f.querySelector("[data-section='s1'] .describe-grid")).toBeNull();
    const input = getByLabelText("Find in report");
    fireEvent.change(input, { target: { value: "69423343" } });
    expect(getByText("1 of 1")).toBeTruthy();
    expect(f.querySelector("[data-section='s1'] mark[data-cur='true']")?.textContent).toBe("69423343");
    fireEvent.change(input, { target: { value: "accepted" } });
    const count = () => document.querySelector(".describe-count")?.textContent;
    const total = Number(count()!.split(" of ")[1]);
    expect(total).toBeGreaterThan(1);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(count()).toBe(`2 of ${total}`);
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(count()).toBe(`${total} of ${total}`);
    fireEvent.keyDown(input, { key: "Escape" });
    expect((input as HTMLInputElement).value).toBe("");
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("counts only kubectl's words: ages and labels are never matches", () => {
    const hits = findHits([{ id: 0, text: "2026-10-10T18:34:40Z" }, { id: 1, text: "ago" }], "ago");
    expect(hits.list).toEqual([{ run: 1, start: 0, end: 3 }]);
  });

  it("disables Refresh while a run is in flight, and keeps the report on screen while it refreshes", async () => {
    const second = deferred();
    const { findByTestId, getByRole, getByText } = sheet();
    await findByTestId("describe-formatted");
    mocked().mockImplementation(() => second.p);
    const refresh = getByRole("button", { name: "Refresh" }) as HTMLButtonElement;
    fireEvent.click(refresh);
    expect(refresh.disabled).toBe(true);
    expect(getByText("refreshing…")).toBeTruthy();
    expect(document.querySelector("[data-testid='describe-formatted']")).toBeTruthy();
    await act(async () => second.resolve(ok()));
    expect(refresh.disabled).toBe(false);
  });

  it("keeps the last report, labelled, when a refresh fails without output", async () => {
    const { findByTestId, getByRole, findByRole, getByText } = sheet();
    await findByTestId("describe-formatted");
    mocked().mockImplementation(() => Promise.resolve({ ok: false, output: "", error: "Unable to connect to the server", command: CMD, commandError: "" }));
    fireEvent.click(getByRole("button", { name: "Refresh" }));
    const alert = await findByRole("alert");
    expect(alert.textContent).toContain("The refresh failed");
    expect(alert.textContent).toContain("Unable to connect");
    expect(getByText(/^Output from .*, before the failed refresh$/)).toBeTruthy();
    expect(document.querySelector("[data-testid='describe-formatted']")).toBeTruthy();
  });

  it("shows a failed run's partial output as the newest evidence, labelled", async () => {
    const { findByTestId, getByRole, findByText } = sheet();
    await findByTestId("describe-formatted");
    mocked().mockImplementation(() => Promise.resolve({ ok: false, output: "Name:  internal\n", error: "events: forbidden", command: CMD, commandError: "" }));
    fireEvent.click(getByRole("button", { name: "Refresh" }));
    expect(await findByText("Partial output: kubectl exited with an error")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Text" }));
    expect((await findByTestId("describe-text")).textContent).toBe("Name:  internal\n");
  });

  it("renders a first failed run honestly", async () => {
    mocked().mockImplementation(() => Promise.resolve({ ok: false, output: "", error: "kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary", command: "", commandError: "" }));
    const { findByRole } = sheet();
    const alert = await findByRole("alert");
    expect(alert.textContent).toContain("kubectl describe failed");
    expect(alert.textContent).toContain("kubectl not found");
  });

  it("fences replies by identity: A, then B, then A, with late replies ignored", async () => {
    const a1 = deferred();
    const b = deferred();
    const a2 = deferred();
    mocked().mockImplementationOnce(() => a1.p).mockImplementationOnce(() => b.p).mockImplementationOnce(() => a2.p);
    const { rerender, queryByTestId, findByTestId } = sheet();
    const props = { cluster: "kind-klyx", kindArg: "gateways.gateway.networking.k8s.io", namespace: "infra", kind: "Gateway", onClose: () => {} };
    rerender(<DescribePane {...props} name="other" />);
    expect(a1.cancel).toHaveBeenCalled();
    rerender(<DescribePane {...props} name="internal" />);
    expect(b.cancel).toHaveBeenCalled();
    // the cancelled calls answer late: neither may show
    await act(async () => { a1.resolve(ok("Name:  stale-a\n")); b.resolve(ok("Name:  other\n")); });
    expect(queryByTestId("describe-formatted")).toBeNull();
    await act(async () => a2.resolve(ok()));
    expect((await findByTestId("describe-formatted")).textContent).toContain("internal");
    expect(document.body.textContent).not.toContain("stale-a");
  });

  it("cancels the run in flight when the sheet closes", () => {
    const d = deferred();
    mocked().mockImplementation(() => d.p);
    const { unmount } = sheet();
    unmount();
    expect(d.cancel).toHaveBeenCalled();
  });

  it("says when the report describes a different object at this name", async () => {
    const { findByRole, unmount } = sheet({ expectUid: "11111111-0000-0000-0000-000000000000" });
    expect((await findByRole("status")).textContent).toContain("This describes a different object at this name (UID 69423343-dafb-43d8-a685-f74670ac26a8), not the one in the inspector.");
    unmount();
    const same = sheet({ expectUid: "69423343-dafb-43d8-a685-f74670ac26a8" });
    await same.findByTestId("describe-formatted");
    expect(same.queryByRole("status")).toBeNull();
  });

  it("claims nothing about the UID when the report prints none", async () => {
    mocked().mockImplementation(() => Promise.resolve(ok("Name:  web\nNamespace:  default\nStatus:  Running\n")));
    const { findByTestId, queryByRole } = sheet({ kindArg: "pod", kind: "Pod", namespace: "default", name: "web", expectUid: "u-1" });
    await findByTestId("describe-formatted");
    expect(queryByRole("status")).toBeNull();
  });

  it("moves find to the sheet's input on the find key", async () => {
    const { findByTestId, getByRole, getByLabelText } = sheet();
    await findByTestId("describe-formatted");
    fireEvent.keyDown(getByRole("dialog"), { key: "f", metaKey: true });
    await waitFor(() => expect(document.activeElement).toBe(getByLabelText("Find in report")));
  });
});
