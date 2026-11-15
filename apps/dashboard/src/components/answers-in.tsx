import type { AnswersIn } from "@/api/types";

/** Where a channel can answer, in the words a person reads them by. */
export const ANSWERS_IN: [value: AnswersIn, words: string][] = [
  ["everywhere", "Direct chats, and groups where it is mentioned"],
  ["groups", "Only groups, where it is mentioned"],
  ["direct", "Only direct chats"],
];

export function answersInWords(a?: AnswersIn): string {
  return ANSWERS_IN.find(([v]) => v === a)?.[1] ?? "Direct chats, and groups where it is mentioned";
}

/**
 * The choice of where a channel answers. Outside it a message asks nothing:
 * no answer, and no stranger queued or told.
 */
export function AnswersInChoice({
  name,
  value,
  onChange,
  disabled = false,
}: {
  name: string;
  value: AnswersIn;
  onChange: (a: AnswersIn) => void;
  disabled?: boolean;
}) {
  return (
    <fieldset className="space-y-1.5" disabled={disabled}>
      <legend className="text-sm font-medium">Where it answers</legend>
      <div className="grid gap-1.5 text-sm">
        {ANSWERS_IN.map(([v, words]) => (
          <label key={v} className="flex cursor-pointer items-center gap-2">
            <input
              type="radio"
              name={name}
              checked={value === v}
              onChange={() => onChange(v)}
              className="size-4 accent-primary"
            />
            {words}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
