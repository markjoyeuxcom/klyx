import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";

vi.mock("../bridge/crd", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));

import { YamlBlock, tokenizeYamlLine } from "./YamlBlock";

const YAML = [
  "apiVersion: apps/v1",
  "kind: Deployment",
  "metadata:",
  "  name: api # the workload",
  "spec:",
  "  replicas: 3",
  "  paused: false",
].join("\n");

describe("tokenizeYamlLine", () => {
  it("colors keys, strings, numbers, and comments distinctly", () => {
    const key = tokenizeYamlLine("  name: api");
    expect(key.some((t) => t.text === "name" && t.color)).toBe(true);
    const num = tokenizeYamlLine("  replicas: 3");
    expect(num[num.length - 1].text).toContain("3");
    expect(num[num.length - 1].color).toBeTruthy();
    const cmt = tokenizeYamlLine("# a comment");
    expect(cmt[0].color).toBeTruthy();
    // never loses characters
    for (const line of YAML.split("\n")) {
      expect(tokenizeYamlLine(line).map((t) => t.text).join("")).toBe(line);
    }
  });
});

describe("YamlBlock", () => {
  it("renders the full text with highlighting", () => {
    const { container } = render(<YamlBlock text={YAML} copyLabel="test YAML" />);
    expect(container.textContent).toContain("apiVersion: apps/v1");
    expect(container.querySelectorAll("span[style]").length).toBeGreaterThan(0);
  });

  it("find reports a match count and marks matching lines", () => {
    const { getByLabelText, getByText, container } = render(<YamlBlock text={YAML} copyLabel="test YAML" />);
    fireEvent.change(getByLabelText("find in test YAML"), { target: { value: "replicas" } });
    expect(getByText("1/1")).toBeTruthy();
    expect(container.querySelectorAll("mark").length).toBeGreaterThan(0);
    fireEvent.change(getByLabelText("find in test YAML"), { target: { value: "zzz-absent" } });
    expect(getByText("0")).toBeTruthy();
  });

  it("Enter cycles through matches", () => {
    const { getByLabelText, getByText } = render(<YamlBlock text={"a: 1\nb: 2\na: 3"} copyLabel="test YAML" />);
    const input = getByLabelText("find in test YAML");
    fireEvent.change(input, { target: { value: "a:" } });
    expect(getByText("1/2")).toBeTruthy();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(getByText("2/2")).toBeTruthy();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(getByText("1/2")).toBeTruthy();
  });
});

// UI review 2026-10-13: unwrapped by default, a Wrap toggle the viewer keeps.
describe("YamlBlock wrap", () => {
  it("scrolls sideways by default and wraps on request, remembered", async () => {
    const { render, fireEvent } = await import("@testing-library/react");
    try { localStorage.removeItem("klyx.yaml.wrap"); } catch { /* storage may be absent */ }
    const { container, getByRole, unmount } = render(<YamlBlock text={"metadata:\n  uid: 0d7b7d0e-1a51-4e1e-9d44-000000000001"} copyLabel="pod YAML" />);
    const pre = () => container.querySelector("pre") as HTMLPreElement;
    expect(pre().style.whiteSpace).toBe("pre");
    const toggle = getByRole("button", { name: "Wrap" });
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(toggle);
    expect(pre().style.whiteSpace).toBe("pre-wrap");
    unmount();
    const again = render(<YamlBlock text="a: 1" copyLabel="pod YAML" />);
    expect((again.container.querySelector("pre") as HTMLPreElement).style.whiteSpace).toBe("pre-wrap");
  });
});
