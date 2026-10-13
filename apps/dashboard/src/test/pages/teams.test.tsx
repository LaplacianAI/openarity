import { screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, show, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.teams.index");
const Teams = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const people = {
  "GET /teams/{id}/members": {
    body: { items: [{ user_id: "u1", subject: "alice", role: "admin" }] },
  },
  "GET /users": { body: { items: [] } },
};

// Adding and removing people is membership:write in the team. A member
// without it still sees who is in the team.
it("offers no membership controls without membership:write", async () => {
  brain({ ...team(["agent:write"]), ...people });
  show(Teams);

  expect(await screen.findByText("alice")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^remove$/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^add$/i })).not.toBeInTheDocument();
});

it("offers them with membership:write", async () => {
  brain({ ...team(["membership:write"]), ...people });
  show(Teams);

  expect(await screen.findByRole("button", { name: /^remove$/i })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /^add$/i })).toBeInTheDocument();
});
