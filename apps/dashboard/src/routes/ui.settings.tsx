import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Ban, Check, Clock, Info, TriangleAlert } from "lucide-react";
import { RadioGroup } from "radix-ui";
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
  ModelRef,
  SandboxProvider,
  SearchProvider,
  TeamSettings,
  TeamSettingsRequest,
} from "@/api/types";
import { Choice, type ChoiceGroup, chosen, Segments } from "@/components/choices";
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
import {
  type CompactionDraft,
  type CompactionProblems,
  checkCompaction,
  DEFAULTS,
  explain,
  firstProblem,
  grouped,
  isDefault,
  MESSAGES,
  parsedCompaction,
  parseWhole,
  TOKENS,
  tidied,
} from "@/lib/compaction";
import { cn } from "@/lib/utils";
import { waitWords } from "@/lib/wait";

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
        description="For this team only: the model its built-in agents run on, how they search the web, where its command servers run, and whether they propose improvements to themselves. Each team has its own."
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
  improvement: boolean;
  /** Whether runs are judged on a model of their own, not the team's. */
  judgeOn: boolean;
  judgeName: string;
  judgeMaxTokens: string;
  judgeTemperature: string;
  /** The share of good runs studied, as typed. */
  sampleShare: string;
  /** The most improver passes in a day, as typed. */
  dailyPasses: string;
  /** What one try at a pass may spend, as typed. */
  passTokens: string;
  passMinutes: string;
  /** Messages, and the token lines, as typed: compared by value. */
  compaction: string;
  tokensMin: string;
  tokensMax: string;
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
    improvement: s.improvement,
    judgeOn: s.judge_model != null,
    judgeName: s.judge_model?.name ?? "",
    judgeMaxTokens: s.judge_model ? String(s.judge_model.max_tokens) : "4096",
    judgeTemperature:
      s.judge_model?.temperature === undefined ? "" : String(s.judge_model?.temperature ?? ""),
    sampleShare: String(s.improvement_sample_share ?? 10),
    dailyPasses: String(s.improvement_daily_passes ?? 20),
    passTokens: grouped(s.improvement_pass_tokens ?? 400_000),
    passMinutes: String(s.improvement_pass_minutes ?? 20),
    compaction: String(s.compaction_messages),
    tokensMin: grouped(s.compaction_tokens_min),
    tokensMax: grouped(s.compaction_tokens_max),
    hub: s.hub,
  };
}

/** The groups of the page, each saved on its own. */
export type Group =
  | "model"
  | "hub"
  | "compaction"
  | "search"
  | "sandbox"
  | "browser"
  | "improvement";

/** Which of the draft's fields each group owns. */
const FIELDS: Record<Group, (keyof SettingsDraft)[]> = {
  model: ["modelOn", "modelName", "maxTokens", "temperature"],
  hub: ["hub"],
  compaction: ["compaction", "tokensMin", "tokensMax"],
  search: ["provider", "url", "key", "dropKey"],
  sandbox: ["sandbox", "sandboxURL", "sandboxTemplate", "sandboxKey"],
  browser: ["browser", "browserURL", "browserKey", "dropBrowserKey"],
  improvement: [
    "improvement",
    "judgeOn",
    "judgeName",
    "judgeMaxTokens",
    "judgeTemperature",
    "sampleShare",
    "dailyPasses",
    "passTokens",
    "passMinutes",
  ],
};

function same(a: SettingsDraft[keyof SettingsDraft], b: SettingsDraft[keyof SettingsDraft]) {
  // The hub is an object: two picks of the same agent are the same choice.
  if (a !== null && typeof a === "object" && b !== null && typeof b === "object") {
    return a.name === b.name;
  }
  return a === b;
}

/** Fields compared by the number they hold: "60000" is the stored 60,000. */
const BY_VALUE = new Set<keyof SettingsDraft>([
  "compaction",
  "tokensMin",
  "tokensMax",
  "sampleShare",
  "dailyPasses",
  "passTokens",
  "passMinutes",
]);

/** Whether a group's fields differ from what the brain holds. */
export function isDirty(group: Group, d: SettingsDraft, stored: TeamSettings): boolean {
  const base = draftOf(stored);
  return FIELDS[group].some((f) =>
    BY_VALUE.has(f)
      ? (parseWhole(d[f] as string) ?? d[f]) !== (parseWhole(base[f] as string) ?? base[f])
      : !same(d[f], base[f]),
  );
}

/** The compaction fields of a draft, as lib/compaction checks them. */
function compactionOf(d: SettingsDraft): CompactionDraft {
  return { messages: d.compaction, least: d.tokensMin, most: d.tokensMax };
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
 * neither sent nor lost. The token lines are optional on the wire, so only
 * Compaction's own Save sends them: another group's Save, from a tab opened
 * before someone else changed them, leaves them as the brain holds them.
 */
export function groupRequest(
  group: Group,
  d: SettingsDraft,
  stored: TeamSettings,
): TeamSettingsRequest | string {
  const body = settingsRequest(withGroup(group, d, stored), stored);
  if (typeof body === "string" || group === "compaction") return body;
  const { compaction_tokens_min: _min, compaction_tokens_max: _max, ...rest } = body;
  return rest;
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
    const ref = modelRef(d.modelName, d.maxTokens, d.temperature, "Name the model");
    if (typeof ref === "string") return ref;
    model = ref;
  }

  const compaction = parsedCompaction(compactionOf(d));
  if (compaction === null) {
    return firstProblem(checkCompaction(compactionOf(d))) ?? "Check the compaction settings.";
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

  // Sent only when changed: left out, the brain keeps what it holds, so a Save
  // from another group, or a tab opened before someone else's change, never
  // writes over them.
  const improvement = improvementRequest(d, stored);
  if (typeof improvement === "string") return improvement;

  return {
    model,
    search,
    sandbox,
    browser,
    improvement: d.improvement,
    ...improvement,
    compaction_messages: compaction.messages,
    compaction_tokens_min: compaction.least,
    compaction_tokens_max: compaction.most,
    // Whole, as the rest is: null sends the team back to the built-in orchestrator.
    hub: d.hub?.name ?? null,
  };
}

/** A model as the brain takes it, or the sentence saying why it cannot. */
function modelRef(
  nameTyped: string,
  maxTyped: string,
  temperatureTyped: string,
  nameIt: string,
): ModelRef | string {
  const name = nameTyped.trim();
  if (name === "") return `${nameIt}, as your model gateway names it.`;
  const maxTokens = wholeNumber(maxTyped);
  if (maxTokens === null || maxTokens < 1) return "Max tokens must be a whole number above 0.";
  const ref: ModelRef = { name, max_tokens: maxTokens };
  if (temperatureTyped.trim() !== "") {
    const t = Number(temperatureTyped.trim());
    if (!Number.isFinite(t) || t < 0 || t > 2) return "Temperature must be between 0 and 2.";
    ref.temperature = t;
  }
  return ref;
}

/**
 * The judge model, the sample share, the daily passes and a pass's tokens and
 * minutes, each only when it differs from what the brain holds. A judge model
 * of null sends the team back to judging on its own model.
 */
function improvementRequest(
  d: SettingsDraft,
  stored: TeamSettings,
):
  | Pick<
      TeamSettingsRequest,
      | "judge_model"
      | "improvement_sample_share"
      | "improvement_daily_passes"
      | "improvement_pass_tokens"
      | "improvement_pass_minutes"
    >
  | string {
  const base = draftOf(stored);
  const out: Pick<
    TeamSettingsRequest,
    | "judge_model"
    | "improvement_sample_share"
    | "improvement_daily_passes"
    | "improvement_pass_tokens"
    | "improvement_pass_minutes"
  > = {};
  const judgeChanged = (
    ["judgeOn", "judgeName", "judgeMaxTokens", "judgeTemperature"] as const
  ).some((f) => d[f] !== base[f]);
  if (judgeChanged) {
    if (d.judgeOn) {
      const ref = modelRef(
        d.judgeName,
        d.judgeMaxTokens,
        d.judgeTemperature,
        "Name the judge model",
      );
      if (typeof ref === "string") return ref;
      out.judge_model = ref;
    } else {
      out.judge_model = null;
    }
  }
  if (parseWhole(d.sampleShare) !== parseWhole(base.sampleShare)) {
    const share = wholeNumber(d.sampleShare);
    if (share === null || share > 100) return "The share must be a whole number from 0 to 100.";
    out.improvement_sample_share = share;
  }
  if (parseWhole(d.dailyPasses) !== parseWhole(base.dailyPasses)) {
    const passes = wholeNumber(d.dailyPasses);
    if (passes === null || passes < 1 || passes > 200) {
      return "Passes a day must be a whole number from 1 to 200.";
    }
    out.improvement_daily_passes = passes;
  }
  if (parseWhole(d.passTokens) !== parseWhole(base.passTokens)) {
    const tokens = parseWhole(d.passTokens);
    if (tokens === null || tokens < 100_000 || tokens > 2_000_000) {
      return "Tokens a pass must be a whole number from 100,000 to 2,000,000.";
    }
    out.improvement_pass_tokens = tokens;
  }
  if (parseWhole(d.passMinutes) !== parseWhole(base.passMinutes)) {
    const minutes = wholeNumber(d.passMinutes);
    if (minutes === null || minutes < 1 || minutes > 25) {
      return "Minutes a try must be a whole number from 1 to 25.";
    }
    out.improvement_pass_minutes = minutes;
  }
  return out;
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

type SectionProps = Omit<Parameters<typeof Section>[0], "title" | "about" | "children">;

/** The ids of the fields, in the order a Save that stops focuses them. */
const COMPACTION_IDS = {
  messages: "settings-compaction",
  least: "settings-compaction-least",
  most: "settings-compaction-most",
} as const;

/**
 * When a run compacts: past a count of messages, or once its next request
 * would read a tenth of its budget, held between two lines. Nothing is said
 * while a field is typed in: a field is checked when the reader leaves it, and
 * all of them when Save is pressed, which focuses the first that is wrong.
 */
function CompactionSection({
  draft,
  onChange,
  section,
}: {
  draft: CompactionDraft;
  onChange: (patch: Partial<CompactionDraft>) => void;
  section: SectionProps;
}) {
  const [left, setLeft] = useState<Set<keyof CompactionDraft>>(() => new Set());
  const problems = checkCompaction(draft);
  // A field speaks once the reader has left it; a refused Save speaks for all.
  const said = (f: keyof CompactionDraft) =>
    left.has(f) || section.problem ? problems[f] : undefined;
  const pair =
    (left.has("least") && left.has("most")) || section.problem ? problems.pair : undefined;
  const sentence = explain(draft);
  const pairId = "settings-compaction-pair";
  const rangeId = "settings-compaction-range";

  const leave = (f: keyof CompactionDraft) => {
    setLeft((s) => new Set(s).add(f));
    const tidy = tidied(draft[f]);
    if (tidy !== draft[f]) onChange({ [f]: tidy });
  };

  const save = () => {
    const bad = firstBad(problems);
    if (bad) {
      setLeft(new Set(["messages", "least", "most"]));
      document.getElementById(COMPACTION_IDS[bad])?.focus();
    }
    section.onSave();
  };

  return (
    <Section
      title="Compaction"
      about="When a run folds its earlier turns into a summary, so a long task keeps fitting. A run reads these when it starts, and again when it resumes."
      {...section}
      onSave={save}
    >
      <Field
        label="Messages before a run compacts"
        htmlFor={COMPACTION_IDS.messages}
        hint={`${grouped(MESSAGES.min)} to ${grouped(MESSAGES.max)}.`}
        error={said("messages")}
      >
        <Input
          id={COMPACTION_IDS.messages}
          inputMode="numeric"
          autoComplete="off"
          className="max-w-40 tabular-nums"
          value={draft.messages}
          onChange={(e) => onChange({ messages: e.target.value })}
          onBlur={() => leave("messages")}
        />
      </Field>

      <fieldset className="min-w-0 space-y-2">
        <legend className="text-sm font-medium">
          Tokens a request may read before it compacts
        </legend>
        <div className="grid max-w-md gap-3 sm:grid-cols-2">
          {(["least", "most"] as const).map((f) => (
            <Field
              key={f}
              label={f === "least" ? "At least" : "At most"}
              htmlFor={COMPACTION_IDS[f]}
              error={said(f)}
            >
              <TokensInput
                id={COMPACTION_IDS[f]}
                value={draft[f]}
                aria-invalid={pair ? true : undefined}
                aria-describedby={[rangeId, pair ? pairId : null].filter(Boolean).join(" ")}
                onChange={(value) => onChange({ [f]: value })}
                onBlur={() => leave(f)}
              />
            </Field>
          ))}
        </div>
        <p id={rangeId} className="text-xs text-muted-foreground">
          {grouped(TOKENS.min)} to {grouped(TOKENS.max)} each.
        </p>
        {pair ? (
          <p id={pairId} className="text-xs text-destructive">
            {pair}
          </p>
        ) : sentence ? (
          <p className="max-w-prose text-sm">{sentence}</p>
        ) : null}
      </fieldset>

      {section.canWrite && !isDefault(draft) ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="-ml-3"
          onClick={() => {
            setLeft(new Set());
            // The button goes once the fields hold the defaults; keep focus in the section.
            document.getElementById(COMPACTION_IDS.messages)?.focus();
            onChange({
              messages: String(DEFAULTS.messages),
              least: grouped(DEFAULTS.least),
              most: grouped(DEFAULTS.most),
            });
          }}
        >
          Use the defaults
        </Button>
      ) : null}
    </Section>
  );
}

/** The first field a refused Save focuses: messages, then at least, then at most. */
function firstBad(p: CompactionProblems): keyof typeof COMPACTION_IDS | null {
  if (p.messages) return "messages";
  if (p.least || p.pair) return "least";
  if (p.most) return "most";
  return null;
}

/**
 * A count of tokens: typed with or without grouping, its unit inside the
 * field. The aria props Field joins to it land on the input itself.
 */
function TokensInput({
  onChange,
  ...input
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  onBlur: () => void;
  "aria-invalid"?: boolean;
  "aria-describedby"?: string;
}) {
  return (
    <div className="relative">
      <Input
        {...input}
        inputMode="numeric"
        autoComplete="off"
        className="pr-16 tabular-nums"
        onChange={(e) => onChange(e.target.value)}
      />
      <span
        aria-hidden="true"
        className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-muted-foreground"
      >
        tokens
      </span>
    </div>
  );
}

const JUDGES: readonly { value: "team" | "own"; label: string }[] = [
  { value: "team", label: "Same as the team's model" },
  { value: "own", label: "A different model" },
];

const GROUPS: Group[] = [
  "model",
  "hub",
  "compaction",
  "search",
  "sandbox",
  "browser",
  "improvement",
];

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
      </Section>

      <CompactionSection
        draft={compactionOf(draft)}
        onChange={(patch) =>
          set("compaction", {
            ...(patch.messages === undefined ? {} : { compaction: patch.messages }),
            ...(patch.least === undefined ? {} : { tokensMin: patch.least }),
            ...(patch.most === undefined ? {} : { tokensMax: patch.most }),
          })
        }
        section={props("compaction")}
      />

      <Section
        title="Web search"
        about="Where the team's agents search the web, when they are given it."
        {...props("search")}
      >
        <Field label="Provider" htmlFor="settings-provider">
          <Choice
            id="settings-provider"
            value={draft.provider}
            options={PROVIDERS}
            onChange={(provider) => set("search", { provider, key: "", dropKey: false })}
          />
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
          <Choice
            id="settings-sandbox"
            value={draft.sandbox}
            options={SANDBOXES}
            onChange={(sandbox) =>
              // The address and key are the old provider's.
              set("sandbox", { sandbox, sandboxURL: "", sandboxTemplate: "", sandboxKey: "" })
            }
          />
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
          <Choice
            id="settings-browser"
            value={draft.browser}
            options={BROWSERS}
            onChange={(browser) =>
              // The address and key are the old provider's.
              set("browser", { browser, browserURL: "", browserKey: "", dropBrowserKey: false })
            }
          />
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
        title="Improvement"
        about="How the team's agents improve from their runs."
        {...props("improvement")}
      >
        <SwitchField
          label="Improve agents from their runs"
          description="While this is on, each finished run is judged in the background on whether the task was done, and members can mark an answer right or wrong. When one weakness recurs for an agent in three runs, or once a day, the improver proposes changes for an approver to decide."
          checked={draft.improvement}
          onCheckedChange={(improvement) => set("improvement", { improvement })}
        />
        {draft.improvement ? (
          <>
            <Field
              label="Judge model"
              htmlFor="settings-judge"
              hint="The model that judges each run, and studies the ones worth improving from."
            >
              <Choice
                id="settings-judge"
                value={draft.judgeOn ? "own" : "team"}
                options={JUDGES}
                onChange={(v) => set("improvement", { judgeOn: v === "own" })}
              />
            </Field>
            {draft.judgeOn ? (
              <div className="grid gap-3 sm:grid-cols-3">
                <Field
                  label="Judge's model name"
                  htmlFor="settings-judge-model"
                  hint="As the model gateway names it."
                >
                  <Input
                    id="settings-judge-model"
                    value={draft.judgeName}
                    onChange={(e) => set("improvement", { judgeName: e.target.value })}
                  />
                </Field>
                <Field label="Judge's max tokens" htmlFor="settings-judge-max-tokens">
                  <Input
                    id="settings-judge-max-tokens"
                    inputMode="numeric"
                    value={draft.judgeMaxTokens}
                    onChange={(e) => set("improvement", { judgeMaxTokens: e.target.value })}
                  />
                </Field>
                <Field
                  label="Judge's temperature"
                  htmlFor="settings-judge-temperature"
                  hint="0 to 2; empty for the model's own."
                >
                  <Input
                    id="settings-judge-temperature"
                    inputMode="decimal"
                    value={draft.judgeTemperature}
                    onChange={(e) => set("improvement", { judgeTemperature: e.target.value })}
                  />
                </Field>
              </div>
            ) : null}
            <div className="grid items-start gap-3 sm:grid-cols-2">
              <Field
                label="Share of good runs to study"
                htmlFor="settings-sample-share"
                hint="Runs judged bad are always studied. A share of the good ones is too, so agents keep what works. Each one studied costs another model call."
              >
                <div className="relative max-w-40">
                  <Input
                    id="settings-sample-share"
                    inputMode="numeric"
                    autoComplete="off"
                    className="pr-9 tabular-nums"
                    value={draft.sampleShare}
                    onChange={(e) => set("improvement", { sampleShare: e.target.value })}
                  />
                  <span
                    aria-hidden="true"
                    className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-muted-foreground"
                  >
                    %
                  </span>
                </div>
              </Field>
              <Field
                label="Passes a day, at most"
                htmlFor="settings-daily-passes"
                hint="Each pass the improver makes costs model calls. This caps how many it makes for the team in a day, from 1 to 200."
              >
                <Input
                  id="settings-daily-passes"
                  inputMode="numeric"
                  autoComplete="off"
                  className="max-w-40 tabular-nums"
                  value={draft.dailyPasses}
                  onChange={(e) => set("improvement", { dailyPasses: e.target.value })}
                />
              </Field>
              <Field
                label="Tokens a pass, at most"
                htmlFor="settings-pass-tokens"
                hint="What a pass may spend across its tries, screening included, before it stops and reports what it found. From 100,000 to 2,000,000."
              >
                <Input
                  id="settings-pass-tokens"
                  inputMode="numeric"
                  autoComplete="off"
                  className="max-w-40 tabular-nums"
                  value={draft.passTokens}
                  onChange={(e) => set("improvement", { passTokens: e.target.value })}
                  onBlur={(e) => set("improvement", { passTokens: tidied(e.target.value) })}
                />
              </Field>
              <Field
                label="Minutes a try, at most"
                htmlFor="settings-pass-minutes"
                hint="How long one try at a pass may run before it stops and reports what it found. A pass that cannot reach its model is tried up to three times. From 1 to 25."
              >
                <Input
                  id="settings-pass-minutes"
                  inputMode="numeric"
                  autoComplete="off"
                  className="max-w-40 tabular-nums"
                  value={draft.passMinutes}
                  onChange={(e) => set("improvement", { passMinutes: e.target.value })}
                />
              </Field>
            </div>
          </>
        ) : null}
      </Section>

      <ApprovalSections teamId={teamId} canWrite={canWrite} />
    </div>
  );
}

/* ---------------------------------------------------------- tool approvals */

/** The modes from loosest to strictest, each with what it asks before. */
const APPROVAL_MODES: { value: ApprovalMode; label: string; about: string }[] = [
  { value: "off", label: "Off", about: "Agents never ask, and the rules below are off too." },
  {
    value: "destructive",
    label: "Destructive calls only",
    about:
      "Agents ask before a call that may delete or send something. Opening a page, clicking and typing are not asked.",
  },
  {
    value: "writes",
    label: "Writes and destructive calls",
    about:
      "Agents ask before any call that changes something, clicking or typing in a web page included.",
  },
  {
    value: "all",
    label: "Every call",
    about: "Agents ask before every call, reading a page or searching the web too.",
  },
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
      <ApprovalRulesSection teamId={teamId} canWrite={canWrite} mode={stored.data?.mode} />
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
  const labelId = useId();

  const save = useMutation({
    mutationFn: (s: ApprovalSettings) => saveApprovalSettings(teamId, s),
    onSuccess: (next) => {
      qc.setQueryData(["approval-settings", teamId], next);
      setSaved(true);
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const edited = () => {
    setSaved(false);
    setProblem(null);
  };

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
      <div className="space-y-1.5">
        <p id={labelId} className="text-sm font-medium">
          Ask before
        </p>
        <RadioGroup.Root
          aria-labelledby={labelId}
          value={mode}
          onValueChange={(v) => {
            setMode(v as ApprovalMode);
            edited();
          }}
          className="divide-y divide-rule overflow-hidden rounded-md border border-border"
        >
          {APPROVAL_MODES.map((m) => {
            const id = `approval-mode-${m.value}`;
            return (
              <label
                key={m.value}
                htmlFor={id}
                className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors duration-(--dur-press) ease-out hover:bg-muted has-[:disabled]:cursor-default has-[:disabled]:hover:bg-transparent"
              >
                <RadioGroup.Item
                  id={id}
                  value={m.value}
                  aria-labelledby={`${id}-label`}
                  aria-describedby={`${id}-about`}
                  className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border border-input transition-colors duration-(--dur-press) ease-out outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 data-[state=checked]:border-primary"
                >
                  <RadioGroup.Indicator className="size-2 rounded-full bg-primary" />
                </RadioGroup.Item>
                <span className="min-w-0 space-y-0.5">
                  <span id={`${id}-label`} className="block text-sm font-medium">
                    {m.label}
                  </span>
                  <span id={`${id}-about`} className="block text-xs text-muted-foreground">
                    {m.about}
                  </span>
                </span>
              </label>
            );
          })}
        </RadioGroup.Root>
      </div>
      <Field
        label="Minutes a request waits"
        htmlFor="settings-approval-ttl"
        hint={`${TTL_MIN} to ${TTL_MAX}. After that it expires and the run carries on without it.`}
      >
        <WaitInput
          id="settings-approval-ttl"
          value={ttl}
          readout={valid ? waitWords(minutes) : null}
          onChange={(v) => {
            setTtl(v);
            edited();
          }}
        />
      </Field>
      <p className="flex gap-2 text-xs text-muted-foreground">
        <Info aria-hidden className="mt-px size-3.5 shrink-0" />
        <span>
          Unless approvals are off, a run for a guest asks before any call that is not a read, and a
          run with nobody there to answer, such as a scheduled one, is refused a call it would have
          asked about.
        </span>
      </p>
    </Section>
  );
}

/** The wait in minutes, with what it comes to beside it. */
function WaitInput({
  id,
  value,
  readout,
  onChange,
  ...aria
}: {
  id: string;
  value: string;
  readout: string | null;
  onChange: (next: string) => void;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        className="w-24 tabular-nums"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        {...aria}
      />
      <span className="text-sm text-muted-foreground">
        minutes{readout && readout !== `${Number(value)} minutes` ? ` · ${readout}` : ""}
      </span>
    </div>
  );
}

/** A rule's answer: its words, and the icon and colour it is shown in. */
const RULE_ANSWERS: {
  value: ApprovalRuleMode;
  label: string;
  short: string;
  icon: typeof Check;
  tone: string;
}[] = [
  {
    value: "allow",
    label: "Allow without asking",
    short: "Allow",
    icon: Check,
    tone: "text-succeeded",
  },
  { value: "ask", label: "Always ask", short: "Ask", icon: Clock, tone: "text-warning" },
  { value: "deny", label: "Never allow", short: "Never", icon: Ban, tone: "text-destructive" },
];
/**
 * A rule's answer as shown. One this build does not know is shown as asking,
 * since that is what the brain does with it (`verdictOf`).
 */
function answerOf(m: ApprovalRuleMode) {
  const ask = RULE_ANSWERS.find((a) => a.value === "ask");
  return RULE_ANSWERS.find((a) => a.value === m) ?? (ask as (typeof RULE_ANSWERS)[number]);
}

/** The brain's own tools a rule may name: those the approval gate stands before. */
const BUILT_INS: ChoiceGroup<string>[] = [
  {
    label: "Web",
    options: [
      { value: "web_search", label: "web_search" },
      { value: "web_fetch", label: "web_fetch" },
    ],
  },
  {
    label: "Browser",
    options: [
      {
        value: "browser_open",
        label: "browser_open",
        hint: "Asked when a page opens, and covers what is then done on that page's own site.",
      },
      { value: "browser_snapshot", label: "browser_snapshot" },
      { value: "browser_click", label: "browser_click" },
      { value: "browser_type", label: "browser_type" },
      { value: "browser_screenshot", label: "browser_screenshot" },
    ],
  },
];
const BUILT_IN_NAMES = BUILT_INS.flatMap((g) => g.options.map((o) => o.value));
// Not a UUID, so no server's id can be it.
const BUILT_IN: Picked = { id: ":built-in", name: "Built-in tools" };

/** The servers a rule may name, searched by name; a tool rule may also name a built-in. */
async function ruleServers(teamId: string, q: string, builtIns: boolean) {
  const page = await listMCPServers(teamId, null, { q, limit: PICK_PAGE });
  const items: Picked[] = page.items.map((s) => ({ id: s.id, name: s.name }));
  const wanted = q.trim().toLowerCase();
  const matches =
    wanted === "" ||
    "built-in tools".includes(wanted) ||
    BUILT_IN_NAMES.some((name) => name.includes(wanted.replaceAll(" ", "_")));
  if (builtIns && matches) items.unshift(BUILT_IN);
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
  // Each option carries its target and its label, so neither is cut from the other.
  const options: ChoiceGroup<string>[] = builtIn
    ? BUILT_INS
    : [
        {
          label: "",
          options: (tools.data?.items ?? []).map((t) => ({
            value: `${server?.name}__${t.name}`,
            label: t.name,
          })),
        },
      ];
  const empty = options.every((g) => g.options.length === 0);
  const hint = !server ? (
    "Choose a server first."
  ) : builtIn ? (
    (chosen(BUILT_INS, value)?.hint ??
    "Tools every agent can be given, such as reading a web page.")
  ) : tools.isPending ? (
    "Reading the server's tools…"
  ) : tools.isError ? (
    <>
      Couldn't read this server's tools.{" "}
      <button
        type="button"
        className="underline underline-offset-2 hover:text-foreground"
        onClick={() => void tools.refetch()}
      >
        Try again
      </button>
    </>
  ) : empty ? (
    "This server has said it has no tools."
  ) : undefined;
  return (
    <Field label="Tool" htmlFor="rule-target" hint={hint}>
      <Choice
        id="rule-target"
        value={value}
        options={options}
        placeholder="Choose a tool"
        disabled={empty}
        onChange={onChange}
      />
    </Field>
  );
}

/** A rule's target as a person reads it: the tool, and where it is from. */
function ruleSubject(kind: string, target: string): { name: string; from: string; mono: boolean } {
  if (kind === "server") return { name: target, from: "Every tool of this server", mono: false };
  const cut = target.indexOf("__");
  if (cut > 0) {
    return { name: target.slice(cut + 2), from: `on ${target.slice(0, cut)}`, mono: true };
  }
  return { name: target, from: "Built-in", mono: true };
}

const RULE_SCOPES = [
  { value: "tool", label: "One tool" },
  { value: "server", label: "Every tool of a server" },
] as const;

function ApprovalRulesSection({
  teamId,
  canWrite,
  mode: teamMode,
}: {
  teamId: string;
  canWrite: boolean;
  /** The team's mode, once read: with approvals off, no rule applies. */
  mode?: ApprovalMode;
}) {
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
        // Only an answer that asks has someone to answer it.
        require_other: mode === "ask" && other,
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
      about="A standing answer for one tool, or every tool of a server, whatever the mode. A tool's own rule wins over its server's."
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
      {teamMode === "off" ? (
        <p className="flex gap-2 text-sm">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
          <span>
            Approvals are off, so no rule applies until a mode is chosen under Tool approvals.
          </span>
        </p>
      ) : null}
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
        <ul aria-label="Tool approval rules" className="divide-y divide-rule">
          {items.map((r) => {
            const subject = ruleSubject(r.target_kind, r.target);
            const answer = answerOf(r.mode);
            const Icon = answer.icon;
            return (
              <li
                key={r.id}
                data-rule={r.target}
                className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 first:pt-0"
              >
                <div className="min-w-0 flex-1 basis-48">
                  <p className={cn("truncate", subject.mono ? "font-mono text-xs" : "text-sm")}>
                    {subject.name}
                  </p>
                  <p className="text-xs text-muted-foreground">{subject.from}</p>
                </div>
                <div className="min-w-0 basis-40">
                  <p className="flex items-center gap-1.5 text-sm">
                    <Icon aria-hidden className={cn("size-3.5 shrink-0", answer.tone)} />
                    {answer.label}
                  </p>
                  {r.require_other ? (
                    <p className="text-xs text-muted-foreground">Someone else answers</p>
                  ) : null}
                </div>
                {canWrite ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="ml-auto"
                    disabled={remove.isPending}
                    aria-label={`Remove the rule for ${r.target}`}
                    onClick={() => remove.mutate(r.id)}
                  >
                    Remove
                  </Button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {canWrite ? (
        <div className="space-y-4 rounded-md bg-surface p-4">
          <h3 className="text-sm font-medium">New rule</h3>
          <Segments
            label="Applies to"
            value={kind}
            options={RULE_SCOPES}
            onChange={(next) => {
              setKind(next);
              setServer(null);
              setTarget("");
            }}
          />
          <div className={cn("grid gap-3", kind === "tool" && "sm:grid-cols-2")}>
            <div className="min-w-0 space-y-1.5">
              <label
                id="rule-server-label"
                htmlFor="rule-server"
                className="block text-sm font-medium"
              >
                Server
              </label>
              <Picker
                label="Server"
                noun="server"
                max={1}
                field={{
                  id: "rule-server",
                  labelId: "rule-server-label",
                  placeholder: "Choose a server",
                }}
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
          </div>
          <div className="space-y-1.5">
            <p className="text-sm font-medium">Answer</p>
            <Segments
              label="Answer"
              value={mode}
              options={RULE_ANSWERS.map((a) => ({ ...a, label: a.short }))}
              onChange={(next) => {
                setMode(next);
                // Someone else's answer belongs to a rule that asks.
                if (next !== "ask") setOther(false);
              }}
            />
          </div>
          {mode === "ask" ? (
            <SwitchField
              label="Someone else must answer"
              description="The person the run works for may not approve their own request."
              checked={other}
              onCheckedChange={setOther}
            />
          ) : null}
          <Button type="submit" size="sm" loading={add.isPending}>
            Add rule
          </Button>
        </div>
      ) : null}
    </Section>
  );
}
