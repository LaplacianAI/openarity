import { readFileSync } from "node:fs";
import { join } from "node:path";
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Mark } from "./mark";

// The mark is drawn inline, not shown with <img>: the dashboard's theme is a
// class on <html>, and an SVG in an <img> is a separate document that only
// sees the browser's preference. So this is a copy of assets/logo.svg's
// geometry, and like the favicon, a copy is checked against its source.
const source = readFileSync(join(process.cwd(), "../../assets/logo.svg"), "utf8");

function parts(svg: string): string[] {
  const mask = svg.match(/<mask[^>]*>([\s\S]*?)<\/mask>/)?.[1] ?? "";
  return [...mask.matchAll(/<(rect|path|circle)\b([^>]*?)\/?>/g)].map(([, tag, attrs = ""]) => {
    const pairs = [...attrs.matchAll(/([a-z-]+)="([^"]*)"/g)].map(([, k, v]) => `${k}=${v}`).sort();
    return `${tag} ${pairs.join(" ")}`;
  });
}

describe("Mark", () => {
  it("draws assets/logo.svg's parts, attribute for attribute", () => {
    const { container } = render(<Mark />);
    const drawn = container.innerHTML.replace(/<(rect|path|circle)([^>]*)><\/\1>/g, "<$1$2/>");

    expect(parts(source).length).toBe(5);
    expect(parts(drawn)).toEqual(parts(source));
  });

  it("gives every copy its own mask, so a second mark does not borrow the first's", () => {
    const { container } = render(
      <>
        <Mark />
        <Mark />
      </>,
    );
    const ids = [...container.querySelectorAll("mask")].map((m) => m.id);

    expect(new Set(ids).size).toBe(2);
    for (const rect of container.querySelectorAll("svg > rect")) {
      expect(ids).toContain(rect.getAttribute("mask")?.match(/url\(#(.+)\)/)?.[1]);
    }
  });

  it("takes the page's accent, so it follows the dashboard's theme toggle", () => {
    const { container } = render(<Mark />);

    expect(container.querySelector("svg > rect")?.getAttribute("fill")).toBe("currentColor");
    expect(container.querySelector("svg")?.getAttribute("class")).toContain("text-primary");
  });

  it("is decoration beside the name, and a named image on its own", () => {
    const beside = render(<Mark />).container.querySelector("svg");
    expect(beside?.getAttribute("aria-hidden")).toBe("true");

    const alone = render(<Mark label="openarity" />).container.querySelector("svg");
    expect(alone?.getAttribute("role")).toBe("img");
    expect(alone?.getAttribute("aria-label")).toBe("openarity");
    expect(alone?.hasAttribute("aria-hidden")).toBe(false);
  });
});
