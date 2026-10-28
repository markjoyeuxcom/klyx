import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SourceLine } from "./SourceLine";

// Settings S3b §3.4: the scope sentence, an earlier read qualified, and no
// data under a scope said as such.
describe("SourceLine", () => {
  it("says where the data was read", () => {
    render(<SourceLine sentence="from lgtm-we-metrics, hosted by prd, filtered by cluster=dev" />);
    expect(screen.getByTestId("source-line").textContent).toBe("Read from lgtm-we-metrics, hosted by prd, filtered by cluster=dev");
  });
  it("qualifies data kept from an earlier read", () => {
    render(<SourceLine sentence="from lgtm-we-metrics, hosted by prd, filtered by cluster=dev" stale />);
    expect(screen.getByTestId("source-line").textContent).toBe("Earlier read from lgtm-we-metrics, hosted by prd, filtered by cluster=dev, not current");
  });
  it("says no data under the scope beside the sentence", () => {
    render(<SourceLine sentence="from m, filtered by cluster=dev" noData="no data for dev under cluster=dev" />);
    expect(screen.getByTestId("source-line").textContent).toBe("Read from m, filtered by cluster=dev · no data for dev under cluster=dev");
  });
  it("says nothing without either", () => {
    const { container } = render(<SourceLine />);
    expect(container.innerHTML).toBe("");
  });
});
