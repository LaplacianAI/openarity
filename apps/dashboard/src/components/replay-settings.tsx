import { Field } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { Unit } from "@/components/unit-input";
import { REPLAY, type ReplayDraft, replayProblems } from "@/lib/replay-settings";

/**
 * Trying each proposed change on past runs before anyone decides on it: its
 * switch, and once it is on, what a try may spend. Each says what it does in
 * plain words, for a reader new to it. What is wrong with a number is said
 * under it once a Save was refused.
 */
export function ReplayFields({
  draft,
  onChange,
  showProblems,
}: {
  draft: ReplayDraft;
  onChange: (patch: Partial<ReplayDraft>) => void;
  showProblems: boolean;
}) {
  const problems = showProblems ? replayProblems(draft) : {};
  return (
    <div className="space-y-4 border-t border-rule pt-4">
      <SwitchField
        label="Try each change on past runs first"
        description="Before you decide on a proposed change, it is tried on a few of this agent's past runs and judged the same way its real runs are, so you can see whether it would have helped. Nothing is sent or changed while it is tried: a step the past run took gets the answer it got then, a new look-up runs for real, and anything that would change or send something is skipped. A web search counts as a look-up only when a member was there for the past run; for a guest's run, or one nobody was there for, it is skipped too. Each past run tried costs about as many tokens as the run did, plus a little to judge it."
        checked={draft.replay}
        onCheckedChange={(replay) => onChange({ replay })}
      />
      {draft.replay ? (
        <div className="grid items-start gap-3 sm:grid-cols-2">
          <Field
            label="Past runs to try it on"
            htmlFor="settings-replay-runs"
            hint={`Half are the runs the change was proposed from, half recent runs that went well, so you see both what it fixes and what it might break. More runs tell you more and cost more. From ${REPLAY.runs.min} to ${REPLAY.runs.max}.`}
            error={problems.replayRuns}
          >
            <Input
              id="settings-replay-runs"
              inputMode="numeric"
              autoComplete="off"
              className="max-w-40 tabular-nums"
              value={draft.replayRuns}
              onChange={(e) => onChange({ replayRuns: e.target.value })}
            />
          </Field>
          <Field
            label="Tokens for one try"
            htmlFor="settings-replay-tokens"
            hint={`The most one try may spend, checked as each reply from the model comes back: the reply that goes past it still counts, and the try stops there. A try that needs more is stopped and counted as “could not tell”, so a change that makes runs run away cannot run up the bill. Judging the try is not counted here. From ${REPLAY.tokens.min.toLocaleString("en-US")} to ${REPLAY.tokens.max.toLocaleString("en-US")}.`}
            error={problems.replayTokens}
          >
            <Unit
              id="settings-replay-tokens"
              unit="tokens"
              value={draft.replayTokens}
              onChange={(replayTokens) => onChange({ replayTokens })}
            />
          </Field>
          <Field
            label="Minutes for one try"
            htmlFor="settings-replay-minutes"
            hint={`The longest one try may take, stopped the same way past it. From ${REPLAY.minutes.min} to ${REPLAY.minutes.max} minutes.`}
            error={problems.replayMinutes}
          >
            <Unit
              id="settings-replay-minutes"
              unit="min"
              value={draft.replayMinutes}
              onChange={(replayMinutes) => onChange({ replayMinutes })}
            />
          </Field>
          <Field
            label="Tries a day"
            htmlFor="settings-replay-daily"
            hint={`The most past runs the whole team tries in a day (UTC). Past it, changes proposed that day are shown without being tried. From ${REPLAY.daily.min} to ${REPLAY.daily.max}.`}
            error={problems.replayDaily}
          >
            <Input
              id="settings-replay-daily"
              inputMode="numeric"
              autoComplete="off"
              className="max-w-40 tabular-nums"
              value={draft.replayDaily}
              onChange={(e) => onChange({ replayDaily: e.target.value })}
            />
          </Field>
        </div>
      ) : null}
    </div>
  );
}
