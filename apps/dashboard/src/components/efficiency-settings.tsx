import { Choice } from "@/components/choices";
import { Field } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { Unit } from "@/components/unit-input";
import {
  EFFICIENCY,
  type EfficiencyDraft,
  efficiencyProblems,
  MARGINS,
  WASTE_SIGNS,
} from "@/lib/efficiency-settings";

/**
 * The weekly efficiency pass: its switch, and once it is on, the numbers that
 * decide which agents it looks at and how strictly their changes are watched.
 * Each says what it does in plain words, for a reader new to it. What is
 * wrong with a number is said under it once a Save was refused.
 */
export function EfficiencyFields({
  draft,
  onChange,
  showProblems,
}: {
  draft: EfficiencyDraft;
  onChange: (patch: Partial<EfficiencyDraft>) => void;
  showProblems: boolean;
}) {
  const problems = showProblems ? efficiencyProblems(draft) : {};
  return (
    <div className="space-y-4 border-t border-rule pt-4">
      <SwitchField
        label="Also make good runs cheaper and faster"
        description="Once a week, on Sunday morning (UTC), the improver looks at agents whose runs go well but whose costliest runs spend far more tokens or time than their usual one. It proposes the smallest change that would bring those down, never a bigger model or more access, and an approver decides. An agent with a weakness still open, or a change still waiting or being watched, is left alone until that is settled."
        checked={draft.efficiency}
        onCheckedChange={(efficiency) => onChange({ efficiency })}
      />
      {draft.efficiency ? (
        <div className="grid items-start gap-3 sm:grid-cols-2">
          <Field
            label="Good runs needed"
            htmlFor="settings-eff-runs"
            hint={`An agent is looked at only once it has had this many runs judged good in the days below. More runs make the comparison fairer; fewer let a quiet agent be looked at sooner. From ${EFFICIENCY.runs.min} to ${EFFICIENCY.runs.max}.`}
            error={problems.effRuns}
          >
            <Input
              id="settings-eff-runs"
              inputMode="numeric"
              autoComplete="off"
              className="max-w-40 tabular-nums"
              value={draft.effRuns}
              onChange={(e) => onChange({ effRuns: e.target.value })}
            />
          </Field>
          <Field
            label="Days to look back"
            htmlFor="settings-eff-days"
            hint={`How far back the weekly look counts runs and compares the costly ones with the cheap. From ${EFFICIENCY.days.min} to ${EFFICIENCY.days.max} days.`}
            error={problems.effDays}
          >
            <Unit
              id="settings-eff-days"
              unit="days"
              value={draft.effDays}
              onChange={(effDays) => onChange({ effDays })}
            />
          </Field>
          <Field
            label="Costliest runs, against the usual"
            htmlFor="settings-eff-spread"
            hint="An agent is looked at when its costliest tenth of good runs costs at least this many times its usual run, in tokens or in time. 2 means twice. From 1.25 to 10."
            error={problems.effSpread}
          >
            <Unit
              id="settings-eff-spread"
              unit="×"
              decimal
              value={draft.effSpread}
              onChange={(effSpread) => onChange({ effSpread })}
            />
          </Field>
          <Field
            label="Runs a sign of waste needs"
            htmlFor="settings-eff-marks"
            hint={`A sign of waste is something a run's record shows by itself: ${WASTE_SIGNS}. An agent is also looked at when one sign shows in this many good runs. From ${EFFICIENCY.markRuns.min} to ${EFFICIENCY.markRuns.max}, and no more than the good runs needed.`}
            error={problems.effMarkRuns}
          >
            <Input
              id="settings-eff-marks"
              inputMode="numeric"
              autoComplete="off"
              className="max-w-40 tabular-nums"
              value={draft.effMarkRuns}
              onChange={(e) => onChange({ effMarkRuns: e.target.value })}
            />
          </Field>
          <Field
            label="Watch these changes more strictly"
            htmlFor="settings-eff-margin"
            hint="Every applied change is watched over its next 10 runs and flagged for a person if too many go wrong. A change meant only to save should make nothing worse, so it can be flagged sooner. It is also flagged when the saving it promised does not show. A change to this applies to changes still being watched."
          >
            <Choice
              id="settings-eff-margin"
              value={draft.effMargin}
              options={MARGINS}
              onChange={(effMargin) => onChange({ effMargin })}
            />
          </Field>
        </div>
      ) : null}
    </div>
  );
}
