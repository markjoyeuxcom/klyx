import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { MetersStrip } from "./MetersStrip";

describe("MetersStrip", () => {
  it("renders counter wells with the ordering note", () => {
    const { getByText } = render(
      <MetersStrip
        note="broken first · severity order"
        meters={[
          { label: "pods", value: 214 },
          { label: "crashloop", value: 1, tone: "danger" },
          { label: "running", value: 209, tone: "success" },
        ]}
      />,
    );
    expect(getByText("pods")).toBeTruthy();
    expect(getByText("214")).toBeTruthy();
    expect(getByText("crashloop")).toBeTruthy();
    expect(getByText("broken first · severity order")).toBeTruthy();
  });

  it("drops falsy meters - zero-fault cells never render", () => {
    const zero = 0;
    const { queryByText, getByText } = render(
      <MetersStrip meters={[{ label: "total", value: 3 }, zero > 0 && { label: "failed", value: zero, tone: "danger" }]} />,
    );
    expect(getByText("total")).toBeTruthy();
    expect(queryByText("failed")).toBeNull();
  });

  it("renders nothing at all when no meters survive", () => {
    const { container } = render(<MetersStrip meters={[false, null]} note="never shown" />);
    expect(container.innerHTML).toBe("");
  });
});
