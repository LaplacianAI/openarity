import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ReactNode, useId, useState } from "react";

import { getTeamSettings, listAgents, saveTeamSettings } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { SearchProvider, TeamSettings, TeamSettingsRequest } from "@/api/types";
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

export const Route = createFileRoute("/ui/settings")({
  component: Settings,
});

const WRITE = "team:settings";

/** The finding when the team has no model: nothing answers its messages. */
export const NO_MODEL = "The hub can't run until a model is set.";

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
        description="For this team only: the model its built-in agents run on, how they search the web, and what they learn. Each team has its own."
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
  learning: boolean;
  compaction: string;
  /** The chosen hub agent; null for the built-in orchestrator. */
  hub: Picked | null;
};

export function draftOf(s: TeamSettings): SettingsDraft {
  return {
    modelOn: s.model !== null,
    modelName: s.model?.name ?? "",
    maxTokens: s.model ? String(s.model.max_tokens) : "4096",
    temperature: s.model?.temperature === undefined ? "" : String(s.model.temperature),
    provider: s.search.provider,
    url: s.search.url ?? "",
    key: "",
    dropKey: false,
    learning: s.learning,
    compaction: String(s.compaction_messages),
    hub: s.hub,
  };
}

/** The groups of the page, each saved on its own. */
export type Group = "model" | "hub" | "search" | "learning";

/** Which of the draft's fields each group owns. */
const FIELDS: Record<Group, (keyof SettingsDraft)[]> = {
  model: ["modelOn", "modelName", "maxTokens", "temperature"],
  hub: ["hub", "compaction"],
  search: ["provider", "url", "key", "dropKey"],
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

  return {
    model,
    search,
    learning: d.learning,
    compaction_messages: compaction,
    // Whole, as the rest is: null sends the team back to the built-in orchestrator.
    hub: d.hub?.name ?? null,
  };
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

const GROUPS: Group[] = ["model", "hub", "search", "learning"];

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
          hint={`${MIN_COMPACTION} to ${MAX_COMPACTION}. Stored now; compaction uses it from a later release.`}
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
    </div>
  );
}
