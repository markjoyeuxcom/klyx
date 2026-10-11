import { describe, it, expect } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { releaseHealth } from "../cluster/helmHealth";

type Rule = { included: boolean; role: string; label: string };
type Dto = { status: string; updatedUnix: number; then?: { atUnix: number; rule: Rule } };
const lens = (d: Dto, nowS: number): Rule => {
  const r = releaseHealth(d, nowS);
  return { included: r.kind === "fault" || r.kind === "degraded", role: r.kind, label: r.label };
};

describe("pre-switch: the Helm lens's own rule equals the Go rule (golden)", () => {
  it("helm releases, at read time and on each side of the step", () => {
    for (const c of fixtures.helmReleases as unknown as { case: string; nowUnix: number; dto: Dto; want: Rule }[]) {
      expect(lens(c.dto, c.nowUnix), c.case).toEqual(c.want);
      if (c.dto.then) {
        expect(lens(c.dto, c.dto.then.atUnix - 1), `${c.case} (1s before the step)`).toEqual(c.want);
        expect(lens(c.dto, c.dto.then.atUnix), `${c.case} (at the step)`).toEqual(c.dto.then.rule);
        expect(lens(c.dto, c.dto.then.atUnix + 86400), `${c.case} (long after)`).toEqual(c.dto.then.rule);
      }
    }
  });
});
