/**
 * A run's token budget as a person reads it: how much of it the run spent,
 * counted as providers bill (a cached input token is a tenth of one), and
 * whether it is near the line or past it.
 */
import type { Run } from "@/api/types";

/** At this share or more, the budget is nearly spent. */
export const nearShare = 0.9;

export type Budget = {
  spent: number;
  budget: number;
  /** Whole percent of the budget spent: rounded, but 99 at most until it is spent. */
  percent: number;
  state: "ok" | "near" | "spent";
};

/** Null for a brain from before the budget was sent, or a budget of none. */
export function budgetOf(run: Pick<Run, "spent" | "token_budget">): Budget | null {
  const { spent, token_budget: budget } = run;
  if (spent === undefined || budget === undefined || budget <= 0) return null;
  const share = Math.max(0, spent) / budget;
  const state = share >= 1 ? "spent" : share >= nearShare ? "near" : "ok";
  const percent =
    state === "spent" ? Math.round(share * 100) : Math.min(99, Math.round(share * 100));
  return { spent, budget, percent, state };
}

/** Tokens in short: 950, 574k, 2M, 1.85M. */
export function shortTokens(n: number): string {
  if (n < 1000) return String(n);
  const k = Math.round(n / 1000);
  if (k < 1000) return `${k}k`;
  const m = n / 1_000_000;
  return `${Number(m.toFixed(m < 10 ? 2 : 1))}M`;
}
