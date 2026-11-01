import { describe, expect, it } from "vitest";
import { isActive } from "./nav";

describe("isActive", () => {
  // The brain serves the dashboard at /ui/, so that is where a browser lands,
  // and the router keeps the slash it was given.
  it("holds Overview at /ui and at /ui/", () => {
    expect(isActive("/ui", { to: "/ui", exact: true })).toBe(true);
    expect(isActive("/ui/", { to: "/ui", exact: true })).toBe(true);
  });

  it("does not hold Overview on any other page", () => {
    expect(isActive("/ui/teams", { to: "/ui", exact: true })).toBe(false);
  });

  it("holds a section on its pages, with or without the slash", () => {
    expect(isActive("/ui/agents", { to: "/ui/agents", exact: false })).toBe(true);
    expect(isActive("/ui/agents/", { to: "/ui/agents", exact: false })).toBe(true);
    expect(isActive("/ui/agents/billing", { to: "/ui/agents", exact: false })).toBe(true);
  });

  it("does not hold a section for a page that only starts with its name", () => {
    expect(isActive("/ui/agents-archive", { to: "/ui/agents", exact: false })).toBe(false);
  });
});
