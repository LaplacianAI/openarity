import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { listAgents, saveTeamSettings } from "@/api";
import type { SandboxTemplateBuild, TeamSettings } from "@/api/types";
import { Choice, Segments } from "@/components/choices";
import { EfficiencyFields } from "@/components/efficiency-settings";
import { Picker } from "@/components/picker";
import { Field } from "@/components/registry-bits";
import { ReplayFields } from "@/components/replay-settings";
import { Notice, Section, type SectionProps } from "@/components/settings/section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { Unit } from "@/components/unit-input";
import { LeaveGuard } from "@/components/unsaved";
import {
  type CompactionDraft,
  type CompactionProblems,
  checkCompaction,
  DEFAULTS,
  explain,
  grouped,
  isDefault,
  MESSAGES,
  TOKENS,
  tidied,
} from "@/lib/compaction";
import {
  BROWSER_URL,
  BROWSERS,
  compactionOf,
  draftOf,
  FIELDS,
  type Group,
  groupRequest,
  isDirty,
  KEY_REQUIRED,
  keepLowered,
  PICK_PAGE,
  PROVIDERS,
  providerChanged,
  SANDBOXES,
  SELF_HOSTED,
  type SettingsDraft,
  sizeChanged,
} from "@/lib/team-settings";

const SIZES = [
  { value: "provider", label: "Provider's" },
  { value: "custom", label: "Custom" },
] as const;
const IDLE = [
  { value: "deployment", label: "Deployment's" },
  { value: "own", label: "After a time" },
] as const;

/** What a workspace's size will be, for the sandbox and size in the draft. */
function sizeHint(draft: SettingsDraft): string {
  if (draft.sandbox === "e2b" && draft.sandboxTemplate.trim() !== "") {
    return "Your own template sets each workspace's size, so a size here is not used.";
  }
  if (draft.sandbox === "e2b") {
    return draft.sizeCustom
      ? "E2B builds a template at this size in your account first, and new workspaces wait a few minutes for it. A size your E2B plan does not allow shows as a failed build. Disk is the free space E2B makes room for."
      : "Each conversation's workspace gets 2 CPUs and 2 GB, the size the default template is built at.";
  }
  if (!draft.sizeCustom) {
    return "Each conversation's workspace gets the size your sandbox gives one.";
  }
  const daytona =
    draft.sandbox === "daytona" ? " Daytona rounds memory and disk up to a whole GB." : "";
  return `Each conversation's workspace gets this. A size past what your sandbox allows a workspace is refused when the workspace is made.${daytona}`;
}

/**
 * How big a conversation's workspace is, and how long it lives: it stops when
 * the conversation's last run ends, and keeps its files while stopped.
 */
function Workspaces({
  draft,
  sizeChanged,
  providerChanged,
  keepLowered,
  onChange,
}: {
  draft: SettingsDraft;
  /** Whether saving would change the size, which gives every conversation a new workspace. */
  sizeChanged: boolean;
  /** Whether saving would move to another sandbox, which does the same. */
  providerChanged: boolean;
  /** The stored keep hours, when the draft lowers them. */
  keepLowered: number | null;
  onChange: (patch: Partial<SettingsDraft>) => void;
}) {
  const ownTemplate = draft.sandbox === "e2b" && draft.sandboxTemplate.trim() !== "";
  return (
    <div className="space-y-4 border-t border-rule pt-4">
      <h3 className="text-sm font-medium">Workspaces</h3>
      {providerChanged ? (
        <Notice>
          Saving gives each conversation a new workspace in this sandbox, without the files in its
          old one.
        </Notice>
      ) : null}
      <fieldset className="space-y-2" aria-describedby="settings-size-hint">
        <legend className="mb-1.5 text-sm font-medium">Size</legend>
        <Segments
          label="Size"
          value={draft.sizeCustom ? "custom" : "provider"}
          options={SIZES}
          onChange={(v) => onChange({ sizeCustom: v === "custom" })}
        />
        {draft.sizeCustom ? (
          <div className="grid gap-3 pt-1 sm:grid-cols-3">
            <Field label="Memory" htmlFor="settings-size-memory">
              <Unit
                id="settings-size-memory"
                unit="GB"
                decimal
                value={draft.sizeMemory}
                onChange={(sizeMemory) => onChange({ sizeMemory })}
              />
            </Field>
            <Field label="CPUs" htmlFor="settings-size-cpus">
              <Unit
                id="settings-size-cpus"
                unit="CPUs"
                value={draft.sizeCPUs}
                onChange={(sizeCPUs) => onChange({ sizeCPUs })}
              />
            </Field>
            <Field label="Disk" htmlFor="settings-size-disk">
              <Unit
                id="settings-size-disk"
                unit="GB"
                decimal
                value={draft.sizeDisk}
                onChange={(sizeDisk) => onChange({ sizeDisk })}
              />
            </Field>
          </div>
        ) : null}
        <p id="settings-size-hint" className="text-xs text-muted-foreground">
          {sizeHint(draft)}
        </p>
        {sizeChanged && !providerChanged && !ownTemplate ? (
          <Notice>
            Saving gives each conversation a new workspace at this size, without the files in its
            old one.
          </Notice>
        ) : null}
      </fieldset>
      <fieldset className="space-y-2" aria-describedby="settings-idle-hint">
        <legend className="mb-1.5 text-sm font-medium">Stop when idle</legend>
        <div className="flex flex-wrap items-center gap-3">
          <Segments
            label="Stop when idle"
            value={draft.idleOwn ? "own" : "deployment"}
            options={IDLE}
            onChange={(v) => onChange({ idleOwn: v === "own" })}
          />
          {draft.idleOwn ? (
            <Unit
              id="settings-idle-minutes"
              aria-label="Minutes idle"
              unit="minutes"
              value={draft.idleMinutes}
              onChange={(idleMinutes) => onChange({ idleMinutes })}
            />
          ) : null}
        </div>
        <p id="settings-idle-hint" className="text-xs text-muted-foreground">
          {draft.idleOwn
            ? "A workspace stops when its conversation's last run ends. This stops one left running, such as one waiting for an approval, after 1 to 1,440 minutes unused."
            : "A workspace stops when its conversation's last run ends. One left running, such as one waiting for an approval, stops after the time your deployment sets."}
        </p>
      </fieldset>
      <Field
        label="Keep stopped"
        htmlFor="settings-keep-hours"
        hint="A stopped workspace keeps its files for the conversation's next run, which starts it again. After this long it is deleted with them, workspaces stopped before a change included. From 1 to 720 hours."
      >
        <Unit
          id="settings-keep-hours"
          unit="hours"
          value={draft.keepHours}
          onChange={(keepHours) => onChange({ keepHours })}
        />
      </Field>
      {keepLowered !== null ? (
        <Notice>
          Lowering it from {keepLowered} hours deletes workspaces already stopped longer than{" "}
          {draft.keepHours.trim()}, with their files, at the next sweep.
        </Notice>
      ) : null}
    </div>
  );
}

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
        hint={`Once a run's conversation is this many messages long, its earlier turns are folded into a summary. ${grouped(MESSAGES.min)} to ${grouped(MESSAGES.max)}.`}
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

/** How far the brain's build of the room template in the team's E2B account is. */
function TemplateBuildStatus({ build }: { build: SandboxTemplateBuild | null }) {
  if (build === null) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        The room template is built in your E2B account when the sandbox is saved, or when the first
        room starts.
      </p>
    );
  }
  const text =
    build.status === "ready"
      ? "Room template built: rooms start from it."
      : build.status === "failed"
        ? `The room template failed to build: ${build.error ?? "E2B gave no reason"}. It is tried again in ten minutes, or when you save.`
        : "Building the room template in your E2B account. This takes a few minutes; until it is done, agents are told to try again.";
  return (
    <p
      className={
        build.status === "failed" ? "text-sm text-destructive" : "text-sm text-muted-foreground"
      }
      role="status"
    >
      {text} <span className="font-mono text-xs">{build.name}</span>
    </p>
  );
}

export function SettingsForm({
  teamId,
  stored,
  canWrite,
  groups,
}: {
  teamId: string;
  stored: TeamSettings;
  canWrite: boolean;
  /** The groups this page shows; the others are another page's. */
  groups: readonly Group[];
}) {
  const has = (g: Group) => groups.includes(g);
  const qc = useQueryClient();
  const [draft, setDraft] = useState<SettingsDraft>(() => draftOf(stored));
  const [problem, setProblem] = useState<{ group: Group; text: string } | null>(null);
  const [pending, setPending] = useState<Group | null>(null);
  const [saved, setSaved] = useState<Group | null>(null);
  // Once a save of the improvement section is refused, each efficiency
  // field shows its own error until a save goes through: fixing one clears
  // only that one's.
  const [effChecked, setEffChecked] = useState(false);

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
      if (group === "improvement") setEffChecked(true);
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
      if (group === "improvement") setEffChecked(false);
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
      <LeaveGuard dirty={() => groups.some(dirty)} what="the team settings" />

      {has("model") ? (
        <Section
          title="Model"
          about="The model the hub and the team's built-in agents run on."
          {...props("model")}
        >
          <SwitchField
            label="Use a model"
            description="The model is what the hub and the team's built-in agents think with. Without one they cannot answer."
            checked={draft.modelOn}
            onCheckedChange={(modelOn) => set("model", { modelOn })}
          />
          {draft.modelOn ? (
            <div className="grid gap-3 sm:grid-cols-3">
              <Field
                label="Model"
                htmlFor="settings-model"
                hint="Its name exactly as your model gateway lists it."
              >
                <Input
                  id="settings-model"
                  value={draft.modelName}
                  onChange={(e) => set("model", { modelName: e.target.value })}
                />
              </Field>
              <Field
                label="Max tokens"
                htmlFor="settings-max-tokens"
                hint="The longest reply the model may write in one go. A token is about three quarters of a word."
              >
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
                hint="How varied its wording is: 0 is the most predictable, 2 the most varied. Leave empty for the model's own."
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
      ) : null}

      {has("hub") ? (
        <Section
          title="The hub"
          about="Every message to the team reaches the hub first, and the hub answers it or hands the work to the agent best placed to. Leave empty for the built-in orchestrator, which does this for you."
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
      ) : null}

      {has("compaction") ? (
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
      ) : null}

      {has("search") ? (
        <Section
          title="Web search"
          about="Where the team's agents search the web, when they are given it."
          {...props("search")}
        >
          <Field
            label="Provider"
            htmlFor="settings-provider"
            hint="The search service an agent uses when it looks something up on the web. Off turns web search off for every agent."
          >
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
      ) : null}

      {has("sandbox") ? (
        <Section
          title="Sandbox"
          about="Where the team's command MCP servers and its agents' workspaces run. A command server's sandbox reaches only the network its server lists."
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
              hint="Empty to start rooms from the image the brain builds in your E2B account. Name a template of your own only to replace it."
            >
              <Input
                id="settings-sandbox-template"
                spellCheck={false}
                placeholder="Built by the brain"
                value={draft.sandboxTemplate}
                onChange={(e) => set("sandbox", { sandboxTemplate: e.target.value })}
              />
            </Field>
          ) : null}
          {draft.sandbox === "e2b" &&
          stored.sandbox?.provider === "e2b" &&
          !stored.sandbox.template &&
          draft.sandboxTemplate.trim() === "" ? (
            <TemplateBuildStatus build={stored.sandbox.template_build ?? null} />
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
          {draft.sandbox !== "none" ? (
            <Workspaces
              draft={draft}
              sizeChanged={sizeChanged(draft, stored)}
              providerChanged={providerChanged(draft, stored)}
              keepLowered={keepLowered(draft, stored)}
              onChange={(patch) => set("sandbox", patch)}
            />
          ) : null}
        </Section>
      ) : null}

      {has("browser") ? (
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
              onCheckedChange={(dropBrowserKey) =>
                set("browser", { dropBrowserKey, browserKey: "" })
              }
            />
          ) : null}
        </Section>
      ) : null}

      {has("improvement") ? (
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
                    hint="Its name exactly as your model gateway lists it."
                  >
                    <Input
                      id="settings-judge-model"
                      value={draft.judgeName}
                      onChange={(e) => set("improvement", { judgeName: e.target.value })}
                    />
                  </Field>
                  <Field
                    label="Judge's max tokens"
                    htmlFor="settings-judge-max-tokens"
                    hint="The longest answer the judge may write about one run. A verdict is a few sentences."
                  >
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
                    hint="0 to 2; low keeps its verdicts steady. Leave empty for the model's own."
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
                <Field
                  label="Runs before a daily pass, at least"
                  htmlFor="settings-daily-runs"
                  hint="The daily pass looks at an agent once it has had this many judged runs since its last pass; one with fewer waits for more. From 1 to 100."
                >
                  <Input
                    id="settings-daily-runs"
                    inputMode="numeric"
                    autoComplete="off"
                    className="max-w-40 tabular-nums"
                    value={draft.dailyRuns}
                    onChange={(e) => set("improvement", { dailyRuns: e.target.value })}
                  />
                </Field>
              </div>
              <EfficiencyFields
                draft={draft}
                onChange={(patch) => set("improvement", patch)}
                showProblems={effChecked}
              />
              <ReplayFields
                draft={draft}
                onChange={(patch) => set("improvement", patch)}
                showProblems={effChecked}
              />
            </>
          ) : null}
        </Section>
      ) : null}
    </div>
  );
}
