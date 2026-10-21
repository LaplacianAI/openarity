import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Anchor, Markdown, safeHref, urlTransform } from "./markdown";

describe("safeHref", () => {
  it("keeps http and https", () => {
    expect(safeHref("https://example.com/a?b=1")).toBe("https://example.com/a?b=1");
    expect(safeHref("http://example.com")).toBe("http://example.com/");
  });

  it("refuses every other scheme, and anything relative", () => {
    for (const url of [
      "javascript:alert(1)",
      " JavaScript:alert(1)",
      "data:text/html,<script>x</script>",
      "vbscript:x",
      "file:///etc/passwd",
      "mailto:a@b.c",
      "/ui/teams",
      "//evil.example",
      "",
      undefined,
      null,
    ]) {
      expect(safeHref(url)).toBeNull();
    }
  });
});

// Two guards, each tested on its own, so removing either one fails a test
// even while the other still holds.
describe("the link guards", () => {
  it("blank every URL react-markdown would emit that is not http or https", () => {
    expect(urlTransform("javascript:alert(1)")).toBe("");
    expect(urlTransform("data:text/html,x")).toBe("");
    expect(urlTransform("https://e.x/")).toBe("https://e.x/");
  });

  it("will not draw a link to anywhere else, whatever reaches it", () => {
    // Built at run time: the point is to hand the component what a model might.
    const script = ["javascript", "alert(1)"].join(":");
    const { container } = render(
      <>
        <Anchor href={script}>bad</Anchor>
        <Anchor href="">empty</Anchor>
        <Anchor href="https://e.x/">good</Anchor>
      </>,
    );
    expect(container.querySelectorAll("a")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "good" })).toHaveAttribute("target", "_blank");
  });
});

describe("Markdown", () => {
  it("draws headings, lists, code and tables", () => {
    const { container } = render(
      <Markdown>
        {"# Title\n\n- one\n- two\n\n```\nblock\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |"}
      </Markdown>,
    );
    expect(screen.getByRole("heading", { name: "Title" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(container.querySelector("pre code")).toHaveTextContent("block");
    expect(screen.getByRole("table")).toHaveTextContent("ab12");
  });

  it("keeps every heading level below the page's own, and draws quotes and rules", () => {
    const { container } = render(
      <Markdown>
        {
          "## two\n\n### three\n\n#### four\n\n##### five\n\n###### six\n\n> quoted\n\n---\n\n1. first"
        }
      </Markdown>,
    );
    expect(container.querySelector("h1, h2")).toBeNull();
    expect(screen.getAllByRole("heading").map((h) => h.tagName)).toEqual([
      "H3",
      "H4",
      "H5",
      "H6",
      "H6",
    ]);
    expect(container.querySelector("blockquote")).toHaveTextContent("quoted");
    expect(container.querySelector("hr")).not.toBeNull();
    expect(container.querySelector("ol li")).toHaveTextContent("first");
  });

  it("drops raw HTML instead of rendering it", () => {
    const { container } = render(
      <Markdown>{'hi <b onclick="x()">there</b>\n\n<iframe src="https://e.x"></iframe>'}</Markdown>,
    );
    expect(container.querySelector("b, iframe, [onclick]")).toBeNull();
    expect(container).toHaveTextContent("hi there");
  });

  it("shows a link it will not follow as its text, and an image as a link", () => {
    const { container } = render(
      <Markdown>
        {
          "[x](data:text/html,hi) ![alt text](https://e.x/i.png) ![](javascript:x) <https://e.x/auto>"
        }
      </Markdown>,
    );
    expect(screen.queryByRole("link", { name: "x" })).toBeNull();
    expect(screen.getByText("x")).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByRole("link", { name: "alt text" })).toHaveAttribute(
      "href",
      "https://e.x/i.png",
    );
    expect(screen.getByText("image")).not.toHaveAttribute("href");
    expect(screen.getByRole("link", { name: "https://e.x/auto" })).toHaveAttribute(
      "rel",
      "noopener noreferrer nofollow",
    );
  });
});
