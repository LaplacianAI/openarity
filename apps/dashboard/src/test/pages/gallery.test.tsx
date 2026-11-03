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
      "Run",
    ]) {
      expect(screen.getByRole("heading", { level: 2, name })).toBeInTheDocument();
    }
  });

  // Four variants and no more; each shown enabled, disabled and busy.
  it("shows each button variant enabled, disabled and loading", () => {
    show(Gallery);

    const buttons = screen.getByRole("region", { name: "Buttons" });
    for (const variant of ["default", "outline", "ghost", "destructive"]) {
      const [enabled, disabled, loading] = within(buttons).getAllByRole("button", {
        name: variant,
      });
      expect(enabled).toBeEnabled();
      expect(disabled).toBeDisabled();
      // Busy keeps its name for a screen reader, and takes no second press.
      expect(loading).toHaveAttribute("aria-busy", "true");
      expect(loading).toBeDisabled();
    }
  });

  it("opens its dialog on demand rather than over the page", async () => {
    show(Gallery);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open the dialog" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  // The run's states that no seeded team holds at will: still running, with
  // a call answered and folded, one failed and open, and one waiting.
  it("shows a running run with each state of a call", () => {
    show(Gallery);

    const run = screen.getByRole("region", { name: "Run" });
    expect(within(run).getByText("Live")).toBeInTheDocument();
    expect(
      within(run).getByRole("button", { name: "Cancel the run of ops-lead" }),
    ).toBeInTheDocument();
    const calls = Array.from(run.querySelectorAll<HTMLElement>("[data-call]"));
    expect(calls.map((c) => c.dataset.state)).toEqual(["done", "failed", "pending"]);
    expect(calls[0]?.querySelector("details")).not.toHaveAttribute("open");
    expect(calls[1]?.querySelector("details")).toHaveAttribute("open");
  });

  // What a screen is for is read on request, not above every visit's work.
  it("keeps a page's explanation behind What is this?", async () => {
    show(Gallery);
    const region = screen.getByRole("region", { name: "Page header" });
    expect(within(region).queryByText(/long enough to wrap twice/)).toBeNull();
    await userEvent.click(within(region).getByRole("button", { name: "What is Agents?" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(/long enough to wrap twice/);
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
