import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ReactNode, useId, useState } from "react";

import {
  createApprovalRule,
  deleteApprovalRule,
  getApprovalSettings,
  getTeamSettings,
  listAgents,
  listApprovalRules,
  listMCPServers,
  listMCPTools,
  saveApprovalSettings,
  saveTeamSettings,
} from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type {
  ApprovalMode,
  ApprovalRuleMode,
  ApprovalSettings,
  BrowserProvider,
  SandboxProvider,
  SearchProvider,
  TeamSettings,
  TeamSettingsRequest,
} from "@/api/types";
import { DeleteTeamDialog } from "@/components/delete-team-dialog";
import { type Picked, Picker } from "@/components/picker";
import { Field, ReadOnlyNote } from "@/components/registry-bits";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { SwitchField } from "@/components/ui/switch";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { LeaveGuard } from "@/components/unsaved";
import { NO_MODEL } from "@/lib/ask";

export const Route = createFileRoute("/ui/settings")({
  component: Settings,
});

const WRITE = "team:settings";

/** The finding when the team has no model: nothing answers its messages. */
export { NO_MODEL };

const PROVIDERS: { value: SearchProvider; label: string }[] = [
  { value: "none", label: "Off" },
  { value: "searxng", label: "SearXNG (self-hosted)" },
  { value: "brave", label: "Brave Search" },
  { value: "tavily", label: "Tavily" },
  { value: "firecrawl-cloud", label: "Firecrawl Cloud" },
  { value: "firecrawl-oss", label: "Firecrawl (self-hosted)" },
];

const SELF_HOSTED: SearchProvider[] = ["searxng", "firecrawl-oss"];
const KEY_REQUIRED: SearchProvider[] = ["brave", "tavily", "firecrawl-cloud"];

/**
 * Where command servers run, and what each provider does with a server's
 * network list: said beside the choice, since it is what a team is choosing.
 */
const SANDBOXES: { value: SandboxProvider; label: string; about: string }[] = [
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
const BROWSERS: { value: BrowserProvider; label: string; about: string }[] = [
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
const BROWSER_URL: BrowserProvider[] = ["browserless", "steel", "lightpanda", "cdp"];

/** What an E2B team's rooms start from when it names no template; the build script's default. */
const E2B_DEFAULT_TEMPLATE = "openarity-sandbox";
/** The brain's own rule: a name, or owner/name for another team's public template. */
const E2B_TEMPLATE = /^[a-z0-9][a-z0-9_.-]*(\/[a-z0-9][a-z0-9_.-]*)?$/;

/** How many agents one search for the hub offers. */
const PICK_PAGE = 20;

/** The brain's own bounds; checked here so a typo is caught before a round trip. */
const MIN_COMPACTION = 10;
const MAX_COMPACTION = 10000;

function Settings() {
  const { teamId, teams } = useCurrentTeam();
  const teamName = teams.find((t) => t.id === teamId)?.name;
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);

  const settings = useQuery({
    queryKey: ["settings", teamId],
    queryFn: () => getTeamSettings(teamId as string),
    enabled: Boolean(teamId),
  });

  return (
    <div className="space-y-5">
      <PageHeader
        title={teamName ? `Team settings — ${teamName}` : "Team settings"}
        finding={settings.data && settings.data.model === null ? NO_MODEL : null}
        description="For this team only: the model its built-in agents run on, how they search the web, where its command servers run, and what they learn. Each team has its own."
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="settings" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its settings." />
      ) : settings.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading settings">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : settings.isError ? (
        <ErrorState
          what="the settings"
          error={settings.error}
          onRetry={() => settings.refetch()}
          retrying={settings.isRefetching}
        />
      ) : (
        // Keyed on the team: another team's settings start a fresh form. A
        // save does not remount it, so another group's unsaved edits stay.
        <SettingsForm key={teamId} teamId={teamId} stored={settings.data} canWrite={canWrite} />
      )}

      {teamId && teamName && perms.superAdmin ? (
        <DangerZone teamId={teamId} teamName={teamName} />
      ) : null}
    </div>
  );
}

/** Super admins only: the brain refuses a delete from anyone else. */
function DangerZone({ teamId, teamName }: { teamId: string; teamName: string }) {
  return (
    <section
      aria-labelledby="danger-zone"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-destructive/40 bg-card p-4"
    >
      <div className="space-y-1">
        <h2 id="danger-zone" className="text-sm font-medium text-destructive">
          Delete this team
        </h2>
        <p className="max-w-xl text-xs text-muted-foreground">
          Its members lose access right away, and its name can be used again. A super admin can
          restore it from Deleted teams until it is erased for good. Not possible while a run is
          waiting or running.
        </p>
      </div>
      <DeleteTeamDialog teamId={teamId} teamName={teamName} />
    </section>
  );
}

export type SettingsDraft = {
  modelOn: boolean;
  modelName: string;
  maxTokens: string;
  temperature: string;
  provider: SearchProvider;
  url: string;
  key: string;
  dropKey: boolean;
  sandbox: SandboxProvider;
  sandboxURL: string;
  /** E2B only; empty for the default template. */
  sandboxTemplate: string;
  /** Typed only; the stored key is never read back. */
  sandboxKey: string;
  browser: BrowserProvider;
  browserURL: string;
  /** Typed only; the stored key is never read back. */
  browserKey: string;
  /** A self-hosted browser only: reach it with no key. */
  dropBrowserKey: boolean;
  learning: boolean;
  compaction: string;
  /** The chosen hub agent; null for the built-in orchestrator. */
  hub: Picked | null;
};

export function draftOf(s: TeamSettings): SettingsDraft {
  return {
    modelOn: s.model !== null,
    modelName: s.model?.name ?? "",
    maxTokens: s.model ? String(s.model.max_tokens) : "200000",
    temperature: s.model?.temperature === undefined ? "" : String(s.model.temperature),
    provider: s.search.provider,
    url: s.search.url ?? "",
    key: "",
    dropKey: false,
    // A brain from before sandboxes sends none: the team has none.
    sandbox: s.sandbox?.provider ?? "none",
    sandboxURL: s.sandbox?.url ?? "",
    sandboxTemplate: s.sandbox?.template ?? "",
    sandboxKey: "",
    // A brain from before browsers sends none: the team has none.
    browser: s.browser?.provider ?? "none",
    browserURL: s.browser?.url ?? "",
    browserKey: "",
    dropBrowserKey: false,
    learning: s.learning,
    compaction: String(s.compaction_messages),
    hub: s.hub,
  };
}

/** The groups of the page, each saved on its own. */
export type Group = "model" | "hub" | "search" | "sandbox" | "browser" | "learning";

/** Which of the draft's fields each group owns. */
const FIELDS: Record<Group, (keyof SettingsDraft)[]> = {
  model: ["modelOn", "modelName", "maxTokens", "temperature"],
  hub: ["hub", "compaction"],
  search: ["provider", "url", "key", "dropKey"],
  sandbox: ["sandbox", "sandboxURL", "sandboxTemplate", "sandboxKey"],
  browser: ["browser", "browserURL", "browserKey", "dropBrowserKey"],
  learning: ["learning"],
};

function same(a: SettingsDraft[keyof SettingsDraft], b: SettingsDraft[keyof SettingsDraft]) {
  // The hub is an object: two picks of the same agent are the same choice.
  if (a !== null && typeof a === "object" && b !== null && typeof b === "object") {
    return a.name === b.name;
  }
  return a === b;
}

/** Whether a group's fields differ from what the brain holds. */
export function isDirty(group: Group, d: SettingsDraft, stored: TeamSettings): boolean {
  const base = draftOf(stored);
  return FIELDS[group].some((f) => !same(d[f], base[f]));
}

/** What the brain holds, with one group's fields taken from the draft. */
function withGroup(group: Group, d: SettingsDraft, stored: TeamSettings): SettingsDraft {
  const merged = draftOf(stored);
  for (const f of FIELDS[group]) (merged as Record<string, unknown>)[f] = d[f];
  return merged;
}

/**
 * The request one group's Save sends. The brain takes the settings whole, so
 * every other group is sent as the brain holds it, and its unsaved edits are
 * neither sent nor lost.
 */
export function groupRequest(
  group: Group,
  d: SettingsDraft,
  stored: TeamSettings,
): TeamSettingsRequest | string {
  return settingsRequest(withGroup(group, d, stored), stored);
}

function wholeNumber(raw: string): number | null {
  const t = raw.trim();
  return /^\d+$/.test(t) ? Number(t) : null;
}

/**
 * The request the form sends, or the sentence saying why it cannot. Mirrors
 * the brain's rules so most mistakes are caught here; the brain checks again.
 *
 * The key is sent only when typed — exactly as typed, since trimming a
 * credential changes it — and otherwise left out, which keeps the stored one
 * while it still belongs: same provider, and for a self-hosted one, same URL.
 */
export function settingsRequest(
  d: SettingsDraft,
  stored: TeamSettings,
): TeamSettingsRequest | string {
  let model: TeamSettingsRequest["model"] = null;
  if (d.modelOn) {
    const name = d.modelName.trim();
    if (name === "") return "Name the model, as your model gateway names it.";
    const maxTokens = wholeNumber(d.maxTokens);
    if (maxTokens === null || maxTokens < 1) return "Max tokens must be a whole number above 0.";
    model = { name, max_tokens: maxTokens };
    if (d.temperature.trim() !== "") {
      const t = Number(d.temperature.trim());
      if (!Number.isFinite(t) || t < 0 || t > 2) return "Temperature must be between 0 and 2.";
      model.temperature = t;
    }
  }

  const compaction = wholeNumber(d.compaction);
  if (compaction === null || compaction < MIN_COMPACTION || compaction > MAX_COMPACTION) {
    return `Compaction must be between ${MIN_COMPACTION} and ${MAX_COMPACTION} messages.`;
  }

  const search: TeamSettingsRequest["search"] = { provider: d.provider };
  const selfHosted = SELF_HOSTED.includes(d.provider);
  if (selfHosted) {
    const url = d.url.trim();
    if (url === "") return "A self-hosted provider needs its address.";
    search.url = url;
  }

  const sameProvider = stored.search.provider === d.provider;
  const sameURL = !selfHosted || (stored.search.url ?? "") === (search.url ?? "");
  const keeps = sameProvider && sameURL && stored.search.key_set;

  if (d.key !== "") {
    if (!KEY_REQUIRED.includes(d.provider) && d.provider !== "firecrawl-oss") {
      return "This provider takes no key.";
    }
    search.api_key = d.key;
  } else if (KEY_REQUIRED.includes(d.provider) && !keeps) {
    return "Paste this provider's API key. A key is never carried from one provider to another.";
  } else if (d.provider === "firecrawl-oss") {
    if (d.dropKey) {
      search.api_key = "";
    } else if (sameProvider && stored.search.key_set && !sameURL) {
      return "The key stays with the old address. Paste it again, or choose to run without one.";
    }
  }

  const sandbox = sandboxRequest(d, stored);
  if (typeof sandbox === "string") return sandbox;
  const browser = browserRequest(d, stored);
  if (typeof browser === "string") return browser;

  return {
    model,
    search,
    sandbox,
    browser,
    learning: d.learning,
    compaction_messages: compaction,
    // Whole, as the rest is: null sends the team back to the built-in orchestrator.
    hub: d.hub?.name ?? null,
  };
}

/**
 * The sandbox as the brain takes it. Its key is kept as the search key is:
 * left out, it stays while the provider and its URL are unchanged.
 */
function sandboxRequest(
  d: SettingsDraft,
  stored: TeamSettings,
): TeamSettingsRequest["sandbox"] | string {
  const sandbox: NonNullable<TeamSettingsRequest["sandbox"]> = { provider: d.sandbox };
  if (d.sandbox === "none") return sandbox;

  const url = d.sandboxURL.trim();
  if (d.sandbox === "sandboxd" && url === "") return "sandboxd needs its address.";
  if (d.sandbox !== "e2b" && url !== "") sandbox.url = url;
  const template = d.sandboxTemplate.trim();
  if (d.sandbox === "e2b" && template !== "") {
    if (!E2B_TEMPLATE.test(template)) {
      return "An E2B template is a name such as openarity-sandbox, or owner/name for a public one.";
    }
    sandbox.template = template;
  }

  const was = stored.sandbox;
  const keeps =
    was?.key_set && was.provider === d.sandbox && (was.url ?? "") === (sandbox.url ?? "");
  if (d.sandboxKey !== "") {
    sandbox.api_key = d.sandboxKey;
  } else if (!keeps) {
    return was?.key_set && was.provider === d.sandbox
      ? "The key stays with the old address. Paste it again."
      : "Paste the provider's key. A key is never carried from one provider to another.";
  }
  return sandbox;
}

/**
 * The browser as the brain takes it. A hosted one needs its key; a
 * self-hosted one may have none, and its key stays with its address.
 */
export function browserRequest(
  d: SettingsDraft,
  stored: TeamSettings,
): TeamSettingsRequest["browser"] | string {
  const browser: NonNullable<TeamSettingsRequest["browser"]> = { provider: d.browser };
  if (d.browser === "none") return browser;

  const url = d.browserURL.trim();
  if (d.browser === "cdp" && url === "") return "A CDP browser needs its address.";
  if (BROWSER_URL.includes(d.browser) && url !== "") browser.url = url;
  const hosted = browser.url === undefined && d.browser !== "cdp";

  const was = stored.browser;
  const sameProvider = was?.provider === d.browser;
  const keeps = was?.key_set === true && sameProvider && (was.url ?? "") === (browser.url ?? "");
  if (d.browserKey !== "") {
    if (d.browser === "brightdata" && !d.browserKey.includes(":")) {
      return "Bright Data's key is the zone's username and password, as USER:PASS.";
    }
    browser.api_key = d.browserKey;
  } else if (d.dropBrowserKey && !hosted) {
    browser.api_key = "";
  } else if (hosted && !keeps) {
    return "Paste the provider's key. A key is never carried from one provider to another.";
  } else if (!hosted && !keeps && was?.key_set && sameProvider) {
    return "The key stays with the old address. Paste it again, or choose to reach it with none.";
  }
  return browser;
}

/** One group: what it decides, its fields, and its own Save. */
function Section({
  title,
  about,
  canWrite,
  dirty,
  saved,
  problem,
  pending,
  busy,
  onSave,
  children,
}: {
  title: string;
  about: ReactNode;
  canWrite: boolean;
  dirty: boolean;
  /** Saved since it last changed: said beside the button until the next edit. */
  saved: boolean;
  problem: string | null;
  pending: boolean;
  /** Another group is saving; the settings are sent whole, so one at a time. */
  busy: boolean;
  onSave: () => void;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section
      aria-labelledby={id}
      className="grid gap-x-8 gap-y-3 border-t border-rule py-6 first:border-t-0 first:pt-0 lg:grid-cols-[13rem_minmax(0,1fr)]"
    >
      <div className="space-y-1">
        <h2 id={id} className="text-base font-medium">
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{about}</p>
      </div>
      <form
        className="min-w-0 max-w-2xl space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          onSave();
        }}
      >
        <fieldset disabled={!canWrite} className="space-y-4">
          {children}
        </fieldset>
        {canWrite ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {/* Offered only for a change: a row of greyed buttons says nothing. */}
            {dirty || pending ? (
              <Button type="submit" size="sm" disabled={busy} loading={pending}>
                Save
              </Button>
            ) : null}
            {problem ? (
              <p role="alert" className="text-sm text-destructive">
                {problem}
              </p>
            ) : dirty ? (
              <p className="text-sm text-muted-foreground">Unsaved changes</p>
            ) : saved ? (
              <p role="status" className="text-sm text-succeeded">
                Saved
              </p>
            ) : null}
          </div>
        ) : null}
      </form>
    </section>
  );
}

const GROUPS: Group[] = ["model", "hub", "search", "sandbox", "browser", "learning"];

function SettingsForm({
  teamId,
  stored,
  canWrite,
}: {
  teamId: string;
  stored: TeamSettings;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<SettingsDraft>(() => draftOf(stored));
  const [problem, setProblem] = useState<{ group: Group; text: string } | null>(null);
  const [pending, setPending] = useState<Group | null>(null);
  const [saved, setSaved] = useState<Group | null>(null);

  const set = (group: Group, patch: Partial<SettingsDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    if (problem?.group === group) setProblem(null);
    if (saved === group) setSaved(null);
  };

  const dirty = (g: Group) => isDirty(g, draft, stored);
  const selfHosted = SELF_HOSTED.includes(draft.provider);
  const takesKey = KEY_REQUIRED.includes(draft.provider) || draft.provider === "firecrawl-oss";
  const keyKept = stored.search.key_set && stored.search.provider === draft.provider;
  const sandboxKeyKept =
    stored.sandbox?.key_set === true && stored.sandbox.provider === draft.sandbox;
  const sandboxAbout = SANDBOXES.find((p) => p.value === draft.sandbox)?.about;
  const browserKeyKept =
    stored.browser?.key_set === true &&
    stored.browser.provider === draft.browser &&
    (stored.browser.url ?? "") === draft.browserURL.trim();
  const browserAbout = BROWSERS.find((p) => p.value === draft.browser)?.about;
  const browserTakesURL = BROWSER_URL.includes(draft.browser);
  const browserSelfHosted =
    draft.browser === "cdp" || (browserTakesURL && draft.browserURL.trim() !== "");

  // A plain call rather than useMutation: a mutation keeps its variables in
  // the query client's cache, and they can hold the key.
  const save = async (group: Group) => {
    const body = groupRequest(group, draft, stored);
    if (typeof body === "string") {
      setProblem({ group, text: body });
      return;
    }
    setPending(group);
    setProblem(null);
    try {
      const next = await saveTeamSettings(teamId, body);
      qc.setQueryData(["settings", teamId], next);
      // This group now reads what the brain holds, the typed key gone; the
      // others keep what was typed in them.
      const fresh = draftOf(next);
      setDraft((d) => {
        const out = { ...d };
        for (const f of FIELDS[group]) (out as Record<string, unknown>)[f] = fresh[f];
        return out;
      });
      setSaved(group);
    } catch (err) {
      setProblem({ group, text: err instanceof Error ? err.message : String(err) });
    } finally {
      setPending(null);
    }
  };

  const props = (group: Group) => ({
    canWrite,
    dirty: dirty(group),
    saved: saved === group,
    problem: problem?.group === group ? problem.text : null,
    pending: pending === group,
    busy: pending !== null && pending !== group,
    onSave: () => void save(group),
  });

  return (
    <div>
      <LeaveGuard dirty={() => GROUPS.some(dirty)} what="the team settings" />

      <Section
        title="Model"
        about="The model the hub and the team's built-in agents run on."
        {...props("model")}
      >
        <SwitchField
          label="Use a model"
          checked={draft.modelOn}
          onCheckedChange={(modelOn) => set("model", { modelOn })}
        />
        {draft.modelOn ? (
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Model" htmlFor="settings-model" hint="As the model gateway names it.">
              <Input
                id="settings-model"
                value={draft.modelName}
                onChange={(e) => set("model", { modelName: e.target.value })}
              />
            </Field>
            <Field label="Max tokens" htmlFor="settings-max-tokens">
              <Input
                id="settings-max-tokens"
                inputMode="numeric"
                value={draft.maxTokens}
                onChange={(e) => set("model", { maxTokens: e.target.value })}
              />
            </Field>
            <Field
              label="Temperature"
              htmlFor="settings-temperature"
              hint="0 to 2; empty for the model's own."
            >
              <Input
                id="settings-temperature"
                inputMode="decimal"
                value={draft.temperature}
                onChange={(e) => set("model", { temperature: e.target.value })}
              />
            </Field>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            No model — the built-in agents do not run, and messages are stored unanswered.
          </p>
        )}
      </Section>

      <Section
        title="The hub"
        about="Every message reaches the hub first. Leave empty for the built-in orchestrator."
        {...props("hub")}
      >
        {draft.hub === null ? <p className="text-sm">The built-in orchestrator</p> : null}
        <Picker
          label="Hub"
          noun="agent"
          max={1}
          value={draft.hub ? [draft.hub] : []}
          onChange={(next) => set("hub", { hub: next[0] ?? null })}
          search={(q) =>
            listAgents(teamId, null, { q, limit: PICK_PAGE }).then((page) => ({
              items: page.items.map((a) => ({ id: a.id, name: a.name })),
              more: Boolean(page.next_cursor),
            }))
          }
          searchKey={["agents", teamId]}
        />
        <Field
          label="Messages before a transcript compacts"
          htmlFor="settings-compaction"
          hint={`${MIN_COMPACTION} to ${MAX_COMPACTION}. A run also compacts when its next request would read a tenth of its token budget, at least 50,000 and at most 100,000 tokens.`}
        >
          <Input
            id="settings-compaction"
            inputMode="numeric"
            className="max-w-40"
            value={draft.compaction}
            onChange={(e) => set("hub", { compaction: e.target.value })}
          />
        </Field>
      </Section>

      <Section
        title="Web search"
        about="Where the team's agents search the web, when they are given it."
        {...props("search")}
      >
        <Field label="Provider" htmlFor="settings-provider">
          <select
            id="settings-provider"
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={draft.provider}
            onChange={(e) =>
              set("search", { provider: e.target.value as SearchProvider, key: "", dropKey: false })
            }
          >
            {PROVIDERS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </Field>
        {selfHosted ? (
          <Field
            label="Address"
            htmlFor="settings-url"
            hint="Where the brain reaches it, such as http://searxng:8080."
          >
            <Input
              id="settings-url"
              value={draft.url}
              onChange={(e) => set("search", { url: e.target.value })}
            />
          </Field>
        ) : null}
        {takesKey ? (
          <Field
            label="API key"
            htmlFor="settings-key"
            hint={
              keyKept
                ? "A key is set. Leave this empty to keep it."
                : draft.provider === "firecrawl-oss"
                  ? "Only if the instance requires one."
                  : "Kept in the secret store and never shown again."
            }
          >
            {/* new-password, not off: browsers ignore off on a password field
                and would offer the sign-in password saved for this site. */}
            <Input
              id="settings-key"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              value={draft.key}
              onChange={(e) => set("search", { key: e.target.value, dropKey: false })}
            />
          </Field>
        ) : null}
        {draft.provider === "firecrawl-oss" && keyKept ? (
          <SwitchField
            label="Run without a key"
            checked={draft.dropKey}
            onCheckedChange={(dropKey) => set("search", { dropKey, key: "" })}
          />
        ) : null}
      </Section>

      <Section
        title="Sandbox"
        about="Where the team's command MCP servers run, each in a sandbox of its own that reaches only the network its server lists."
        {...props("sandbox")}
      >
        <Field label="Sandbox provider" htmlFor="settings-sandbox" hint={sandboxAbout}>
          <select
            id="settings-sandbox"
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={draft.sandbox}
            onChange={(e) =>
              // The address and key are the old provider's.
              set("sandbox", {
                sandbox: e.target.value as SandboxProvider,
                sandboxURL: "",
                sandboxTemplate: "",
                sandboxKey: "",
              })
            }
          >
            {SANDBOXES.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </Field>
        {draft.sandbox === "daytona" || draft.sandbox === "sandboxd" ? (
          <Field
            label="API address"
            htmlFor="settings-sandbox-url"
            hint={
              draft.sandbox === "daytona"
                ? "Empty for Daytona's hosted service; your instance's API address otherwise."
                : "Where the brain reaches it, such as http://sandboxd:8080."
            }
          >
            <Input
              id="settings-sandbox-url"
              value={draft.sandboxURL}
              onChange={(e) => set("sandbox", { sandboxURL: e.target.value })}
            />
          </Field>
        ) : null}
        {draft.sandbox === "e2b" ? (
          <Field
            label="Template"
            htmlFor="settings-sandbox-template"
            hint={`Built in your E2B account from deployment/sandbox/e2b. Empty for ${E2B_DEFAULT_TEMPLATE}.`}
          >
            <Input
              id="settings-sandbox-template"
              spellCheck={false}
              placeholder={E2B_DEFAULT_TEMPLATE}
              value={draft.sandboxTemplate}
              onChange={(e) => set("sandbox", { sandboxTemplate: e.target.value })}
            />
          </Field>
        ) : null}
        {draft.sandbox !== "none" ? (
          <Field
            label={draft.sandbox === "sandboxd" ? "Token" : "API key"}
            htmlFor="settings-sandbox-key"
            hint={
              sandboxKeyKept
                ? "A key is set. Leave this empty to keep it."
                : "Kept in the secret store and never shown again."
            }
          >
            <Input
              id="settings-sandbox-key"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              value={draft.sandboxKey}
              onChange={(e) => set("sandbox", { sandboxKey: e.target.value })}
            />
          </Field>
        ) : null}
      </Section>

      <Section
        title="Browser"
        about="What the hub, and agents granted one, drive pages in: open, read, click and type. A page runs its scripts, and a browser of your own is never let reach a private address."
        {...props("browser")}
      >
        <Field label="Browser provider" htmlFor="settings-browser" hint={browserAbout}>
          <select
            id="settings-browser"
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={draft.browser}
            onChange={(e) =>
              // The address and key are the old provider's.
              set("browser", {
                browser: e.target.value as BrowserProvider,
                browserURL: "",
                browserKey: "",
                dropBrowserKey: false,
              })
            }
          >
            {BROWSERS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </Field>
        {browserTakesURL ? (
          <Field
            label="Address"
            htmlFor="settings-browser-url"
            hint={
              draft.browser === "cdp"
                ? "Where the brain reaches it, such as ws://headless-shell:9222."
                : draft.browser === "steel"
                  ? "Empty for Steel's hosted service; your instance's API address, such as http://steel:3000, otherwise."
                  : "Empty for the hosted service; your own browser's address, such as ws://lightpanda:9222, otherwise."
            }
          >
            <Input
              id="settings-browser-url"
              spellCheck={false}
              value={draft.browserURL}
              onChange={(e) => set("browser", { browserURL: e.target.value })}
            />
          </Field>
        ) : null}
        {draft.browser !== "none" && !(browserSelfHosted && draft.dropBrowserKey) ? (
          <Field
            label={browserSelfHosted ? "Token" : "API key"}
            htmlFor="settings-browser-key"
            hint={
              browserKeyKept
                ? "A key is set. Leave this empty to keep it."
                : browserSelfHosted
                  ? "Only if your browser asks for one. Kept in the secret store and never shown again."
                  : "Kept in the secret store and never shown again."
            }
          >
            <Input
              id="settings-browser-key"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              value={draft.browserKey}
              onChange={(e) => set("browser", { browserKey: e.target.value })}
            />
          </Field>
        ) : null}
        {browserSelfHosted && stored.browser?.key_set ? (
          <SwitchField
            label="Reach it with no key"
            description="Removes the stored key, for a browser of your own that asks for none."
            checked={draft.dropBrowserKey}
            onCheckedChange={(dropBrowserKey) => set("browser", { dropBrowserKey, browserKey: "" })}
          />
        ) : null}
      </Section>

      <Section
        title="Learning"
        about="What the team's agents take away from their runs."
        {...props("learning")}
      >
        <SwitchField
          label="Learn from each run"
          description="After a run, the learner writes what it taught. Nothing it writes is used until someone who may approve learning approves it."
          checked={draft.learning}
          onCheckedChange={(learning) => set("learning", { learning })}
        />
      </Section>

      <ApprovalSections teamId={teamId} canWrite={canWrite} />
    </div>
  );
}

/* ---------------------------------------------------------- tool approvals */

const APPROVAL_MODES: { value: ApprovalMode; label: string; about: string }[] = [
  { value: "off", label: "Off", about: "Agents never ask. Every call is made." },
  {
    value: "writes",
    label: "Writes and destructive calls",
    about: "Agents ask before any call that is not a read.",
  },
  {
    value: "destructive",
    label: "Destructive calls only",
    about: "Agents ask only before a call that destroys something.",
  },
  { value: "all", label: "Every call", about: "Agents ask before every call, reads too." },
];

const TTL_MIN = 5;
const TTL_MAX = 1440;

/**
 * When the team's agents ask a person, and the standing answers that decide
 * for one tool or one server. Both have routes of their own, apart from the
 * rest of the settings, so they save on their own.
 */
function ApprovalSections({ teamId, canWrite }: { teamId: string; canWrite: boolean }) {
  const stored = useQuery({
    queryKey: ["approval-settings", teamId],
    queryFn: () => getApprovalSettings(teamId),
  });
  return (
    <>
      {stored.isError ? (
        <section className="border-t border-rule py-6">
          <ErrorState
            what="the approval settings"
            error={stored.error}
            onRetry={() => stored.refetch()}
            retrying={stored.isRefetching}
          />
        </section>
      ) : stored.data ? (
        <ApprovalModeSection teamId={teamId} stored={stored.data} canWrite={canWrite} />
      ) : (
        <Skeleton className="mt-6 h-24 w-full" role="status" aria-label="Loading approvals" />
      )}
      <ApprovalRulesSection teamId={teamId} canWrite={canWrite} />
    </>
  );
}

function ApprovalModeSection({
  teamId,
  stored,
  canWrite,
}: {
  teamId: string;
  stored: ApprovalSettings;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<ApprovalMode>(stored.mode);
  const [ttl, setTtl] = useState(String(stored.ttl_minutes));
  const [saved, setSaved] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const minutes = Number(ttl);
  const valid = Number.isInteger(minutes) && minutes >= TTL_MIN && minutes <= TTL_MAX;
  const dirty =
    mode !== stored.mode ||
    (valid ? minutes !== stored.ttl_minutes : ttl !== String(stored.ttl_minutes));

  const save = useMutation({
    mutationFn: (s: ApprovalSettings) => saveApprovalSettings(teamId, s),
    onSuccess: (next) => {
      qc.setQueryData(["approval-settings", teamId], next);
      setSaved(true);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <Section
      title="Tool approvals"
      about="When the team's agents ask a person before making a call. Runs already started keep the settings they began with."
      canWrite={canWrite}
      dirty={dirty}
      saved={saved && !dirty}
      problem={problem}
      pending={save.isPending}
      busy={false}
      onSave={() => {
        if (!valid) {
          setProblem(`Wait between ${TTL_MIN} and ${TTL_MAX} minutes.`);
          return;
        }
        setProblem(null);
        save.mutate({ mode, ttl_minutes: minutes });
      }}
    >
      <Field
        label="Ask before"
        htmlFor="settings-approval-mode"
        hint={APPROVAL_MODES.find((m) => m.value === mode)?.about}
      >
        <select
          id="settings-approval-mode"
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          value={mode}
          onChange={(e) => {
            setMode(e.target.value as ApprovalMode);
            setSaved(false);
            setProblem(null);
          }}
        >
          {APPROVAL_MODES.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </select>
      </Field>
      <Field
        label="Minutes a request waits"
        htmlFor="settings-approval-ttl"
        hint={`${TTL_MIN} to ${TTL_MAX}. After that it expires and the run carries on without it.`}
      >
        <Input
          id="settings-approval-ttl"
          type="number"
          inputMode="numeric"
          className="w-32"
          value={ttl}
          onChange={(e) => {
            setTtl(e.target.value);
            setSaved(false);
            setProblem(null);
          }}
        />
      </Field>
    </Section>
  );
}

const RULE_WORDS: Record<ApprovalRuleMode, string> = {
  allow: "Allow without asking",
  ask: "Always ask",
  deny: "Never allow",
};

/** The brain's own tools a rule may name: those the approval gate stands before. */
const BUILT_INS = ["web_search", "web_fetch"] as const;
const BUILT_IN: Picked = { id: "built-in", name: "Built-in tools" };

/** The servers a rule may name, searched by name; a tool rule may also name a built-in. */
async function ruleServers(teamId: string, q: string, builtIns: boolean) {
  const page = await listMCPServers(teamId, null, { q, limit: PICK_PAGE });
  const items: Picked[] = page.items.map((s) => ({ id: s.id, name: s.name }));
  const wanted = q.trim().toLowerCase();
  if (builtIns && (wanted === "" || "built-in tools".includes(wanted))) items.unshift(BUILT_IN);
  return { items, more: Boolean(page.next_cursor) };
}

/**
 * The tool a rule names, chosen from what its server said it has: a rule's
 * target is <server>__<tool>, or a built-in's own name.
 */
function RuleTool({
  teamId,
  server,
  value,
  onChange,
}: {
  teamId: string;
  server: Picked | null;
  value: string;
  onChange: (target: string) => void;
}) {
  const builtIn = server?.id === BUILT_IN.id;
  const tools = useQuery({
    queryKey: ["mcp-tools", teamId, server?.id],
    queryFn: () => listMCPTools(teamId, server?.id as string),
    enabled: Boolean(server) && !builtIn,
  });
  const names: string[] = builtIn
    ? [...BUILT_INS]
    : (tools.data?.items ?? []).map((t) => `${server?.name}__${t.name}`);
  const hint = !server
    ? "Choose a server first."
    : builtIn
      ? "The brain's own tools."
      : tools.isPending
        ? "Reading the server's tools…"
        : tools.isError
          ? "Couldn't read this server's tools. Try again from its page."
          : names.length === 0
            ? "This server has said it has no tools."
            : undefined;
  return (
    <Field label="Tool" htmlFor="rule-target" hint={hint}>
      <select
        id="rule-target"
        className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-50"
        value={value}
        disabled={names.length === 0}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">Choose a tool</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {builtIn ? n : n.slice((server?.name.length ?? 0) + 2)}
          </option>
        ))}
      </select>
    </Field>
  );
}

function ApprovalRulesSection({ teamId, canWrite }: { teamId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const rules = useQuery({
    queryKey: ["approval-rules", teamId],
    queryFn: () => listApprovalRules(teamId),
  });
  const [kind, setKind] = useState<"tool" | "server">("tool");
  // The server a rule names, or whose tool it names: picked, not typed.
  const [server, setServer] = useState<Picked | null>(null);
  const [target, setTarget] = useState("");
  const [mode, setMode] = useState<ApprovalRuleMode>("ask");
  const [other, setOther] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const refresh = () => qc.invalidateQueries({ queryKey: ["approval-rules", teamId] });
  const add = useMutation({
    mutationFn: () =>
      createApprovalRule(teamId, {
        target_kind: kind,
        target: target.trim(),
        mode,
        require_other: other,
      }),
    onSuccess: () => {
      setServer(null);
      setTarget("");
      setOther(false);
      setProblem(null);
      void refresh();
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteApprovalRule(teamId, id),
    onSuccess: () => {
      setProblem(null);
      void refresh();
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const items = rules.data?.items ?? [];

  return (
    <Section
      title="Tool approval rules"
      about="A standing answer for one tool or every tool of a server, whatever the mode. The most specific rule wins."
      canWrite={canWrite}
      dirty={false}
      saved={false}
      problem={problem}
      pending={false}
      busy={false}
      onSave={() => {
        if (target.trim() === "") {
          setProblem(
            kind === "tool"
              ? "Choose the tool the rule is for."
              : "Choose the server the rule is for.",
          );
          return;
        }
        add.mutate();
      }}
    >
      {rules.isError ? (
        <ErrorState
          what="the approval rules"
          error={rules.error}
          onRetry={() => rules.refetch()}
          retrying={rules.isRefetching}
        />
      ) : rules.isPending ? (
        <Skeleton className="h-12 w-full" role="status" aria-label="Loading rules" />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No rules. The mode above decides for every call.
        </p>
      ) : (
        <table className="w-full text-sm" aria-label="Tool approval rules">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="py-1 pr-3 font-normal">Applies to</th>
              <th className="py-1 pr-3 font-normal">Target</th>
              <th className="py-1 pr-3 font-normal">Answer</th>
              <th className="py-1 pr-3 font-normal">Someone else answers</th>
              <th className="py-1">
                <span className="sr-only">Remove</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.id} data-rule={r.target} className="border-t border-rule">
                <td className="py-2 pr-3">{r.target_kind === "tool" ? "A tool" : "A server"}</td>
                <td className="py-2 pr-3 font-mono text-xs break-all">{r.target}</td>
                <td className="py-2 pr-3">{RULE_WORDS[r.mode]}</td>
                <td className="py-2 pr-3">{r.require_other ? "Yes" : "No"}</td>
                <td className="py-2 text-right">
                  {canWrite ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={remove.isPending}
                      aria-label={`Remove the rule for ${r.target}`}
                      onClick={() => remove.mutate(r.id)}
                    >
                      Remove
                    </Button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canWrite ? (
        <div className="space-y-3 border-t border-rule pt-4">
          <div
            className={
              kind === "tool"
                ? "grid gap-3 sm:grid-cols-[8rem_minmax(0,1fr)_minmax(0,1fr)_12rem]"
                : "grid gap-3 sm:grid-cols-[8rem_minmax(0,1fr)_12rem]"
            }
          >
            <Field label="Applies to" htmlFor="rule-kind">
              <select
                id="rule-kind"
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={kind}
                onChange={(e) => {
                  setKind(e.target.value as "tool" | "server");
                  setServer(null);
                  setTarget("");
                }}
              >
                <option value="tool">A tool</option>
                <option value="server">A server</option>
              </select>
            </Field>
            <div className="min-w-0 space-y-1.5">
              <Picker
                label="Server"
                noun="server"
                max={1}
                value={server ? [server] : []}
                onChange={(next) => {
                  const picked = next[0] ?? null;
                  setServer(picked);
                  setTarget(kind === "server" && picked ? picked.name : "");
                }}
                search={(q) => ruleServers(teamId, q, kind === "tool")}
                searchKey={["rule-servers", teamId, kind]}
              />
            </div>
            {kind === "tool" ? (
              <RuleTool teamId={teamId} server={server} value={target} onChange={setTarget} />
            ) : null}
            <Field label="Answer" htmlFor="rule-mode">
              <select
                id="rule-mode"
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={mode}
                onChange={(e) => setMode(e.target.value as ApprovalRuleMode)}
              >
                {(Object.keys(RULE_WORDS) as ApprovalRuleMode[]).map((m) => (
                  <option key={m} value={m}>
                    {RULE_WORDS[m]}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <SwitchField
            label="Someone else must answer"
            description="The person the run works for may not approve their own request."
            checked={other}
            onCheckedChange={setOther}
          />
          <Button type="submit" size="sm" variant="outline" loading={add.isPending}>
            Add rule
          </Button>
        </div>
      ) : null}
    </Section>
  );
}
