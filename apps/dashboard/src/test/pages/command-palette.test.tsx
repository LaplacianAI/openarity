import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { brain, showAt, team } from "@/test/brain";

const { Route: UiRoute } = await import("@/routes/ui");

const real = window.matchMedia;
afterEach(() => {
  window.matchMedia = real;
});

function open() {
  window.matchMedia = ((query: string) => ({
    matches: true,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  brain({
    ...team([]),
    "GET /whoami": { body: { subject: "ann" } },
    "GET /teams/{id}/channels": { body: { items: [] } },
    "GET /teams/{id}/agents": {
      body: { items: [{ name: "log-reader", description: "Reads logs" }, { name: "ops-lead" }] },
    },
    "GET /teams/{id}/skills": { body: { items: [{ name: "incident-postmortem" }] } },
    "GET /teams/{id}/sessions": {
      body: { items: [{ id: "s-1", provider_ref: "eng-support:t-1001", kind: "thread" }] },
    },
  });
  return showAt("/ui", [["/ui", UiRoute]]);
}

describe("the command palette", () => {
  it("opens on ⌘K and finds a screen or an agent by name", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });

    await userEvent.keyboard("{Meta>}k{/Meta}");
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), "log");

    const options = await within(palette).findAllByRole("option");
    expect(options.map((o) => o.textContent)).toContain("log-readerAgent");
    expect(options[0]).toHaveAttribute("aria-selected", "true");

    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/agents"));
    expect(screen.queryByRole("dialog", { name: "Go to" })).toBeNull();
  });

  it("moves through what it found with the arrow keys, and opens a session's own page", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });

    await userEvent.click(screen.getByRole("button", { name: /Search/ }));
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), "s");
    await within(palette).findByRole("option", { name: /eng-support:t-1001/ });

    const target = within(palette).getByRole("option", { name: /eng-support:t-1001/ });
    while (target.getAttribute("aria-selected") !== "true") await userEvent.keyboard("{ArrowDown}");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-1"));
  });

  it("closes on Escape without going anywhere", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });
    await userEvent.keyboard("{Control>}k{/Control}");
    await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Go to" })).toBeNull());
    expect(router.state.location.pathname).toBe("/ui");
  });
});
