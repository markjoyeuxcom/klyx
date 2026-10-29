import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChartSource, chartSentence, SourceLine } from "./SourceLine";

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

// A range read's charts say their own source wherever the view's readout
// does not already say it (Codex's S3b-2c review, R3).
describe("a chart's own source", () => {
  const spark = { available: true, sentence: "from source-A", lineage: "A" };
  it("says nothing where the readout reads the same source", () => {
    expect(chartSentence(spark, { lineage: "A" })).toEqual({ stale: false });
    const { container } = render(<ChartSource spark={spark} readout={{ lineage: "A" }} />);
    expect(container.innerHTML).toBe("");
  });
  it("keeps the charts as an earlier read where the readout moved", () => {
    render(<ChartSource spark={spark} readout={{ lineage: "B" }} />);
    expect(screen.getByTestId("chart-source").textContent).toBe("30m charts: Earlier read from source-A, not current");
  });
  it("says its own source where there is no current readout", () => {
    render(<ChartSource spark={spark} readout={null} />);
    expect(screen.getByTestId("chart-source").textContent).toBe("30m charts: Read from source-A");
  });
  it("says the range's own no data under its scope", () => {
    render(<ChartSource spark={{ ...spark, noData: "no data for dev under cluster=dev" }} readout={{ lineage: "A" }} />);
    expect(screen.getByTestId("chart-source").textContent).toBe("30m charts: no data for dev under cluster=dev");
  });
  it("says nothing for an unavailable range", () => {
    const { container } = render(<ChartSource spark={{ available: false }} readout={null} />);
    expect(container.innerHTML).toBe("");
  });
});
