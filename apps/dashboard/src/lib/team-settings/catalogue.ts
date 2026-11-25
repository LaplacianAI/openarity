import type { BrowserProvider, SandboxProvider, SearchProvider } from "@/api/types";

export const PROVIDERS: { value: SearchProvider; label: string }[] = [
  { value: "none", label: "Off" },
  { value: "searxng", label: "SearXNG (self-hosted)" },
  { value: "brave", label: "Brave Search" },
  { value: "tavily", label: "Tavily" },
  { value: "firecrawl-cloud", label: "Firecrawl Cloud" },
  { value: "firecrawl-oss", label: "Firecrawl (self-hosted)" },
];

export const SELF_HOSTED: SearchProvider[] = ["searxng", "firecrawl-oss"];
export const KEY_REQUIRED: SearchProvider[] = ["brave", "tavily", "firecrawl-cloud"];

/**
 * Where command servers run, and what each provider does with a server's
 * network list: said beside the choice, since it is what a team is choosing.
 */
export const SANDBOXES: { value: SandboxProvider; label: string; about: string }[] = [
  { value: "none", label: "Off", about: "Command servers do not run." },
  {
    value: "e2b",
    label: "E2B",
    about:
      "Hosted. Web ports (80 and 443) are allowed by name; any other port is allowed by the addresses its host has when the sandbox is made.",
  },
  {
    value: "daytona",
    label: "Daytona",
    about:
      "Hosted, or your own instance at its API address. Hosts are allowed by the addresses they have when the sandbox is made, on every port; hosted Daytona allows a list of web hosts (port 443) by name instead.",
  },
  {
    value: "sandboxd",
    label: "sandboxd (self-hosted)",
    about: "Your own sandboxd at its address, with its token as the key.",
  },
];

/**
 * What a team's runs drive pages in. Where the browser runs is what a team is
 * choosing: on the provider's network, or on its own beside the brain.
 */
export const BROWSERS: { value: BrowserProvider; label: string; about: string }[] = [
  { value: "none", label: "Off", about: "Runs have no browser." },
  {
    value: "lightpanda",
    label: "Lightpanda",
    about:
      "A browser built for automation, using a fraction of Chrome's memory; a few sites do not work in it yet. Hosted, or your own at its address.",
  },
  {
    value: "browserless",
    label: "Browserless",
    about: "Chrome, hosted, or your own Browserless at its address with its token as the key.",
  },
  {
    value: "steel",
    label: "Steel",
    about: "Chrome, hosted, or your own Steel at its API address, which takes no key.",
  },
  {
    value: "cdp",
    label: "Any CDP browser (self-hosted)",
    about:
      "Any browser that speaks the Chrome DevTools Protocol at its address, such as headless-shell. Keep it on a network only the brain reaches.",
  },
  {
    value: "browserbase",
    label: "Browserbase",
    about: "Hosted. Each run gets a session of its own.",
  },
  {
    value: "hyperbrowser",
    label: "Hyperbrowser",
    about: "Hosted. Each run gets a session of its own.",
  },
  { value: "anchor", label: "Anchor", about: "Hosted. Each run gets a session of its own." },
  { value: "kernel", label: "Kernel", about: "Hosted. Each run gets a browser of its own." },
  {
    value: "brightdata",
    label: "Bright Data",
    about:
      "Hosted, with its proxies, for sites that block automation. The key is the zone's username and password, as USER:PASS.",
  },
];
/** Providers that run at an address of the team's own. */
export const BROWSER_URL: BrowserProvider[] = ["browserless", "steel", "lightpanda", "cdp"];

/** How many agents one search for the hub offers. */
export const PICK_PAGE = 20;
