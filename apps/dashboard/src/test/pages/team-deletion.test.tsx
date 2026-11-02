import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, pageOf, showAt, TEAM, team } from "@/test/brain";

const { Route: SettingsRoute } = await import("@/routes/ui.settings");
const { Route: TeamsRoute } = await import("@/routes/ui.teams.index");
const { Route: DeletedRoute } = await import("@/routes/ui.teams.deleted");
await pageOf(SettingsRoute);
await pageOf(TeamsRoute);
await pageOf(DeletedRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const SETTINGS: TeamSettings = {
  model: null,
  search: { provider: "none", url: null, key_set: false },
  learning: false,
  compaction_messages: 100,
  hub: null,
  updated_at: null,
};

const GONE = {
  id: TEAM,
  name: "platform",
  deleted_at: "2026-11-01T09:30:00Z",
  purge_at: "2026-11-08T09:30:00Z",
};

function at(path: string) {
  return showAt(path, [
    ["/ui/settings", SettingsRoute],
    ["/ui/teams/", TeamsRoute],
    ["/ui/teams/deleted", DeletedRoute],
  ]);
}

function settingsBrain(superAdmin: boolean, extra = {}) {
  return brain({
    ...team(["team:settings"], superAdmin),
    "GET /teams/{id}/settings": { body: SETTINGS },
    "GET /teams/{id}/members": { body: { items: [] } },
    "GET /users": { body: { items: [] } },
    ...extra,
  });
}

async function openDialog() {
  await userEvent.click(await screen.findByRole("button", { name: "Delete team" }));
  return screen.findByRole("dialog");
}

describe("the danger zone", () => {
  it("is not offered to anyone but a super admin", async () => {
    settingsBrain(false);
    at("/ui/settings");

    expect(await screen.findByLabelText(/^provider$/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete team" })).not.toBeInTheDocument();
  });

  it("deletes only once the exact name is typed", async () => {
    const seen = settingsBrain(true);
    at("/ui/settings");

    const dialog = await openDialog();
    const confirm = within(dialog).getByRole("button", { name: /delete platform/i });
    const field = within(dialog).getByLabelText(/type the team's name/i);

    for (const typed of ["platfor", "Platform", "platform "]) {
      await userEvent.clear(field);
      await userEvent.type(field, typed);
      expect(confirm).toBeDisabled();
    }
    await userEvent.clear(field);
    await userEvent.type(field, "platform");
    expect(confirm).toBeEnabled();
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);
  });

  // The dashboard cannot know the brain's purge window, so it gives the
  // default as a usual case, in words; the brain's own date comes on delete.
  it("says when a team is purged by default", async () => {
    settingsBrain(true);
    at("/ui/settings");

    const dialog = await openDialog();
    const week = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
    expect(dialog).toHaveTextContent(week);
    expect(dialog).toHaveTextContent(/usually a week/);
    expect(dialog.textContent).not.toMatch(/[A-Z]+_[A-Z_]+/);
  });

  it("says why when the team has runs in progress", async () => {
    settingsBrain(true, {
      "DELETE /teams/{id}": { status: 409, text: "the team has runs in progress" },
    });
    at("/ui/settings");

    const dialog = await openDialog();
    await userEvent.type(within(dialog).getByLabelText(/type the team's name/i), "platform");
    await userEvent.click(within(dialog).getByRole("button", { name: /delete platform/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "This team has runs in progress",
    );
  });

  // Review focus 3: the deleted team was the one selected. Its selection is
  // forgotten and the page moves to Teams, rather than every screen 404ing.
  it("forgets the team and goes to Teams once it is deleted", async () => {
    window.sessionStorage.setItem("openarity.team", TEAM);
    const seen = settingsBrain(true, {
      "DELETE /teams/{id}": { status: 204 },
      "GET /teams/{id}": { body: GONE },
    });
    const router = at("/ui/settings");

    const dialog = await openDialog();
    await userEvent.type(within(dialog).getByLabelText(/type the team's name/i), "platform");
    await userEvent.click(within(dialog).getByRole("button", { name: /delete platform/i }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/teams"));
    expect(seen.filter((s) => s.method === "DELETE").map((s) => s.path)).toEqual([
      `/teams/${TEAM}`,
    ]);
    expect(window.sessionStorage.getItem("openarity.team")).toBeNull();
    // The list was asked again, so the deleted team is no longer offered.
    await waitFor(() =>
      expect(seen.filter((s) => s.method === "GET" && s.path === "/teams").length).toBeGreaterThan(
        1,
      ),
    );
  });
});

describe("the deleted teams view", () => {
  function deletedBrain(extra = {}) {
    return brain({
      ...team([], true),
      "GET /teams": (s) =>
        s.path.includes("deleted=true")
          ? { body: { items: [GONE] } }
          : { body: { items: [{ id: "live-1", name: "payments" }] } },
      ...extra,
    });
  }

  it("lists each deleted team with when it was deleted and when it is purged", async () => {
    const seen = deletedBrain();
    at("/ui/teams/deleted");

    const row = (await screen.findByText("platform")).closest("tr") as HTMLElement;
    expect(row).toHaveTextContent("2026-11-01");
    expect(row).toHaveTextContent("2026-11-08");
    expect(screen.getByRole("columnheader", { name: "Deleted on" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Erased on" })).toBeInTheDocument();
    expect(seen.some((s) => s.path.startsWith("/teams?") && s.path.includes("deleted=true"))).toBe(
      true,
    );
  });

  // The brain ignores ?deleted for anyone but a super admin and answers with
  // their live teams, which must not be shown as deleted ones.
  it("shows only teams that carry a deletion date", async () => {
    brain({
      ...team([], false),
      "GET /teams": { body: { items: [{ id: TEAM, name: "platform", role: "member" }] } },
    });
    at("/ui/teams/deleted");

    expect(await screen.findByText(/no deleted teams/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /restore/i })).not.toBeInTheDocument();
  });

  // The live list is the team picker's, mounted in the layout rather than on
  // this page, so what the page owes it is the invalidation.
  it("restores a team, takes its row away and invalidates the teams list", async () => {
    const invalidate = vi.spyOn(QueryClient.prototype, "invalidateQueries");
    const seen = deletedBrain({
      "POST /teams/{id}/restore": { body: { id: TEAM, name: "platform" } },
    });
    at("/ui/teams/deleted");

    await userEvent.click(await screen.findByRole("button", { name: "Restore platform" }));

    await waitFor(() => expect(screen.queryByText("platform")).not.toBeInTheDocument());
    expect(seen.filter((s) => s.method === "POST").map((s) => s.path)).toEqual([
      `/teams/${TEAM}/restore`,
    ]);
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["teams"] }));
    invalidate.mockRestore();
  });

  it("shows the brain's sentence when the name is taken", async () => {
    deletedBrain({
      "POST /teams/{id}/restore": { status: 409, text: "a team with that name already exists" },
    });
    at("/ui/teams/deleted");

    await userEvent.click(await screen.findByRole("button", { name: "Restore platform" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "a team with that name already exists",
    );
    expect(screen.getByText("platform")).toBeInTheDocument();
  });
});

describe("the nav", () => {
  it("offers Deleted teams under Teams, to super admins only", async () => {
    const { navFor } = await import("@/routes/ui");

    const labels = (superAdmin: boolean) => navFor(superAdmin).map((i) => i.label);
    expect(labels(false)).not.toContain("Deleted teams");
    const admin = labels(true);
    expect(admin[admin.indexOf("Teams") + 1]).toBe("Deleted teams");
  });
});

describe("whether the nav offers Deleted teams", () => {
  async function offers() {
    const { useOffersDeletedTeams } = await import("@/routes/ui");
    setToken("a-token");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    return renderHook(() => useOffersDeletedTeams(), { wrapper });
  }

  it("does for a super admin in a live team", async () => {
    brain(team([], true));
    const { result } = await offers();
    await waitFor(() => expect(result.current).toBe(true));
  });

  // Review: a super admin who deleted their only team has no team to ask
  // permissions in, and the toast still sends them to Deleted teams.
  it("does once the super admin's last team is deleted", async () => {
    brain({
      "GET /teams": (s) =>
        s.path.includes("deleted=true") ? { body: { items: [GONE] } } : { body: { items: [] } },
    });
    const { result } = await offers();
    await waitFor(() => expect(result.current).toBe(true));
  });

  it("does not for someone with no teams, whom the brain answers with none deleted", async () => {
    const seen = brain({ "GET /teams": { body: { items: [] } } });
    const { result } = await offers();
    await waitFor(() =>
      expect(seen.some((s) => s.path.includes("deleted=true"))).toBe(true),
    );
    expect(result.current).toBe(false);
  });

  it("asks nothing more of a member who has a team", async () => {
    const seen = brain(team([], false));
    const { result } = await offers();
    await waitFor(() => expect(seen.some((s) => s.path.includes("/permissions"))).toBe(true));
    expect(result.current).toBe(false);
    expect(seen.some((s) => s.path.includes("deleted=true"))).toBe(false);
  });
});
