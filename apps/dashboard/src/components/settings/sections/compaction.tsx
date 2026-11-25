import { useState } from "react";
import { Field } from "@/components/registry-bits";
import { Section, type SectionProps } from "@/components/settings/section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
export function CompactionSection({
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
