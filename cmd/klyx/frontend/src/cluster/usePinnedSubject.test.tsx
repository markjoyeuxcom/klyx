import { describe, it, expect, vi } from "vitest";
import { render, act } from "@testing-library/react";
import { usePinnedSubject, type Gone } from "./usePinnedSubject";
import { expectPin, takeExpectedPin } from "../nav/inspectorPin";

// The Line slice 4 §6.3 (ruled in design review 37f00bc3): a selection is
// pinned to its first UID; a same-name object with a new UID on a complete
// read is "replaced", never silently followed; a partial read claims nothing.

type Row = { uid?: string; name: string; v: number };
let out: { subject: Row | null; gone: Gone | null; next: Row | null; adopt: () => void };
function Lens({ cluster = "c", sel, live, complete = true }: { cluster?: string; sel: string | null; live?: Row; complete?: boolean }) {
  out = usePinnedSubject(cluster, sel, live, complete, "T");
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

// The Line slice 9 note §4.2: an opening that names its object by UID pins
// the selection before any row is seen; a replacement or an absence is said,
// never substituted.
describe("usePinnedSubject with an expected UID", () => {
  let res: ReturnType<typeof usePinnedSubject<Row>>;
  function Expecting({ sel, live, complete = true }: { sel: string | null; live?: Row; complete?: boolean }) {
    res = usePinnedSubject("c", sel, live, complete, "T");
    return null;
  }

  it("the expected row pins as usual", () => {
    expectPin("c", "T", "ns/api", "u1");
    render(<Expecting sel="ns/api" live={row("u1", 4)} />);
    expect([res.subject?.v, res.gone, res.expected]).toEqual([4, null, undefined]);
  });

  it("another UID at the key: replaced on a complete read (no subject, the new one offered), unverified otherwise", () => {
    expectPin("c", "T", "ns/api", "u1");
    const r = render(<Expecting sel="ns/api" live={row("u2", 9)} complete={false} />);
    expect([res.subject, res.gone, res.next, res.expected]).toEqual([null, "unverified", null, "u1"]);
    r.rerender(<Expecting sel="ns/api" live={row("u2", 9)} />);
    expect([res.subject, res.gone, res.next?.uid, res.expected]).toEqual([null, "replaced", "u2", "u1"]);
    act(() => res.adopt());
    expect([res.subject?.uid, res.gone, res.expected]).toEqual(["u2", null, undefined]);
  });

  it("absent: removed on a complete read, unverified otherwise; the row arriving later pins", () => {
    expectPin("c", "T", "ns/api", "u1");
    const r = render(<Expecting sel="ns/api" live={undefined} complete={false} />);
    expect([res.subject, res.gone, res.expected]).toEqual([null, "unverified", "u1"]);
    r.rerender(<Expecting sel="ns/api" live={undefined} />);
    expect([res.subject, res.gone, res.expected]).toEqual([null, "removed", "u1"]);
    r.rerender(<Expecting sel="ns/api" live={row("u1", 2)} />);
    expect([res.subject?.v, res.gone]).toEqual([2, null]);
  });

  it("an expectation is consumed once, and only for its cluster and key", () => {
    expectPin("c", "T", "ns/api", "u1");
    expect(takeExpectedPin("d", "T", "ns/api")).toBeUndefined();
    const r = render(<Expecting sel="ns/web" live={row("u5")} />);
    expect(res.gone).toBeNull();
    r.rerender(<Expecting sel="ns/api" live={row("u2")} />);
    expect(res.gone).toBe("replaced");
    r.rerender(<Expecting sel={null} />);
    r.rerender(<Expecting sel="ns/api" live={row("u2")} />);
    expect([res.subject?.uid, res.gone]).toEqual(["u2", null]); // consumed: a later plain selection pins as usual
  });
});

// Review of ff41862: an opening by UID establishes its object even where
// the key already has one; an expectation expires.
describe("a later opening at the same key", () => {
  it("a new expected UID replaces the subject the key had", () => {
    const r = render(<Lens sel="ns/api" live={row("current")} />);
    expectPin("c", "T", "ns/api", "expected-old");
    r.rerender(<Lens sel="ns/api" live={row("current")} />);
    expect([out.gone, out.subject]).toEqual(["replaced", null]);
    // and the reverse: an opening of the current UID over a replaced pin
    expectPin("c", "T", "ns/api", "current");
    r.rerender(<Lens sel="ns/api" live={row("current", 5)} />);
    expect([out.gone, out.subject?.v]).toEqual([null, 5]);
  });

  it("an expectation older than its TTL is ignored", () => {
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now);
    expectPin("c", "T", "ns/stale", "u-old");
    vi.spyOn(Date, "now").mockReturnValue(now + 11_000);
    render(<Lens sel="ns/stale" live={row("u-new")} />);
    expect([out.gone, out.subject?.uid]).toEqual([null, "u-new"]);
    vi.restoreAllMocks();
  });
});

// Review of efbfb31: an expectation is the pin's it was parked for - two
// pins with the same key never take each other's.
describe("scoped expectations", () => {
  it("an ApplicationSet's expectation is never taken by the Application pin at the same key", () => {
    let app!: ReturnType<typeof usePinnedSubject<Row>>, set!: ReturnType<typeof usePinnedSubject<Row>>;
    function Two({ live }: { live: Row }) {
      app = usePinnedSubject("c", "argocd/shared", live, true, "Application");
      set = usePinnedSubject("c", "argocd/shared", live, true, "ApplicationSet");
      return null;
    }
    const r = render(<Two live={row("current")} />);
    expectPin("c", "ApplicationSet", "argocd/shared", "old");
    r.rerender(<Two live={row("current")} />);
    expect([app.gone, set.gone]).toEqual([null, "replaced"]);
  });
  it("a hook without a scope takes none", () => {
    expectPin("c", "T", "ns/x", "old");
    let res!: ReturnType<typeof usePinnedSubject<Row>>;
    function Unscoped() { res = usePinnedSubject("c", "ns/x", row("current"), true); return null; }
    render(<Unscoped />);
    expect(res.gone).toBeNull();
    expect(takeExpectedPin("c", "T", "ns/x")).toBe("old");
  });
});
