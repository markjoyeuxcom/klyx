import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ClampedText } from "./ClampedText";

// UI review 2026-10-13: a long message is clipped to a few lines with an
// explicit control; the whole text stays in the page.
describe("ClampedText", () => {
  const long = "health check failed after 4m59.9s: timeout waiting for: [Deployment/observability/grafana-agent status: 'InProgress', StatefulSet/observability/loki-ingester status: 'Failed': container loki crashlooping with OOMKilled (limit 512Mi)]";

  it("clips a long message, keeps all of it in the page, and expands on request", () => {
    const { getByTestId, getByRole } = render(<ClampedText text={long} style={{ color: "red" }} />);
    const text = getByTestId("clamped-text");
    expect(text.textContent).toBe(long);
    expect(text.style.webkitLineClamp).toBe("3");
    expect(text.style.color).toBe("red");
    const toggle = getByRole("button", { name: "Show full message" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(getByTestId("clamped-text").style.webkitLineClamp).toBe("");
    expect(getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("a short message is shown as it is, with no control", () => {
    const { queryByRole, getByTestId } = render(<ClampedText text="Back-off pulling image" prefix="Failed: " />);
    expect(getByTestId("clamped-text").textContent).toBe("Failed: Back-off pulling image");
    expect(queryByRole("button")).toBeNull();
  });
});
