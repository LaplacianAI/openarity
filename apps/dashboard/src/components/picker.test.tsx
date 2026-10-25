import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { type Picked, Picker } from "./picker";

const SKILLS: Picked[] = [
  "invoice-reconciliation",
  "invoice-dunning",
  "inventory-count",
  "pdf",
  "payroll",
].map((name, i) => ({ id: `s${i}`, name }));

/** A brain that searches names anywhere in them, ignoring case, a page at a time. */
function searcher(all: Picked[] = SKILLS, page = 20) {
  return vi.fn(async (q: string) => {
    const hits = all.filter((s) => s.name.toLowerCase().includes(q.trim().toLowerCase()));
    return { items: hits.slice(0, page), more: hits.length > page };
  });
}

function Harness({
  initial = [],
  search = searcher(),
  exclude,
  extra,
}: {
  initial?: Picked[];
  search?: ReturnType<typeof searcher>;
  exclude?: string[];
  extra?: (p: Picked) => React.ReactNode;
}) {
  const [value, setValue] = useState<Picked[]>(initial);
  return (
    <>
      <Picker
        label="Skills"
        noun="skill"
        value={value}
        onChange={setValue}
        search={search}
        searchKey={["test"]}
        exclude={exclude}
        extra={extra}
      />
      <output data-testid="chosen">{value.map((v) => v.name).join(",")}</output>
    </>
  );
}

function show(props: Parameters<typeof Harness>[0] = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness {...props} />
    </QueryClientProvider>,
  );
}

const chosen = () => screen.getByTestId("chosen").textContent;

async function openPicker() {
  await userEvent.click(screen.getByRole("button", { name: /add skill/i }));
  return screen.findByRole("combobox", { name: /search skills/i });
}

describe("the picker", () => {
  it("shows what is chosen as chips, each removable on its own", async () => {
    show({ initial: [SKILLS[0] as Picked, SKILLS[3] as Picked] });

    const chips = screen.getByRole("list", { name: "Skills" });
    expect(
      within(chips)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual([expect.stringContaining("invoice-reconciliation"), expect.stringContaining("pdf")]);
    await userEvent.click(screen.getByRole("button", { name: "Remove pdf" }));
    expect(chosen()).toBe("invoice-reconciliation");
  });

  it("opens on a search with the focus in it, and asks the brain as you type", async () => {
    const search = searcher();
    show({ search });

    const box = await openPicker();
    expect(box).toHaveFocus();
    await userEvent.type(box, "INVOICE");

    const list = await screen.findByRole("listbox");
    await waitFor(() =>
      expect(
        within(list)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["invoice-reconciliation", "invoice-dunning"]),
    );
    expect(search).toHaveBeenLastCalledWith("INVOICE");
    expect(list).toHaveAttribute("aria-multiselectable", "true");
  });

  it("adds and removes by clicking, and stays open to pick more", async () => {
    show();

    const box = await openPicker();
    await userEvent.click(await screen.findByRole("option", { name: "pdf" }));
    await userEvent.click(screen.getByRole("option", { name: "payroll" }));
    expect(chosen()).toBe("pdf,payroll");
    expect(screen.getByRole("option", { name: "pdf" })).toHaveAttribute("aria-selected", "true");
    expect(box).toBeInTheDocument();

    await userEvent.click(screen.getByRole("option", { name: "pdf" }));
    expect(chosen()).toBe("payroll");
  });

  it("is driven from the keyboard: arrows move, Enter toggles, Escape closes", async () => {
    show();

    const box = await openPicker();
    await userEvent.type(box, "inv");
    // Wait for the search itself: a new search starts again at its first match.
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    // The first match starts active; one down is the second.
    await userEvent.keyboard("{ArrowDown}");
    const active = box.getAttribute("aria-activedescendant");
    expect(active && document.getElementById(active)?.textContent).toBe("invoice-dunning");
    await userEvent.keyboard("{Enter}");
    expect(chosen()).toBe("invoice-dunning");

    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
    expect(screen.getByRole("button", { name: /add skill/i })).toHaveFocus();
  });

  it("takes the last chip off with Backspace in an empty search", async () => {
    show({ initial: [SKILLS[0] as Picked, SKILLS[3] as Picked] });

    await openPicker();
    await userEvent.keyboard("{Backspace}");
    expect(chosen()).toBe("invoice-reconciliation");
  });

  it("says when more match than it shows", async () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ id: `m${i}`, name: `skill-${i}` }));
    show({ search: searcher(many, 20) });

    await openPicker();
    expect(await screen.findByText(/more match — keep typing/i)).toBeInTheDocument();
    expect(screen.getAllByRole("option")).toHaveLength(20);
  });

  it("says so when nothing matches", async () => {
    show();

    const box = await openPicker();
    await userEvent.type(box, "zzz");
    expect(await screen.findByText(/no skill matches/i)).toBeInTheDocument();
  });

  it("never offers what is excluded", async () => {
    show({ exclude: ["s3"] });

    await openPicker();
    await screen.findByRole("option", { name: "payroll" });
    expect(screen.queryByRole("option", { name: "pdf" })).toBeNull();
  });

  // Names come from people and from imported skills; they are text.
  it("shows a name as text, never as markup", async () => {
    const markup = '<img src=x onerror="alert(1)">';
    show({ initial: [{ id: "x", name: markup }], search: searcher([{ id: "x", name: markup }]) });

    await openPicker();
    await screen.findByRole("option");
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getAllByText(markup).length).toBeGreaterThanOrEqual(2);
  });

  it("lets a chip carry more, such as a server's tool limit", () => {
    show({
      initial: [SKILLS[0] as Picked],
      extra: (p) => <span data-testid={`extra-${p.id}`}>only some</span>,
    });
    expect(screen.getByTestId("extra-s0")).toHaveTextContent("only some");
  });
});
