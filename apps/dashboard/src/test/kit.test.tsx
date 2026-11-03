import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CodeBlock, JsonView } from "@/components/code";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { EmptyState, ErrorState, ForbiddenState } from "@/components/states";
import { Count, StatusPill } from "@/components/status";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { SwitchField } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
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

describe("a field", () => {
  it("joins its hint and its error to the control, and marks it invalid", () => {
    mount(
      <Field label="Name" htmlFor="f" hint="Lowercase." error="That name is taken.">
        <Input id="f" />
      </Field>,
    );
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).toHaveAccessibleDescription("Lowercase. That name is taken.");
    expect(input).toHaveAttribute("aria-invalid", "true");
  });

  it("is valid and undescribed when there is nothing to say", () => {
    mount(
      <Field label="Name" htmlFor="f">
        <Input id="f" />
      </Field>,
    );
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).not.toHaveAttribute("aria-describedby");
    expect(input).not.toHaveAttribute("aria-invalid");
  });

  // A comment or a stray space beside the control must not break the wiring.
  it("finds its control among children that are not elements", () => {
    mount(
      <Field label="Secret" htmlFor="f" hint="Sent once.">
        {null} <Input id="f" />
      </Field>,
    );
    expect(screen.getByLabelText("Secret")).toHaveAccessibleDescription("Sent once.");
  });

  it("shows a limit beside the label, not in a tooltip", () => {
    mount(
      <Field label="Description" htmlFor="f" limit="58 of 280">
        <Input id="f" />
      </Field>,
    );
    expect(screen.getByText("58 of 280")).toBeVisible();
  });
});

describe("a switch", () => {
  it("is named by its label, described by what it does, and toggles from the whole row", async () => {
    const user = userEvent.setup();
    mount(<SwitchField label="Web" description="Search the web and fetch pages." />);
    const sw = screen.getByRole("switch", { name: "Web" });
    expect(sw).toHaveAccessibleDescription("Search the web and fetch pages.");
    expect(sw).not.toBeChecked();
    await user.click(screen.getByText("Search the web and fetch pages."));
    expect(sw).toBeChecked();
  });
});

describe("a table", () => {
  function rows(onOpen: (n: string) => void) {
    return mount(
      <Table>
        <TableBody>
          {["a", "b", "c"].map((n) => (
            <TableRow key={n} onOpen={() => onOpen(n)}>
              <TableCell>{n}</TableCell>
              <TableCell>
                <button type="button">Edit {n}</button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>,
    );
  }

  it("is one tab stop, moved through with the arrows and opened with Enter", async () => {
    const user = userEvent.setup();
    const opened = vi.fn();
    rows(opened);
    const [a, b, c] = screen.getAllByRole("row");

    await user.tab();
    expect(a).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(b).toHaveFocus();
    await user.keyboard("{End}");
    expect(c).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(opened).toHaveBeenCalledWith("c");

    // Only the row last focused stays in the tab order.
    expect(screen.getAllByRole("row").map((r) => r.tabIndex)).toEqual([-1, -1, 0]);
  });

  it("opens on a click, but not on a click meant for a control inside it", async () => {
    const user = userEvent.setup();
    const opened = vi.fn();
    rows(opened);
    await user.click(screen.getByRole("button", { name: "Edit a" }));
    expect(opened).not.toHaveBeenCalled();
    await user.click(screen.getByText("b"));
    expect(opened).toHaveBeenCalledWith("b");
  });
});

describe("a sheet", () => {
  it("opens as a dialog named by its title, and closes", async () => {
    const user = userEvent.setup();
    mount(
      <Sheet>
        <SheetTrigger>Edit</SheetTrigger>
        <SheetContent aria-describedby={undefined}>
          <SheetTitle>Edit log-reader</SheetTitle>
        </SheetContent>
      </Sheet>,
    );
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const sheet = await screen.findByRole("dialog", { name: "Edit log-reader" });
    await user.click(within(sheet).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
