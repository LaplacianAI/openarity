import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { show } from "@/test/brain";

const { default: Gallery } = await import("@/gallery");

describe("the states gallery", () => {
  // Each section is where its component's states are reviewed; one that
  // stops rendering is a state the redesign is no longer judged on.
  it("shows every shared component", () => {
    show(Gallery);

    for (const name of [
      "Buttons",
      "Fields",
      "Page header",
      "Empty state",
      "Load more",
      "Marks",
      "Read-only note",
      "Loading",
      "Markdown",
      "Dialog",
    ]) {
      expect(screen.getByRole("heading", { level: 2, name })).toBeInTheDocument();
    }
  });

  it("shows each button variant enabled and disabled", () => {
    show(Gallery);

    const buttons = screen.getByRole("region", { name: "Buttons" });
    for (const variant of ["default", "destructive", "outline", "secondary", "ghost", "link"]) {
      const pair = within(buttons).getAllByRole("button", { name: variant });
      expect(pair).toHaveLength(2);
      expect(pair[1]).toBeDisabled();
    }
  });

  it("opens its dialog on demand rather than over the page", async () => {
    show(Gallery);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open the dialog" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("needs no brain", () => {
    const calls: unknown[] = [];
    const real = globalThis.fetch;
    globalThis.fetch = ((...args: unknown[]) => {
      calls.push(args);
      return real(...(args as Parameters<typeof fetch>));
    }) as typeof fetch;
    try {
      show(Gallery);
      expect(calls).toEqual([]);
    } finally {
      globalThis.fetch = real;
    }
  });
});
