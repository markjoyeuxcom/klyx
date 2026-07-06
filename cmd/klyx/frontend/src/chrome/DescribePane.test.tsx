import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";

vi.mock("../bridge/exec", () => ({
  describeResource: vi.fn(),
}));
// CopyButton copies via the crd bridge; mock it so no bindings chain loads.
vi.mock("../bridge/crd", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));
import { describeResource } from "../bridge/exec";
import { copyText } from "../bridge/crd";
import { DescribePane } from "./DescribePane";

const OUTPUT = "Name:         web-abc\nNamespace:    default\nStatus:       Running";

describe("DescribePane", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(describeResource).mockResolvedValue({ ok: true, output: OUTPUT, error: "" });
  });

  it("calls the bridge with cluster/kindArg/namespace/name and shows the output", async () => {
    const { getByText, findByText } = render(
      <DescribePane cluster="homelab" kindArg="pod" namespace="default" name="web-abc" onClose={() => {}} />,
    );
    expect(describeResource).toHaveBeenCalledWith("homelab", "pod", "default", "web-abc");
    // loading state while the run is in flight
    expect(getByText("running kubectl describe…")).toBeTruthy();
    expect(await findByText(/Status:\s+Running/)).toBeTruthy();
  });

  it("titles the pane describe · ns/name (name alone when cluster-scoped)", async () => {
    const { getByText, findByText, unmount } = render(
      <DescribePane cluster="homelab" kindArg="pod" namespace="default" name="web-abc" onClose={() => {}} />,
    );
    expect(getByText("default/web-abc")).toBeTruthy();
    await findByText(/Name:/);
    unmount();

    const { getByText: getByText2 } = render(
      <DescribePane cluster="homelab" kindArg="node" namespace="" name="node-1" onClose={() => {}} />,
    );
    expect(getByText2("node-1")).toBeTruthy();
  });

  it("renders errors honestly in the pane", async () => {
    vi.mocked(describeResource).mockResolvedValue({
      ok: false,
      output: "",
      error: "kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary",
    });
    const { findByRole } = render(
      <DescribePane cluster="homelab" kindArg="pod" namespace="default" name="web-abc" onClose={() => {}} />,
    );
    const alert = await findByRole("alert");
    expect(alert.textContent).toContain("kubectl not found");
  });

  it("copy-all copies the raw output", async () => {
    const { findByLabelText } = render(
      <DescribePane cluster="homelab" kindArg="pod" namespace="default" name="web-abc" onClose={() => {}} />,
    );
    fireEvent.click(await findByLabelText("copy describe output for default/web-abc"));
    await waitFor(() => expect(copyText).toHaveBeenCalledWith(OUTPUT));
  });

  it("closes on the ✕ button and on Escape", async () => {
    const onClose = vi.fn();
    const { getByLabelText, getByRole, findByText } = render(
      <DescribePane cluster="homelab" kindArg="pod" namespace="default" name="web-abc" onClose={onClose} />,
    );
    await findByText(/Name:/);
    fireEvent.click(getByLabelText("close describe"));
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
