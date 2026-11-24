import type {
  BrowserProvider,
  ModelRef,
  SandboxProvider,
  SearchProvider,
  TeamSettings,
  TeamSettingsRequest,
} from "@/api/types";
import type { Picked } from "@/components/picker";
import {
  type CompactionDraft,
  checkCompaction,
  firstProblem,
  grouped,
  parsedCompaction,
  parseWhole,
} from "@/lib/compaction";
import {
  type EfficiencyDraft,
  efficiencyDraftOf,
  efficiencyRequest,
  spreadOf,
} from "@/lib/efficiency-settings";
import {
  REPLAY_NUMBERS,
  type ReplayDraft,
  replayDraftOf,
  replayRequest,
} from "@/lib/replay-settings";

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

/** The brain's own rule: a name, or owner/name for another team's public template. */
const E2B_TEMPLATE = /^[a-z0-9][a-z0-9_.-]*(\/[a-z0-9][a-z0-9_.-]*)?$/;

/** How many agents one search for the hub offers. */
export const PICK_PAGE = 20;

type DraftFields = EfficiencyDraft & {
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
  /** Whether workspaces get the team's own size, not the provider's. */
  sizeCustom: boolean;
  /** Memory and disk in GB, CPUs whole, as typed. */
  sizeMemory: string;
  sizeCPUs: string;
  sizeDisk: string;
  /** Whether a workspace left running stops after the team's own time, not the deployment's. */
  idleOwn: boolean;
  idleMinutes: string;
  keepHours: string;
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
  /** The judged runs a daily pass waits for, as typed. */
  dailyRuns: string;
  /** Messages, and the token lines, as typed: compared by value. */
  compaction: string;
  tokensMin: string;
  tokensMax: string;
  /** The chosen hub agent; null for the built-in orchestrator. */
  hub: Picked | null;
};

export type SettingsDraft = DraftFields & ReplayDraft;

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
    ...lifeOf(s.sandbox),
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
    dailyRuns: String(s.improvement_daily_runs ?? 5),
    ...efficiencyDraftOf(s),
    ...replayDraftOf(s),
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

/** The draft's workspace fields, in the order the page shows them. */
const LIFE = [
  "sizeCustom",
  "sizeMemory",
  "sizeCPUs",
  "sizeDisk",
  "idleOwn",
  "idleMinutes",
  "keepHours",
] as const satisfies readonly (keyof SettingsDraft)[];
const LIFE_SET = new Set<keyof SettingsDraft>(LIFE);

/** Which of the draft's fields each group owns. */
export const FIELDS: Record<Group, (keyof SettingsDraft)[]> = {
  model: ["modelOn", "modelName", "maxTokens", "temperature"],
  hub: ["hub"],
  compaction: ["compaction", "tokensMin", "tokensMax"],
  search: ["provider", "url", "key", "dropKey"],
  sandbox: ["sandbox", "sandboxURL", "sandboxTemplate", "sandboxKey", ...LIFE],
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
    "dailyRuns",
    "efficiency",
    "effRuns",
    "effDays",
    "effSpread",
    "effMarkRuns",
    "effMargin",
    "replay",
    "replayRuns",
    "replayTokens",
    "replayMinutes",
    "replayDaily",
  ],
};

export function same(a: SettingsDraft[keyof SettingsDraft], b: SettingsDraft[keyof SettingsDraft]) {
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
  "dailyRuns",
  "effRuns",
  "effDays",
  "effMarkRuns",
  "replayRuns",
  "replayTokens",
  "replayMinutes",
  "replayDaily",
]);

/** The efficiency pass's numbers: hidden, and never sent, while it is off. */
const EFF_NUMBERS: ReadonlySet<keyof SettingsDraft> = new Set([
  "effRuns",
  "effDays",
  "effSpread",
  "effMarkRuns",
  "effMargin",
]);

/** Trying on past runs' numbers: hidden, and never sent, while it is off. */
const REPLAY_SET: ReadonlySet<keyof SettingsDraft> = new Set(REPLAY_NUMBERS);

/** Whether a group's fields differ from what the brain holds. */
export function isDirty(group: Group, d: SettingsDraft, stored: TeamSettings): boolean {
  const base = draftOf(stored);
  const changed = FIELDS[group].some((f) =>
    LIFE_SET.has(f) || (EFF_NUMBERS.has(f) && !d.efficiency) || (REPLAY_SET.has(f) && !d.replay)
      ? false
      : f === "effSpread"
        ? spreadOf(d.effSpread) !== spreadOf(base.effSpread)
        : BY_VALUE.has(f)
          ? (parseWhole(d[f] as string) ?? d[f]) !== (parseWhole(base[f] as string) ?? base[f])
          : !same(d[f], base[f]),
  );
  // A workspace's times and size are compared by what they mean: a size
  // typed and then put back to the provider's is no change.
  return changed || (group === "sandbox" && !sameLife(d, base));
}

/** The compaction fields of a draft, as lib/compaction checks them. */
export function compactionOf(d: SettingsDraft): CompactionDraft {
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

export function wholeNumber(raw: string): number | null {
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
      | "improvement_daily_runs"
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
    | "improvement_daily_runs"
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
  if (parseWhole(d.dailyRuns) !== parseWhole(base.dailyRuns)) {
    const runs = wholeNumber(d.dailyRuns);
    if (runs === null || runs < 1 || runs > 100) {
      return "Runs before a daily pass must be a whole number from 1 to 100.";
    }
    out.improvement_daily_runs = runs;
  }
  const efficiency = efficiencyRequest(d, base);
  if (typeof efficiency === "string") return efficiency;
  const replay = replayRequest(d, base);
  if (typeof replay === "string") return replay;
  return { ...out, ...efficiency, ...replay };
}

/** What the page starts a size from when the team has none of its own. */
const SIZE_START = { memory_mb: 2048, cpus: 2, disk_mb: 10240 };
const IDLE_START = 30;
/** What a brain that predates stopping keeps a stopped workspace for. */
const KEEP_HOURS = 24;

function lifeOf(s: TeamSettings["sandbox"] | undefined) {
  const size = s?.workspace_size ?? null;
  const shown = size ?? SIZE_START;
  return {
    sizeCustom: size !== null,
    sizeMemory: gbOf(shown.memory_mb),
    sizeCPUs: String(shown.cpus),
    sizeDisk: gbOf(shown.disk_mb),
    idleOwn: s?.idle_minutes != null,
    idleMinutes: String(s?.idle_minutes ?? IDLE_START),
    keepHours: String(s?.keep_hours ?? KEEP_HOURS),
  };
}

/** MB as the GB the page shows: 1536 is 1.5. */
function gbOf(mb: number): string {
  return String(Number((mb / 1024).toFixed(2)));
}

/** GB as typed, in MB; null when it is not a number. */
function mbOf(raw: string): number | null {
  const t = raw.trim();
  return /^\d+(\.\d+)?$/.test(t) || /^\.\d+$/.test(t) ? Math.round(Number(t) * 1024) : null;
}

/** A workspace's times and size by what they mean, a typo kept as typed so it reads as a change. */
function lifeKey(d: SettingsDraft) {
  return {
    keep: parseWhole(d.keepHours) ?? d.keepHours.trim(),
    idle: d.idleOwn ? (parseWhole(d.idleMinutes) ?? d.idleMinutes.trim()) : null,
    size: d.sizeCustom
      ? [
          mbOf(d.sizeMemory) ?? d.sizeMemory.trim(),
          parseWhole(d.sizeCPUs) ?? d.sizeCPUs.trim(),
          mbOf(d.sizeDisk) ?? d.sizeDisk.trim(),
        ]
      : null,
  };
}

/** Whether saving would give the team's workspaces another size. */
export function sizeChanged(d: SettingsDraft, stored: TeamSettings): boolean {
  return JSON.stringify(lifeKey(d).size) !== JSON.stringify(lifeKey(draftOf(stored)).size);
}

/** Whether saving would move the team's workspaces to another sandbox. */
export function providerChanged(d: SettingsDraft, stored: TeamSettings): boolean {
  const was = stored.sandbox?.provider ?? "none";
  return was !== "none" && d.sandbox !== was;
}

/** The stored keep hours when the draft lowers them, else null. */
export function keepLowered(d: SettingsDraft, stored: TeamSettings): number | null {
  const was = stored.sandbox?.keep_hours ?? KEEP_HOURS;
  const now = parseWhole(d.keepHours);
  return now !== null && now >= 1 && now < was ? was : null;
}

function sameLife(a: SettingsDraft, b: SettingsDraft): boolean {
  return JSON.stringify(lifeKey(a)) === JSON.stringify(lifeKey(b));
}

/** The workspace fields of the sandbox a Save sends. */
type Life = Pick<
  NonNullable<TeamSettingsRequest["sandbox"]>,
  "keep_hours" | "idle_minutes" | "workspace_size"
>;

/**
 * A workspace's times and size, each sent only when it changed: left out, the
 * brain keeps what it holds, so a Save made in a tab opened earlier never
 * writes over them.
 */
function lifeRequest(d: SettingsDraft, stored: TeamSettings): Life | string {
  const out: Life = {};
  const was = lifeKey(draftOf(stored));
  const now = lifeKey(d);

  if (JSON.stringify(now.size) !== JSON.stringify(was.size)) {
    if (!d.sizeCustom) {
      out.workspace_size = null;
    } else {
      const memory = mbOf(d.sizeMemory);
      const cpus = parseWhole(d.sizeCPUs);
      const disk = mbOf(d.sizeDisk);
      if (memory === null || memory < 256 || memory > 65536) {
        return "Memory is between 0.25 and 64 GB.";
      }
      if (cpus === null || cpus < 1 || cpus > 16) return "CPUs is a whole number from 1 to 16.";
      if (disk === null || disk < 256 || disk > 262144) return "Disk is between 0.25 and 256 GB.";
      out.workspace_size = { memory_mb: memory, cpus, disk_mb: disk };
    }
  }
  if (now.idle !== was.idle) {
    if (!d.idleOwn) {
      out.idle_minutes = null;
    } else {
      const minutes = parseWhole(d.idleMinutes);
      if (minutes === null || minutes < 1 || minutes > 1440) {
        return "Stop when idle is a whole number of minutes, from 1 to 1,440.";
      }
      out.idle_minutes = minutes;
    }
  }
  if (now.keep !== was.keep) {
    const hours = parseWhole(d.keepHours);
    if (hours === null || hours < 1 || hours > 720) {
      return "Keep stopped is a whole number of hours, from 1 to 720.";
    }
    out.keep_hours = hours;
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

  const life = lifeRequest(d, stored);
  if (typeof life === "string") return life;
  Object.assign(sandbox, life);

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
