import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";

import { getTeamSettings, listAgents, saveTeamSettings } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { SearchProvider, TeamSettings, TeamSettingsRequest } from "@/api/types";
import { type Picked, Picker } from "@/components/picker";
import { Field, ReadOnlyNote } from "@/components/registry-bits";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/settings")({
  component: Settings,
});

const WRITE = "team:settings";

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
        description="For this team only: the model its built-in agents run on, how they search the web, and what they learn. Each team has its own."
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="settings" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its settings." />
      ) : settings.isPending ? (
        <p className="text-sm text-muted-foreground">Loading settings…</p>
      ) : settings.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {settings.error.message}
        </p>
      ) : (
        // Keyed on the save time, so a save resets the form to what the brain
        // now holds — the typed key included.
        <SettingsForm
          key={settings.data.updated_at ?? "never"}
          teamId={teamId}
          stored={settings.data}
          canWrite={canWrite}
        />
      )}
    </div>
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

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3 rounded-md border border-border bg-card p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      {children}
    </section>
  );
}

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
  const [problem, setProblem] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const set = (patch: Partial<SettingsDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const selfHosted = SELF_HOSTED.includes(draft.provider);
  const takesKey = KEY_REQUIRED.includes(draft.provider) || draft.provider === "firecrawl-oss";
  const keyKept = stored.search.key_set && stored.search.provider === draft.provider;

  // A plain call rather than useMutation: a mutation keeps its variables in
  // the query client's cache, and they can hold the key.
  const submit = async (body: TeamSettingsRequest) => {
    setPending(true);
    setProblem(null);
    try {
      const saved = await saveTeamSettings(teamId, body);
      qc.setQueryData(["settings", teamId], saved);
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        const body = settingsRequest(draft, stored);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        void submit(body);
      }}
    >
      <fieldset disabled={!canWrite} className="space-y-4">
        <Section title="Model">
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={draft.modelOn}
              onChange={(e) => set({ modelOn: e.target.checked })}
            />
            Use a model
          </label>
          {draft.modelOn ? (
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Model" htmlFor="settings-model" hint="As the model gateway names it.">
                <Input
                  id="settings-model"
                  value={draft.modelName}
                  placeholder="anthropic/claude-opus-5-5"
                  onChange={(e) => set({ modelName: e.target.value })}
                />
              </Field>
              <Field label="Max tokens" htmlFor="settings-max-tokens">
                <Input
                  id="settings-max-tokens"
                  inputMode="numeric"
                  value={draft.maxTokens}
                  onChange={(e) => set({ maxTokens: e.target.value })}
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
                  onChange={(e) => set({ temperature: e.target.value })}
                />
              </Field>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              No model — the built-in agents do not run, and messages are stored unanswered.
            </p>
          )}
        </Section>

        <Section title="Hub">
          <p className="text-xs text-muted-foreground">
            Every message reaches the hub first. Leave empty for the built-in orchestrator.
          </p>
          {draft.hub === null ? <p className="text-sm">The built-in orchestrator</p> : null}
          <Picker
            label="Hub"
            noun="agent"
            max={1}
            value={draft.hub ? [draft.hub] : []}
            onChange={(next) => set({ hub: next[0] ?? null })}
            search={(q) =>
              listAgents(teamId, null, { q, limit: PICK_PAGE }).then((page) => ({
                items: page.items.map((a) => ({ id: a.id, name: a.name })),
                more: Boolean(page.next_cursor),
              }))
            }
            searchKey={["agents", teamId]}
          />
        </Section>

        <Section title="Web search">
          <Field label="Provider" htmlFor="settings-provider">
            <select
              id="settings-provider"
              className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={draft.provider}
              onChange={(e) =>
                set({ provider: e.target.value as SearchProvider, key: "", dropKey: false })
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
                onChange={(e) => set({ url: e.target.value })}
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
                onChange={(e) => set({ key: e.target.value, dropKey: false })}
              />
            </Field>
          ) : null}
          {draft.provider === "firecrawl-oss" && keyKept ? (
            <label className="flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                checked={draft.dropKey}
                onChange={(e) => set({ dropKey: e.target.checked, key: "" })}
              />
              Run without a key
            </label>
          ) : null}
        </Section>

        <Section title="Learning">
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={draft.learning}
              onChange={(e) => set({ learning: e.target.checked })}
            />
            Learn from each run
          </label>
          <p className="text-xs text-muted-foreground">
            After a run, the learner writes what it taught. Nothing it writes is used until someone
            with learning:approve approves it.
          </p>
        </Section>

        <Section title="Compaction">
          <Field
            label="Messages before a transcript compacts"
            htmlFor="settings-compaction"
            hint={`${MIN_COMPACTION} to ${MAX_COMPACTION}. Stored now; compaction uses it from a later release.`}
          >
            <Input
              id="settings-compaction"
              inputMode="numeric"
              value={draft.compaction}
              onChange={(e) => set({ compaction: e.target.value })}
            />
          </Field>
        </Section>
      </fieldset>

      {problem ? (
        <p role="alert" className="text-xs text-destructive">
          {problem}
        </p>
      ) : null}

      {canWrite ? (
        <Button type="submit" disabled={pending}>
          {pending ? "Saving…" : "Save"}
        </Button>
      ) : null}
    </form>
  );
}
