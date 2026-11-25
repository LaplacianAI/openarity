import { Field } from "@/components/registry-bits";
import { Section, type SectionProps } from "@/components/settings/section";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import type { SettingsDraft } from "@/lib/team-settings/draft";

/** The model the hub and the team's built-in agents run on. */
export function ModelSection({
  draft,
  set,
  section,
}: {
  draft: SettingsDraft;
  set: (patch: Partial<SettingsDraft>) => void;
  section: SectionProps;
}) {
  return (
    <Section
      title="Model"
      about="The model the hub and the team's built-in agents run on."
      {...section}
    >
      <SwitchField
        label="Use a model"
        description="The model is what the hub and the team's built-in agents think with. Without one they cannot answer."
        checked={draft.modelOn}
        onCheckedChange={(modelOn) => set({ modelOn })}
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
              onChange={(e) => set({ modelName: e.target.value })}
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
              onChange={(e) => set({ maxTokens: e.target.value })}
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
              onChange={(e) => set({ temperature: e.target.value })}
            />
          </Field>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          No model — the built-in agents do not run, and messages are stored unanswered.
        </p>
      )}
    </Section>
  );
}
