import { describe, expect, it } from "vitest";

import type { Member } from "@/api/types";

import { suggestMember } from "./senders";

const m = (user_id: string, subject: string, email?: string) =>
  ({ user_id, subject, role: "member", ...(email ? { email } : {}) }) as Member;

const erik = m("u1", "erik", "erik.lund@halden.co");
const erika = m("u2", "erika", "erika.berg@halden.co");
const lund = m("u3", "lund-ops", "ops@halden.co");

describe("suggestMember", () => {
  it("finds the one member whose address holds every word of the name", () => {
    expect(suggestMember("Erik Lund", [erik, erika, lund])).toBe("u1");
    expect(suggestMember("erika", [erik, erika])).toBe("u2");
  });

  it("matches words whole, so Erik is not Erika", () => {
    expect(suggestMember("Erik", [erika])).toBeNull();
  });

  // Two that fit is a guess, and a wrong one binds a stranger to someone else.
  it("suggests no one when more than one member fits", () => {
    expect(suggestMember("Lund", [erik, lund])).toBeNull();
  });

  it("ignores the domain every member shares", () => {
    // One member alone, so "more than one fits" cannot be what says no.
    expect(suggestMember("Halden", [erik])).toBeNull();
  });

  it("suggests no one for a name with no words, or no members", () => {
    expect(suggestMember("(—)", [erik])).toBeNull();
    expect(suggestMember("", [erik])).toBeNull();
    expect(suggestMember("Erik Lund", [])).toBeNull();
  });
});
