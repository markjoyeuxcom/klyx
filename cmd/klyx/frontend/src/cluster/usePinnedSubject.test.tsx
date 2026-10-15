import { describe, it, expect } from "vitest";
import { render, act } from "@testing-library/react";
import { usePinnedSubject, type Gone } from "./usePinnedSubject";

// The Line slice 4 §6.3 (ruled in design review 37f00bc3): a selection is
// pinned to its first UID; a same-name object with a new UID on a complete
// read is "replaced", never silently followed; a partial read claims nothing.

type Row = { uid?: string; name: string; v: number };
let out: { subject: Row | null; gone: Gone | null; next: Row | null; adopt: () => void };
function Lens({ cluster = "c", sel, live, complete = true }: { cluster?: string; sel: string | null; live?: Row; complete?: boolean }) {
  out = usePinnedSubject(cluster, sel, live, complete);
  return null;
}
const row = (uid: string | undefined, v = 1): Row => ({ uid, name: "api", v });

describe("usePinnedSubject", () => {
  it("follows the same UID's updates", () => {
    const r = render(<Lens sel="ns/api" live={row("u1", 1)} />);
    r.rerender(<Lens sel="ns/api" live={row("u1", 2)} />);
    expect([out.subject?.v, out.gone]).toEqual([2, null]);
  });

  it("a new UID at the key on a complete read is replaced: the pinned object stays, the new one is offered", () => {
    const r = render(<Lens sel="ns/api" live={row("u1", 1)} />);
    r.rerender(<Lens sel="ns/api" live={row("u2", 9)} />);
    expect([out.subject?.uid, out.subject?.v, out.gone, out.next?.uid]).toEqual(["u1", 1, "replaced", "u2"]);
    act(() => out.adopt());
    expect([out.subject?.uid, out.gone]).toEqual(["u2", null]);
    r.rerender(<Lens sel="ns/api" live={row("u2", 10)} />);
    expect([out.subject?.v, out.gone]).toEqual([10, null]);
  });

  it("a new UID on a read that is not complete claims nothing: unverified, pinned, no offer", () => {
    const r = render(<Lens sel="ns/api" live={row("u1")} />);
    r.rerender(<Lens sel="ns/api" live={row("u2")} complete={false} />);
    expect([out.subject?.uid, out.gone, out.next]).toEqual(["u1", "unverified", null]);
  });

  it("absent: removed on a complete read, unverified otherwise, with the last evidence", () => {
    const r = render(<Lens sel="ns/api" live={row("u1", 3)} />);
    r.rerender(<Lens sel="ns/api" live={undefined} />);
    expect([out.subject?.v, out.gone]).toEqual([3, "removed"]);
    r.rerender(<Lens sel="ns/api" live={undefined} complete={false} />);
    expect([out.subject?.v, out.gone]).toEqual([3, "unverified"]);
  });

  it("an object with no UID never pins and is never replaced", () => {
    const r = render(<Lens sel="ns/api" live={row(undefined, 1)} />);
    r.rerender(<Lens sel="ns/api" live={row(undefined, 2)} />);
    expect([out.subject?.v, out.gone]).toEqual([2, null]);
  });

  it("a new selection or another cluster starts fresh, never inheriting a pin", () => {
    const r = render(<Lens sel="ns/api" live={row("u1")} />);
    r.rerender(<Lens sel="ns/web" live={row("u7")} />);
    expect([out.subject?.uid, out.gone]).toEqual(["u7", null]);
    r.rerender(<Lens sel="ns/api" live={undefined} />);
    expect([out.subject, out.gone]).toEqual([null, null]); // the old pin for ns/api is gone
    r.rerender(<Lens cluster="d" sel="ns/web" live={row("u8")} />);
    expect([out.subject?.uid, out.gone]).toEqual(["u8", null]);
    r.rerender(<Lens sel={null} />);
    expect([out.subject, out.gone]).toEqual([null, null]);
  });
});
