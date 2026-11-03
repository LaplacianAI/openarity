import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CodeBlock, JsonView } from "@/components/code";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { EmptyState, ErrorState, ForbiddenState } from "@/components/states";
import { Count, StatusPill } from "@/components/status";
import { TooltipProvider } from "@/components/ui/tooltip";

function mount(ui: ReactNode) {
  return render(<TooltipProvider>{ui}</TooltipProvider>);
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("a status", () => {
  // The colour is never the only signal: the word is always there, and a
  // run's states carry an icon a scanning eye can find.
  it("says its word, with an icon for a run's state and a dot for a session's", () => {
    const { container } = mount(
      <>
        <StatusPill status="failed" />
        <StatusPill status="open" />
      </>,
    );
    const failed = container.querySelector('[data-status="failed"]');
    const open = container.querySelector('[data-status="open"]');
    expect(failed).toHaveTextContent("failed");
    expect(failed?.querySelector("svg")).not.toBeNull();
    expect(open).toHaveTextContent("open");
    expect(open?.querySelector("svg")).toBeNull();
  });

  it("draws a state it does not know as unknown rather than guessing a tone", () => {
    const { container } = mount(<StatusPill status="archived" />);
    const s = container.querySelector('[data-status="archived"]');
    expect(s).toHaveTextContent("archived");
    expect(s?.querySelector("svg")?.getAttribute("class")).toContain("text-muted-foreground");
  });

  it("running spins; nothing else does", () => {
    const { container } = mount(
      <>
        <StatusPill status="running" />
        <StatusPill status="queued" />
      </>,
    );
    expect(container.querySelector('[data-status="running"] svg')?.getAttribute("class")).toContain(
      "animate-spin",
    );
    expect(
      container.querySelector('[data-status="queued"] svg')?.getAttribute("class"),
    ).not.toContain("animate-spin");
  });
});

describe("a count", () => {
  it("reads as a number of people waiting, not a bare digit", () => {
    mount(
      <a href="/ui/approvals">
        Approvals
        <Count n="3" />
      </a>,
    );
    expect(screen.getByRole("link")).toHaveTextContent("Approvals, 3 waiting");
  });
});

describe("facts", () => {
  it("pairs each label with its value in a description list", () => {
    mount(
      <KeyValues>
        <KeyValue label="Model">gemini</KeyValue>
        <KeyValue label="Steps">2</KeyValue>
      </KeyValues>,
    );
    const terms = screen.getAllByRole("term").map((t) => t.textContent);
    const values = screen.getAllByRole("definition").map((d) => d.textContent);
    expect(terms).toEqual(["Model", "Steps"]);
    expect(values).toEqual(["gemini", "2"]);
  });

  it("copies the whole id however much of it is shown", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const id = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d";
    mount(<CopyId value={id} label="run id" />);

    await user.click(screen.getByRole("button", { name: `Copy the run id ${id}` }));
    expect(writeText).toHaveBeenCalledWith(id);
  });

  it("says a time the way a person would, and keeps it current", () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date("2026-11-03T10:00:00Z"));
    mount(<Time iso="2026-11-03T09:56:00Z" />);

    const time = screen.getByText("4m ago");
    expect(time.tagName).toBe("TIME");
    expect(time).toHaveAttribute("dateTime", "2026-11-03T09:56:00Z");

    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(time).toHaveTextContent("5m ago");
  });
});

describe("a code block", () => {
  const long = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join("\n");

  it("folds past a dozen lines and opens on request", async () => {
    const user = userEvent.setup();
    const { container } = mount(<CodeBlock code={long} label="output" />);
    const block = container.firstElementChild as HTMLElement;
    expect(block).toHaveAttribute("data-folded");

    await user.click(screen.getByRole("button", { name: "Show all 30 lines" }));
    expect(block).not.toHaveAttribute("data-folded");
    expect(screen.getByRole("button", { name: "Show less" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("leaves a short block whole, with no fold button", () => {
    mount(<CodeBlock code="ok" />);
    expect(screen.queryByRole("button", { name: /Show all/ })).toBeNull();
  });

  it("copies all of it, folded or not, and says so", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    mount(<CodeBlock code={long} label="output" />);
    await user.click(screen.getByRole("button", { name: "Copy output" }));
    expect(writeText).toHaveBeenCalledWith(long);
    expect(await screen.findByRole("status")).toHaveTextContent("Copied");
  });

  it("indents JSON, and shows what does not parse as it came", () => {
    const { container, rerender } = mount(<JsonView value='{"a":1}' />);
    expect(container.querySelector("pre")?.textContent).toBe('{\n  "a": 1\n}');
    rerender(
      <TooltipProvider>
        <JsonView value="not json {" />
      </TooltipProvider>,
    );
    expect(container.querySelector("pre")?.textContent).toBe("not json {");
  });
});

describe("the states a block can be in", () => {
  it("empty: teaches the next step with one action", () => {
    mount(
      <EmptyState
        title="No agents"
        body="Define one."
        action={<button type="button">Define agent</button>}
      />,
    );
    expect(screen.getByRole("heading", { name: "No agents" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Define agent" })).toBeInTheDocument();
  });

  it("failed: says what and why, and retries", async () => {
    const user = userEvent.setup();
    const retry = vi.fn();
    mount(<ErrorState what="the agents" error={new Error("Gateway timeout.")} onRetry={retry} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load the agents.");
    expect(alert).toHaveTextContent("Gateway timeout.");
    await user.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("failed while retrying: the button is busy and takes no second press", () => {
    mount(<ErrorState what="the agents" error={null} onRetry={() => {}} retrying />);
    const button = screen.getByRole("button", { name: "Try again" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
  });

  it("forbidden: names the permission, so the reader knows whom to ask", () => {
    mount(<ForbiddenState what="this team's sessions" permission="session:read" />);
    expect(screen.getByText("You can't see this team's sessions.")).toBeInTheDocument();
    expect(screen.getByText("session:read")).toBeInTheDocument();
  });
});
