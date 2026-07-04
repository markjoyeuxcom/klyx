import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";

vi.mock("../bridge/crd", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));
import { copyText } from "../bridge/crd";
import { CopyButton } from "./CopyButton";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("CopyButton", () => {
  it("copies the text and flips to a transient copied state", async () => {
    const { getByRole } = render(<CopyButton text="grafana-abc123" label="copy pod name" />);
    const btn = getByRole("button", { name: "copy pod name" });
    await act(async () => {
      fireEvent.click(btn);
    });
    expect(copyText).toHaveBeenCalledWith("grafana-abc123");
    expect(btn.getAttribute("title")).toBe("Copied");
  });

  it("never bubbles the click to the row", async () => {
    const rowClick = vi.fn();
    const { getByRole } = render(
      <div onClick={rowClick}>
        <CopyButton text="x" label="copy name" />
      </div>,
    );
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "copy name" }));
    });
    expect(rowClick).not.toHaveBeenCalled();
    expect(copyText).toHaveBeenCalled();
  });

  it("appearOnRowHover marks the button for the CSS reveal rule", () => {
    const { getByRole } = render(<CopyButton text="x" label="copy" appearOnRowHover />);
    expect(getByRole("button", { name: "copy" }).getAttribute("data-rowcopy")).toBe("true");
  });
});
