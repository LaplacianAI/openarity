/**
 * The schedule form and the watcher form, as a new one is made and as one is
 * changed. The draft is the caller's; a form only changes it. Where one
 * answers and what a watcher watches are fixed once made, so editing leaves
 * them out.
 */
import { type ReactNode, useId } from "react";

import { Choice, Segments } from "@/components/choices";
import { Field, TextArea } from "@/components/registry-bits";
import { AgentField, PlacePicker, UNITS, WhenField, ZoneField } from "@/components/schedule-fields";
import { Input } from "@/components/ui/input";
import { MAX_OBJECTIVE, type ScheduleDraft, type WatcherDraft } from "@/lib/schedule-draft";

/** One part: its name and what it decides on the left from 1024px, the fields on the right. */
export function FormSection({
  title,
  about,
  children,
}: {
  title: string;
  about: ReactNode;
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
      <div className="min-w-0 max-w-2xl space-y-4">{children}</div>
    </section>
  );
}

/** A count from 90% of the cap, so a long objective is warned of before it is refused. */
function objectiveLimit(text: string): string | null {
  const n = [...text.trim()].length;
  return n >= MAX_OBJECTIVE * 0.9 ? `${n} of ${MAX_OBJECTIVE}` : null;
}

function NameAndObjective({
  prefix,
  name,
  objective,
  set,
  hint,
}: {
  prefix: string;
  name: string;
  objective: string;
  set: (patch: { name?: string; objective?: string }) => void;
  hint: string;
}) {
  return (
    <>
      <Field label="Name" htmlFor={`${prefix}-name`} hint="Short, and unique among your own.">
        <Input
          id={`${prefix}-name`}
          value={name}
          maxLength={80}
          autoComplete="off"
          onChange={(e) => set({ name: e.target.value })}
        />
      </Field>
      <Field
        label="What each run does"
        htmlFor={`${prefix}-objective`}
        hint={hint}
        limit={objectiveLimit(objective)}
      >
        <TextArea
          id={`${prefix}-objective`}
          value={objective}
          className="min-h-32"
          onChange={(e) => set({ objective: e.target.value })}
        />
      </Field>
    </>
  );
}

export function ScheduleForm({
  teamId,
  draft,
  set,
  editing = false,
}: {
  teamId: string;
  draft: ScheduleDraft;
  set: (patch: Partial<ScheduleDraft>) => void;
  editing?: boolean;
}) {
  const id = useId();
  return (
    <div>
      <FormSection
        title="What it does"
        about="Each run is given the objective, whole, as its instruction."
      >
        <NameAndObjective
          prefix={id}
          name={draft.name}
          objective={draft.objective}
          set={set}
          hint="Say what to do and where to look, as you would to a person. It runs as you, with what you may use."
        />
      </FormSection>
      <FormSection
        title="When"
        about="Once, every so often, or on a cron rule. The next times show as you type."
      >
        <WhenField teamId={teamId} draft={draft} set={set} />
        <ZoneField
          id={`${id}-zone`}
          value={draft.zone}
          onChange={(zone) => set({ zone })}
          hint="A cron rule and a once are read in this zone; times are shown in yours."
        />
        {editing && draft.mode === "once" && !draft.date ? (
          <p className="text-xs text-muted-foreground">
            Leave the day empty to keep the time it has.
          </p>
        ) : null}
      </FormSection>
      <FormSection
        title="Who runs it"
        about="The hub hands work to the agent that fits, or one agent runs it alone."
      >
        <AgentField teamId={teamId} value={draft.agent} onChange={(agent) => set({ agent })} />
      </FormSection>
      {editing ? null : (
        <FormSection
          title="Where it answers"
          about="Fixed once it is made. To move it, delete it and make another."
        >
          <PlacePicker
            teamId={teamId}
            value={draft.target}
            label={draft.targetLabel}
            onChange={(target, targetLabel) => set({ target, targetLabel })}
          />
        </FormSection>
      )}
    </div>
  );
}

export function WatcherForm({
  teamId,
  draft,
  set,
  editing = false,
}: {
  teamId: string;
  draft: WatcherDraft;
  set: (patch: Partial<WatcherDraft>) => void;
  editing?: boolean;
}) {
  const id = useId();
  const looks = draft.source !== "webhook";
  return (
    <div>
      <FormSection
        title="What it does"
        about="Each run is given the objective, and what changed or what was sent."
      >
        <NameAndObjective
          prefix={id}
          name={draft.name}
          objective={draft.objective}
          set={set}
          hint="Say what to do with a change, as you would to a person. It runs as you."
        />
      </FormSection>
      <FormSection
        title="What it watches"
        about={
          editing
            ? "Fixed once it is made. To watch something else, make another."
            : "A page, a feed, or a call to its own address."
        }
      >
        {editing ? null : (
          <Segments
            label="What it watches"
            value={draft.source}
            options={[
              { value: "web_page", label: "A page" },
              { value: "feed", label: "A feed" },
              { value: "webhook", label: "A webhook" },
            ]}
            onChange={(source) => set({ source })}
          />
        )}
        {looks && !editing ? (
          <Field
            label={draft.source === "feed" ? "Feed address" : "Page address"}
            htmlFor={`${id}-url`}
          >
            <Input
              id={`${id}-url`}
              inputMode="url"
              autoComplete="off"
              spellCheck={false}
              placeholder="https://"
              value={draft.url}
              onChange={(e) => set({ url: e.target.value })}
            />
          </Field>
        ) : null}
        {draft.source === "web_page" ? (
          <Field
            label="Only this part (optional)"
            htmlFor={`${id}-selector`}
            hint="A CSS selector, such as #prices. Only what it matches is compared; left empty, the whole page is."
          >
            <Input
              id={`${id}-selector`}
              className="font-mono"
              spellCheck={false}
              autoComplete="off"
              value={draft.selector}
              onChange={(e) => set({ selector: e.target.value })}
            />
          </Field>
        ) : null}
        {draft.source === "webhook" && !editing ? (
          <p className="text-sm text-muted-foreground">
            Once made, it shows its address and a secret, once. Give both to the service that calls
            it, such as GitHub.
          </p>
        ) : null}
        {looks ? (
          <Length
            id={`${id}-every`}
            label="How often it looks"
            hint="Left empty, the team's shortest interval."
            value={draft.every}
            unit={draft.everyUnit}
            onChange={(every, everyUnit) => set({ every, everyUnit })}
          />
        ) : null}
        <Length
          id={`${id}-cooldown`}
          label="Cooldown"
          hint="How long after a run a change waits before it runs again: 0 for none, up to 7 days. An hour unless given."
          value={draft.cooldown}
          unit={draft.cooldownUnit}
          onChange={(cooldown, cooldownUnit) => set({ cooldown, cooldownUnit })}
        />
      </FormSection>
      <FormSection
        title="Who runs it"
        about="The hub hands work to the agent that fits, or one agent runs it alone."
      >
        <AgentField teamId={teamId} value={draft.agent} onChange={(agent) => set({ agent })} />
      </FormSection>
      {editing ? null : (
        <FormSection title="Where it answers" about="Fixed once it is made.">
          <PlacePicker
            teamId={teamId}
            value={draft.target}
            label={draft.targetLabel}
            onChange={(target, targetLabel) => set({ target, targetLabel })}
          />
        </FormSection>
      )}
    </div>
  );
}

function Length({
  id,
  label,
  hint,
  value,
  unit,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: string;
  unit: WatcherDraft["everyUnit"];
  onChange: (value: string, unit: WatcherDraft["everyUnit"]) => void;
}) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <div className="flex flex-wrap items-center gap-3">
        <Input
          id={id}
          inputMode="numeric"
          autoComplete="off"
          className="w-24 tabular-nums"
          aria-describedby={`${id}-hint`}
          value={value}
          onChange={(e) => onChange(e.target.value, unit)}
        />
        <div className="w-36">
          <Choice
            id={`${id}-unit`}
            aria-label={`${label}: unit`}
            value={unit}
            options={UNITS}
            onChange={(u) => onChange(value, u)}
          />
        </div>
      </div>
      <p id={`${id}-hint`} className="text-xs text-muted-foreground">
        {hint}
      </p>
    </div>
  );
}
