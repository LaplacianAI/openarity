import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { getToken, setToken } from "@/api/token";
import { SignIn } from "./sign-in";

function respond(body: unknown, init: ResponseInit = { status: 200 }) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () => new Response(JSON.stringify(body), init)),
  );
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SignIn />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

// The whole point of reading /auth/config before login: one build serves a
// laptop and a cluster, and the brain says which it is.
it("offers the development token when the brain accepts one", async () => {
  respond({ environment: "development", dev_token_accepted: true });
  show();

  expect(await screen.findByLabelText(/development token/i)).toBeInTheDocument();
});

it("stores the token the operator typed, so the screens can use it", async () => {
  respond({ environment: "development", dev_token_accepted: true });
  show();

  await userEvent.type(await screen.findByLabelText(/development token/i), "letmein");
  await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

  expect(getToken()).toBe("letmein");
});

it("offers the provider when the brain names one", async () => {
  respond({
    environment: "staging",
    dev_token_accepted: false,
    oidc: { issuer: "http://127.0.0.1:5556", client_id: "openarity" },
  });
  show();

  expect(await screen.findByRole("button", { name: /sign in/i })).toBeInTheDocument();
  expect(screen.getByText("http://127.0.0.1:5556")).toBeInTheDocument();
  expect(screen.queryByLabelText(/development token/i)).not.toBeInTheDocument();
});

// Web Crypto exists only in a secure context. Saying so before the button is
// pressed is the difference between an explanation and a failed redirect.
it("refuses to offer sign-in without Web Crypto, and says why", async () => {
  const real = globalThis.crypto;
  Object.defineProperty(globalThis, "crypto", { value: undefined, configurable: true });

  respond({
    environment: "staging",
    dev_token_accepted: false,
    oidc: { issuer: "http://192.168.1.6:5556", client_id: "openarity" },
  });
  show();

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent(/secure context/i);
  expect(screen.queryByRole("button", { name: /^sign in$/i })).not.toBeInTheDocument();

  Object.defineProperty(globalThis, "crypto", { value: real, configurable: true });
});

// A brain with OIDC off and DEV_TOKEN cleared accepts nobody. Saying so is the
// difference between a bug report and a configuration fix.
it("says so when the brain accepts no logins at all", async () => {
  respond({ environment: "staging", dev_token_accepted: false });
  show();

  expect(await screen.findByText(/accepts no logins/i)).toBeInTheDocument();
});

it("reports an unreachable brain instead of waiting forever", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () => new Response("upstream is down", { status: 503 })),
  );
  show();

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent(/did not answer/i);
  expect(alert).toHaveTextContent(/upstream is down/i);
});
