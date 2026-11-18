import { describe, expect, it } from "vitest";

import type { MCPServer } from "@/api/types";
import {
  draftOf,
  EMPTY_SERVER,
  editRequest,
  egressOf,
  missesRegistry,
  serverRequest,
} from "@/lib/server-draft";

const mail = {
  ...EMPTY_SERVER,
  name: "mail",
  transport: "command" as const,
  command: "npx\n-y\nmcp-mail-server",
};

const stored: MCPServer = {
  id: "s1",
  team_id: "t1",
  name: "mail",
  command: ["npx", "-y", "mcp-mail-server"],
  env: { IMAP_PASS: "teams/t1/mcp/mail#pass" },
  vars: { IMAP_HOST: "imap.gmail.com" },
  egress: [{ host: "imap.gmail.com", port: 993 }],
  auth_kind: "static",
  bare: false,
  created_at: "2026-11-18T09:00:00Z",
  updated_at: "2026-11-18T09:00:00Z",
};

describe("a command server's settings and network", () => {
  it("sends them, one per line", () => {
    expect(
      serverRequest({
        ...mail,
        vars: "IMAP_HOST=imap.gmail.com\n\n IMAP_PORT = 993 ",
        egress: "imap.gmail.com:993\nregistry.npmjs.org:443",
      }),
    ).toMatchObject({
      vars: { IMAP_HOST: "imap.gmail.com", IMAP_PORT: "993" },
      egress: [
        { host: "imap.gmail.com", port: 993 },
        { host: "registry.npmjs.org", port: 443 },
      ],
    });
  });

  it("sends neither when both are empty", () => {
    const body = serverRequest(mail);
    expect(body).not.toHaveProperty("vars");
    expect(body).not.toHaveProperty("egress");
  });

  it.each([
    ["a setting with no =", { vars: "IMAP_HOST" }, /NAME=value/],
    ["a name in both lists", { env: "A=teams/t/mcp/x#k", vars: "A=1" }, /both/],
    ["a place with no port", { egress: "imap.gmail.com" }, /host:port/],
    ["a port past 65535", { egress: "imap.gmail.com:70000" }, /host:port/],
    ["a named port", { egress: "imap.gmail.com:imaps" }, /host:port/],
  ])("refuses %s", (_, patch, want) => {
    expect(serverRequest({ ...mail, ...patch })).toMatch(want);
  });

  it("reads an IPv6 literal in brackets, and writes it back the same way", () => {
    expect(egressOf("[2001:db8::1]:443")).toEqual({ host: "2001:db8::1", port: 443 });
    const d = draftOf({ ...stored, egress: [{ host: "2001:db8::1", port: 443 }] });
    expect(d.egress).toBe("[2001:db8::1]:443");
  });

  it("round-trips through the form unchanged", () => {
    const d = draftOf(stored);
    expect(editRequest(stored, d)).toMatchObject({ vars: stored.vars, egress: stored.egress });
  });

  // The form shows both, so emptying one is a choice, not an omission.
  it("clears what the form empties on an edit", () => {
    const body = editRequest(stored, { ...draftOf(stored), vars: "", egress: "" });
    expect(body).not.toHaveProperty("vars");
    expect(body).not.toHaveProperty("egress");
  });
});

describe("the npm registry hint", () => {
  it("shows for npx without the registry", () => {
    expect(missesRegistry(mail)).toBe(true);
  });

  it("goes once the registry is listed, in any case", () => {
    expect(missesRegistry({ ...mail, egress: "Registry.npmjs.org:443" })).toBe(false);
  });

  it("does not show for a command that is not npx", () => {
    expect(missesRegistry({ ...mail, command: "uvx\nmcp-server-time" })).toBe(false);
  });
});
