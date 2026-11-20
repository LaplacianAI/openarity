/**
 * When a run compacts, as the Team settings page edits it: past a count of
 * messages, or once its next request would read a tenth of its token budget,
 * held between the team's least and most. The bounds are the brain's; they are
 * checked here so a typo is caught before a round trip, and the brain checks
 * again.
 */

export const MESSAGES = { min: 10, max: 10_000 } as const;
export const TOKENS = { min: 30_000, max: 1_000_000 } as const;

/** What a team compacts at before it saves its settings. */
export const DEFAULTS = { messages: 100, least: 50_000, most: 60_000 } as const;

/** The three fields as typed. */
export type CompactionDraft = { messages: string; least: string; most: string };

/** What is wrong with each field, and with the two token lines together. */
export type CompactionProblems = {
  messages?: string;
  least?: string;
  most?: string;
  /** At least above at most; said once, under the pair. */
  pair?: string;
};

/**
 * A whole number as a person types one, thousands grouped or not: "60000",
 * "60,000" and "60 000" are all 60,000. Anything else is null.
 */
export function parseWhole(raw: string): number | null {
  const t = raw.trim().replace(/[\s,  ]/g, "");
  return /^\d+$/.test(t) ? Number(t) : null;
}

/** n with its thousands grouped, as the page shows a count: 60,000. */
export function grouped(n: number): string {
  return n.toLocaleString("en-US");
}

/**
 * A field as it is shown once the reader leaves it: grouped when it is a
 * number, left as typed when it is not, so the mistake stays in sight.
 */
export function tidied(raw: string): string {
  const n = parseWhole(raw);
  return n === null ? raw : grouped(n);
}

function outside(n: number | null, range: { min: number; max: number }): boolean {
  return n === null || n < range.min || n > range.max;
}

export function checkCompaction(d: CompactionDraft): CompactionProblems {
  const problems: CompactionProblems = {};
  const messages = parseWhole(d.messages);
  const least = parseWhole(d.least);
  const most = parseWhole(d.most);
  if (outside(messages, MESSAGES)) {
    problems.messages = `Use a whole number from ${grouped(MESSAGES.min)} to ${grouped(MESSAGES.max)}.`;
  }
  const tokens = `Use a whole number from ${grouped(TOKENS.min)} to ${grouped(TOKENS.max)}.`;
  if (outside(least, TOKENS)) problems.least = tokens;
  if (outside(most, TOKENS)) problems.most = tokens;
  if (!problems.least && !problems.most && least !== null && most !== null && least > most) {
    problems.pair =
      `At least, ${grouped(least)}, is above at most, ${grouped(most)}. ` +
      "Lower the first or raise the second.";
  }
  return problems;
}

/** The sentence a Save shows for the first problem, or null for none. */
export function firstProblem(p: CompactionProblems): string | null {
  if (p.messages) return `Messages before a run compacts: ${lower(p.messages)}`;
  if (p.least) return `At least: ${lower(p.least)}`;
  if (p.most) return `At most: ${lower(p.most)}`;
  return p.pair ?? null;
}

function lower(s: string): string {
  return s.charAt(0).toLowerCase() + s.slice(1);
}

/**
 * What the token lines do, in a sentence; null while either is not a number
 * in range, so the page never explains a line the brain would refuse.
 */
export function explain(d: CompactionDraft): string | null {
  const p = checkCompaction(d);
  if (p.least || p.most || p.pair) return null;
  const least = parseWhole(d.least) as number;
  const most = parseWhole(d.most) as number;
  if (least === most) return `Every run compacts at ${grouped(most)} tokens.`;
  return (
    "A run compacts at a tenth of its token budget, but never below " +
    `${grouped(least)} or above ${grouped(most)} tokens.`
  );
}

/** Whether the fields already hold the defaults, by value. */
export function isDefault(d: CompactionDraft): boolean {
  return (
    parseWhole(d.messages) === DEFAULTS.messages &&
    parseWhole(d.least) === DEFAULTS.least &&
    parseWhole(d.most) === DEFAULTS.most
  );
}
