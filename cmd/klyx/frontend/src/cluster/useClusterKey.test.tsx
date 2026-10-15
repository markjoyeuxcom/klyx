import { describe, it, expect } from "vitest";
import { render, act } from "@testing-library/react";
import { useCallback } from "react";
import { useClusterKey } from "./useClusterKey";

// The Line slice 4 §6.3: a local selection carries its cluster. After a
// cluster change no render shows the old key, and a memoised handler still
// selects on the cluster current when it runs.

const seen: { cluster: string; key: string | null }[] = [];
let select: (k: string | null) => void = () => {};

function Lens({ cluster }: { cluster: string }) {
  const [key, setKey] = useClusterKey(cluster);
  // a handler memoised once, as some lenses do
  const pick = useCallback((k: string | null) => setKey(k), [setKey]);
  select = pick;
  seen.push({ cluster, key });
  return <div data-testid="inspector">{key ?? ""}</div>;
}

describe("useClusterKey", () => {
  it("no render pairs a key with another cluster; the setter is stable and records the current cluster", () => {
    seen.length = 0;
    const r = render(<Lens cluster="a" />);
    const first = select;
    act(() => select("ns/api"));
    expect(r.getByTestId("inspector").textContent).toBe("ns/api");
    seen.length = 0;
    r.rerender(<Lens cluster="b" />);
    expect(seen.every((x) => x.key === null)).toBe(true); // never "ns/api" on b, not even once
    expect(select).toBe(first); // stable across the cluster change
    act(() => select("ns/web"));
    expect(r.getByTestId("inspector").textContent).toBe("ns/web");
    r.rerender(<Lens cluster="a" />);
    expect(r.getByTestId("inspector").textContent).toBe(""); // b's key never re-targets to a
  });

  it("null clears", () => {
    const r = render(<Lens cluster="a" />);
    act(() => select("x"));
    act(() => select(null));
    expect(r.getByTestId("inspector").textContent).toBe("");
  });
});
