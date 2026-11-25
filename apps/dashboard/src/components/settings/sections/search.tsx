import type { TeamSettings } from "@/api/types";
import { Choice } from "@/components/choices";
import { Field } from "@/components/registry-bits";
import { Section, type SectionProps } from "@/components/settings/section";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { KEY_REQUIRED, PROVIDERS, SELF_HOSTED } from "@/lib/team-settings/catalogue";
import type { SettingsDraft } from "@/lib/team-settings/draft";

/** Where the team's agents search the web. */
export function SearchSection({
  stored,
  draft,
  set,
  section,
}: {
  stored: TeamSettings;
  draft: SettingsDraft;
  set: (patch: Partial<SettingsDraft>) => void;
  section: SectionProps;
}) {
  const selfHosted = SELF_HOSTED.includes(draft.provider);
  const takesKey = KEY_REQUIRED.includes(draft.provider) || draft.provider === "firecrawl-oss";
  const keyKept = stored.search.key_set && stored.search.provider === draft.provider;

  return (
    <Section
      title="Web search"
      about="Where the team's agents search the web, when they are given it."
      {...section}
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
          onChange={(provider) => set({ provider, key: "", dropKey: false })}
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
        <SwitchField
          label="Run without a key"
          checked={draft.dropKey}
          onCheckedChange={(dropKey) => set({ dropKey, key: "" })}
        />
      ) : null}
    </Section>
  );
}
