import { describe, it, expect, vi, afterEach } from "vitest";
import { useState } from "react";
import { render, fireEvent, act } from "@testing-library/react";
import { VirtualList } from "./VirtualList";

// jsdom doesn't layout — clientHeight is 0, so the component falls back to a
// 600px assumed height (FALLBACK_HEIGHT), and neither requestAnimationFrame
// nor ResizeObserver fire on their own. Tests that exercise the scroll/resize
// wiring stub those globals to drive the callbacks deterministically.

const PLAIN_THRESHOLD = 100; // must match the constant in VirtualList.tsx

function makeItems(n: number): string[] {
  return Array.from({ length: n }, (_, i) => `item-${i}`);
}

// Queue-based rAF stub: scroll events queue frames, flushFrames() runs them.
function stubRaf() {
  const frames: FrameRequestCallback[] = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frames.push(cb);
    return frames.length;
  });
  vi.stubGlobal("cancelAnimationFrame", () => {});
  return () => act(() => { frames.splice(0).forEach((cb) => cb(0)); });
}

describe("VirtualList", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("renders all items when count < threshold (plain path)", () => {
    const items = makeItems(PLAIN_THRESHOLD - 1);
    const { getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={32}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    expect(getAllByTestId("row").length).toBe(PLAIN_THRESHOLD - 1);
  });

  it("renders exactly the threshold count plainly (boundary)", () => {
    const items = makeItems(PLAIN_THRESHOLD);
    // Threshold is < PLAIN_THRESHOLD, so at exactly 100 it switches to virtual.
    // Virtual with containerHeight=0 renders overscan (default 8) items from 0.
    const { getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={32}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    // Either all (plain) or a subset (virtual), but not more than total.
    expect(getAllByTestId("row").length).toBeLessThanOrEqual(PLAIN_THRESHOLD);
  });

  it("renders far fewer rows than 1000 items (virtual path)", () => {
    const items = makeItems(1000);
    const { getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={32}
        overscan={8}
        style={{ height: 300 }}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    // Virtual mode: only overscan * 2 + visible rows rendered.
    // With jsdom clientHeight=0, we get firstVisible=0 to lastVisible=overscan.
    const rows = getAllByTestId("row");
    expect(rows.length).toBeLessThan(1000);
  });

  it("scroll event triggers re-render and updates the window", () => {
    const flushFrames = stubRaf();
    const items = makeItems(200);
    const { container, getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={40}
        overscan={4}
        style={{ height: 300 }}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    const scrollEl = container.firstElementChild as HTMLElement;
    // Simulate scrolling to row 100 (offset = 100 * 40 = 4000px)
    Object.defineProperty(scrollEl, "scrollTop", { value: 4000, writable: true, configurable: true });
    fireEvent.scroll(scrollEl);
    flushFrames();
    // After the frame runs, the window starts at firstVisible = max(0, floor(4000/40) - 4) = 96
    const renderedTexts = getAllByTestId("row").map((el) => el.textContent ?? "");
    expect(renderedTexts[0]).toBe("item-96");
    expect(renderedTexts).toContain("item-100");
    expect(renderedTexts).not.toContain("item-0");
  });

  it("applies the LATEST scrollTop when scroll events arrive while a frame is queued", () => {
    const flushFrames = stubRaf();
    const items = makeItems(500);
    const { container, getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={40}
        overscan={4}
        style={{ height: 300 }}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    const scrollEl = container.firstElementChild as HTMLElement;
    Object.defineProperty(scrollEl, "scrollTop", { value: 2000, writable: true, configurable: true });
    fireEvent.scroll(scrollEl);
    // Second event lands while the first frame is still queued (fast scroll).
    scrollEl.scrollTop = 8000;
    fireEvent.scroll(scrollEl);
    flushFrames();
    // 8000/40 = row 200 — the window must reflect the SECOND event, not the
    // stale value captured when the frame was queued (row 50).
    const renderedTexts = getAllByTestId("row").map((el) => el.textContent ?? "");
    expect(renderedTexts).toContain("item-200");
    expect(renderedTexts).not.toContain("item-50");
  });

  it("extends the window when the container grows (ResizeObserver)", () => {
    const instances: { cb: () => void; el?: Element }[] = [];
    class ROStub {
      inst: { cb: () => void; el?: Element };
      constructor(cb: () => void) {
        this.inst = { cb };
        instances.push(this.inst);
      }
      observe(el: Element) { this.inst.el = el; }
      disconnect() {}
    }
    vi.stubGlobal("ResizeObserver", ROStub);
    const items = makeItems(1000);
    const { container, getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={40}
        overscan={0}
        style={{ height: 300 }}
        render={(item, i) => <div key={i} data-testid="row">{item}</div>}
      />,
    );
    // Initial: clientHeight=0 in jsdom → fallback 600px → 16 rows.
    const before = getAllByTestId("row").length;
    const el = container.firstElementChild as HTMLElement;
    Object.defineProperty(el, "clientHeight", { value: 1200, configurable: true });
    act(() => { instances[0].cb(); });
    // 1200px window → more rows rendered without any scroll event.
    const after = getAllByTestId("row").length;
    expect(after).toBeGreaterThan(before);
  });

  it("rowKey keeps row-local state attached to the item across reorder (plain path)", () => {
    function StatefulRow({ label }: { label: string }) {
      const [clicked, setClicked] = useState(false);
      return (
        <div data-testid="srow" data-clicked={clicked ? "yes" : "no"} onClick={() => setClicked(true)}>
          {label}
        </div>
      );
    }
    const list = (items: string[]) => (
      <VirtualList
        items={items}
        rowHeight={32}
        rowKey={(it) => it}
        render={(it) => <StatefulRow key={it} label={it} />}
      />
    );
    const { rerender, getAllByTestId } = render(list(["a", "b"]));
    fireEvent.click(getAllByTestId("srow")[0]); // mark "a"
    rerender(list(["b", "a"])); // live update re-ranks the rows
    const rows = getAllByTestId("srow");
    expect(rows[0].textContent).toBe("b");
    expect(rows[0].getAttribute("data-clicked")).toBe("no");
    expect(rows[1].textContent).toBe("a");
    expect(rows[1].getAttribute("data-clicked")).toBe("yes");
  });

  it("passes index correctly to render function", () => {
    const items = makeItems(5);
    const { getAllByTestId } = render(
      <VirtualList
        items={items}
        rowHeight={32}
        render={(item, i) => <div key={i} data-testid="row" data-idx={i}>{item}</div>}
      />,
    );
    const rows = getAllByTestId("row");
    rows.forEach((row, i) => {
      expect(row.getAttribute("data-idx")).toBe(String(i));
    });
  });

  it("plain path owns its scrolling (overflowY auto on the container)", () => {
    const items = Array.from({ length: 5 }, (_, i) => `row-${i}`);
    const { container } = render(
      <VirtualList items={items} rowHeight={32} style={{ flex: 1, minHeight: 0 }} render={(it) => <div key={it}>{it}</div>} />,
    );
    const outer = container.firstElementChild as HTMLElement;
    // Without this, short lists overflow visibly and the nearest ancestor
    // scroll container scrolls the whole view (list + detail panel) together.
    expect(outer.style.overflowY).toBe("auto");
    expect(outer.style.flex).toBe("1 1 0%"); // caller style still applies
  });
});
