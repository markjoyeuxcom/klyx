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
