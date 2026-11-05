import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { getToken, setToken } from "@/api/token";
import { brain, showAt } from "@/test/brain";

const { Route, whatToTry } = await import("@/routes/ui.callback");
await (Route.options.component as { preload?: () => Promise<void> }).preload?.();

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  setToken(null);
});

const q = (s: string) => new URLSearchParams(s);

describe("what a failed sign-in says to try", () => {
  it("tells someone the provider turned away who to ask", () => {
    const said = whatToTry(q("error=access_denied"), "the provider refused the login");
    expect(said.title).toMatch(/refused the sign-in/);
    expect(said.advice).toMatch(/whoever runs the identity provider/);
  });

  it("asks for another try when the provider failed some other way", () => {
    expect(whatToTry(q("error=server_error"), "x").advice).toMatch(/^Start again/);
  });

  it.each([
    "this login was not started in this tab, so it cannot be completed here",
    "the provider returned a state this tab did not send",
  ])("sends someone back to start here when %s", (message) => {
    expect(whatToTry(q("code=c&state=s"), message).title).toBe(
      "This sign-in was started somewhere else",
    );
  });

  it("says the answer could not be used when the exchange failed", () => {
    expect(whatToTry(q("code=c&state=s"), "invalid_grant").title).toBe("Sign-in did not complete");
  });
});

it("shows what to try, the provider's words, and a way to start again", async () => {
  brain({
    "GET /auth/config": {
      body: {
        environment: "staging",
        dev_token_accepted: false,
        oidc: { issuer: "http://idp.test", client_id: "openarity" },
      },
    },
  });
  window.history.replaceState(
    null,
    "",
    "/ui/callback?error=access_denied&error_description=User+is+not+assigned+to+this+app",
  );
  showAt("/ui/callback", [["/ui/callback", Route]]);
  setToken("left-over");

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("The identity provider refused the sign-in");
  expect(alert).toHaveTextContent("User is not assigned to this app");
  await userEvent.click(screen.getByRole("button", { name: "Start again" }));
  expect(getToken()).toBeNull();
});
