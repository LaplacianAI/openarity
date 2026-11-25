import type { ModelRef, TeamSettings, TeamSettingsRequest } from "@/api/types";
import { checkCompaction, firstProblem, parsedCompaction, parseWhole } from "@/lib/compaction";
import { efficiencyRequest } from "@/lib/efficiency-settings";
import { replayRequest } from "@/lib/replay-settings";
import { BROWSER_URL, KEY_REQUIRED, SELF_HOSTED } from "@/lib/team-settings/catalogue";
import {
  compactionOf,
  draftOf,
  FIELDS,
  type Group,
  type SettingsDraft,
  wholeNumber,
  withFields,
} from "@/lib/team-settings/draft";
import { workspaceRequest } from "@/lib/team-settings/workspace";

/** The brain's own rule: a name, or owner/name for another team's public template. */
const E2B_TEMPLATE = /^[a-z0-9][a-z0-9_.-]*(\/[a-z0-9][a-z0-9_.-]*)?$/;

/** What the brain holds, with one group's fields taken from the draft. */
function withGroup(group: Group, d: SettingsDraft, stored: TeamSettings): SettingsDraft {
  return withFields(draftOf(stored), d, FIELDS[group]);
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

  const life = workspaceRequest(d, stored);
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
