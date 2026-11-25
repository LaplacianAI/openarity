import { Choice } from "@/components/choices";
import { EfficiencyFields } from "@/components/efficiency-settings";
import { Field } from "@/components/registry-bits";
import { ReplayFields } from "@/components/replay-settings";
import { Section, type SectionProps } from "@/components/settings/section";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { tidied } from "@/lib/compaction";
import type { SettingsDraft } from "@/lib/team-settings/draft";

const JUDGES: readonly { value: "team" | "own"; label: string }[] = [
  { value: "team", label: "Same as the team's model" },
  { value: "own", label: "A different model" },
];

/** How the team's agents improve from their runs. */
export function ImprovementSection({
  draft,
  set,
  section,
  effChecked,
}: {
  draft: SettingsDraft;
  set: (patch: Partial<SettingsDraft>) => void;
  section: SectionProps;
  /** A Save of this section was refused: each efficiency field shows its own error. */
  effChecked: boolean;
}) {
  return (
    <Section
      title="Improvement"
      about="How the team's agents improve from their runs."
      {...section}
    >
      <SwitchField
        label="Improve agents from their runs"
        description="While this is on, each finished run is judged in the background on whether the task was done, and members can mark an answer right or wrong. When one weakness recurs for an agent in three runs, or once a day, the improver proposes changes for an approver to decide."
        checked={draft.improvement}
        onCheckedChange={(improvement) => set({ improvement })}
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
              onChange={(v) => set({ judgeOn: v === "own" })}
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
                  onChange={(e) => set({ judgeName: e.target.value })}
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
                  onChange={(e) => set({ judgeMaxTokens: e.target.value })}
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
                  onChange={(e) => set({ judgeTemperature: e.target.value })}
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
                  onChange={(e) => set({ sampleShare: e.target.value })}
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
                onChange={(e) => set({ dailyPasses: e.target.value })}
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
                onChange={(e) => set({ passTokens: e.target.value })}
                onBlur={(e) => set({ passTokens: tidied(e.target.value) })}
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
                onChange={(e) => set({ passMinutes: e.target.value })}
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
                onChange={(e) => set({ dailyRuns: e.target.value })}
              />
            </Field>
          </div>
          <EfficiencyFields
            draft={draft}
            onChange={(patch) => set(patch)}
            showProblems={effChecked}
          />
          <ReplayFields draft={draft} onChange={(patch) => set(patch)} showProblems={effChecked} />
        </>
      ) : null}
    </Section>
  );
}
