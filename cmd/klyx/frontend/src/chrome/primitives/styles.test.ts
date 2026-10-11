import { describe, it, expect } from "vitest";
import { toneColor, toneBg, toneBorder, readyColor, ellipsis, btn, actionBtn } from "./styles";

describe("primitive style tokens", () => {
  it("maps every tone to text/bg/border CSS vars", () => {
    expect(toneColor.danger).toBe("var(--color-text-danger)");
    expect(toneBg.success).toBe("var(--color-background-success)");
    expect(toneBorder.info).toBe("var(--color-border-info)");
    expect(toneColor.muted).toBe("var(--color-text-tertiary)");
  });

  it("maps reconciliation/health states to colours", () => {
    expect(readyColor.Ready).toBe("var(--color-text-success)");
    expect(readyColor.Failed).toBe("var(--color-text-danger)");
    // Issuing (cert-manager) and Reconciling (flux) share the in-progress colour.
    expect(readyColor.Issuing).toBe(readyColor.Reconciling);
  });

  it("ellipsis clips", () => {
    expect(ellipsis.whiteSpace).toBe("nowrap");
  });

  it("actionBtn is a btn at primary weight", () => {
    expect(actionBtn.padding).toBe(btn.padding);
    expect(actionBtn.color).toBe("var(--color-text-primary)");
    expect(btn.color).toBe("var(--color-text-secondary)");
  });
});
