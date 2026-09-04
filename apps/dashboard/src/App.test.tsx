import { render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";

function respondWith(body: unknown, init: ResponseInit = { status: 200 }) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () => new Response(JSON.stringify(body), init)),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

it("says what it is doing before the brain answers", () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(() => new Promise(() => {})),
  );

  render(<App />);

  expect(screen.getByText(/asking the brain/i)).toBeInTheDocument();
});

it("reports the issuer when the brain has one", async () => {
  respondWith({
    environment: "staging",
    dev_token_accepted: false,
    oidc: { issuer: "http://127.0.0.1:5556", client_id: "openarity" },
  });

  render(<App />);

  expect(await screen.findByText(/127\.0\.0\.1:5556/)).toBeInTheDocument();
  expect(screen.getByText("staging")).toBeInTheDocument();
});

it("reports the development token when that is the only way in", async () => {
  respondWith({ environment: "development", dev_token_accepted: true });

  render(<App />);

  expect(await screen.findByText(/development token/i)).toBeInTheDocument();
});

// A brain with OIDC off and the development token cleared accepts nobody. The
// page has to say so rather than show an empty field, because the reader is
// looking at it precisely because they cannot log in.
it("says so when the brain accepts no logins at all", async () => {
  respondWith({ environment: "staging", dev_token_accepted: false });

  render(<App />);

  expect(await screen.findByText(/accepts no logins/i)).toBeInTheDocument();
});

// An unreachable brain must surface as an error, not an endless spinner.
it("shows the reason when the brain cannot be reached", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () => {
      throw new Error("Failed to fetch");
    }),
  );

  render(<App />);

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent(/failed to fetch/i);
});
