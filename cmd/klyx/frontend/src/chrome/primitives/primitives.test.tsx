import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { Badge, FilterButton, SummaryCell, Section, Field } from "./index";

describe("primitives", () => {
  it("Badge renders its content for each variant", () => {
    const { getByText, rerender } = render(<Badge tone="danger">3 broken</Badge>);
    expect(getByText("3 broken")).toBeTruthy();
    rerender(<Badge tone="success" variant="soft">ready</Badge>);
    expect(getByText("ready")).toBeTruthy();
    rerender(<Badge tone="warning" variant="filled">degraded</Badge>);
    expect(getByText("degraded")).toBeTruthy();
  });

  it("FilterButton shows the count, reflects active, and fires onClick", () => {
    const onClick = vi.fn();
    const { getByRole } = render(<FilterButton label="failed" active count={2} onClick={onClick} />);
    const btn = getByRole("button", { name: /failed 2/i });
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(btn);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("FilterButton hides a zero count", () => {
    const { getByRole } = render(<FilterButton label="suspended" active={false} count={0} onClick={() => {}} />);
    expect(getByRole("button").textContent).toBe("suspended");
  });

  it("SummaryCell renders label + value", () => {
    const { getByText } = render(<SummaryCell label="certificates" value="12" />);
    expect(getByText("certificates")).toBeTruthy();
    expect(getByText("12")).toBeTruthy();
  });

  it("Section + Field render label/value pairs", () => {
    const { getByText } = render(
      <Section title="issuer">
        <Field label="ref" value="ClusterIssuer/lab" />
      </Section>,
    );
    expect(getByText("issuer")).toBeTruthy();
    expect(getByText("ref")).toBeTruthy();
    expect(getByText("ClusterIssuer/lab")).toBeTruthy();
  });
});
