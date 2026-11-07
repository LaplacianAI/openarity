/**
 * Asking the team something: one of the four jobs the dashboard is for. The
 * question goes to the team's hub, which answers or hands the work on, and
 * the asker lands on the run's trace to watch it work.
 *
 * Offered on Home and in ⌘K, and only to someone who may start runs.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight, TriangleAlert } from "lucide-react";
import { useId, useState } from "react";

import { startRun } from "@/api";
import { runLink } from "@/components/run-table";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Textarea } from "@/components/ui/input";
import { type AskRefusal, askProblem, askRefusal } from "@/lib/ask";

/** What starting a run takes in a team. */
export const RUN_WRITE = "run:write";

/**
 * Starts a hub run with what was asked, and opens its trace once the brain
 * has queued it. Every list a new run or session would appear in is read
 * again.
 */
export function useAsk(teamId: string) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: (input: string) => startRun(teamId, { input }),
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: ["runs", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      void qc.invalidateQueries({ queryKey: ["sessions", teamId] });
      void navigate(runLink(run));
    },
  });
}

/** Ctrl+Enter, or ⌘+Enter on a Mac: Enter alone is a new line. */
export function isSend(e: { key: string; metaKey: boolean; ctrlKey: boolean }): boolean {
  return e.key === "Enter" && (e.metaKey || e.ctrlKey);
}

/** Why the brain refused, with the way to Team settings when that is where the fix is. */
export function Refusal({ refusal }: { refusal: AskRefusal }) {
  return (
    <div role="alert" className="flex items-start gap-2 text-sm">
      <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-failed" />
      <p className="min-w-0">
        {refusal.text}
        {refusal.settings ? (
          <>
            {" "}
            <Link
              to="/ui/settings"
              className="inline-flex items-center gap-1 font-medium underline underline-offset-4"
            >
              Open team settings
              <ArrowRight aria-hidden className="size-3" />
            </Link>
          </>
        ) : null}
      </p>
    </div>
  );
}

/** The box on Home. */
export function AskTeam({ teamId }: { teamId: string }) {
  const [input, setInput] = useState("");
  const ask = useAsk(teamId);
  const id = useId();

  const question = input.trim();
  const problem = askProblem(question);
  const send = () => {
    if (question === "" || problem || ask.isPending) return;
    ask.mutate(question);
  };

  return (
    <form
      aria-label="Ask the team"
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
      className="space-y-3 rounded-lg bg-card p-4"
    >
      <Field
        label="Ask the team"
        htmlFor={id}
        hint="The hub answers, or hands the work to the agent that fits. You'll watch the run as it works. Ctrl+Enter or ⌘+Enter sends it."
        error={problem}
      >
        <Textarea
          id={id}
          value={input}
          rows={2}
          placeholder="What failed in last night's deploy, and who should look at it?"
          onChange={(e) => {
            setInput(e.target.value);
            if (ask.isError) ask.reset();
          }}
          onKeyDown={(e) => {
            if (isSend(e)) {
              e.preventDefault();
              send();
            }
          }}
          className="bg-background"
        />
      </Field>
      <div className="flex flex-col-reverse items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          {ask.isError ? <Refusal refusal={askRefusal(ask.error)} /> : null}
        </div>
        <Button
          type="submit"
          size="sm"
          loading={ask.isPending}
          disabled={question === "" || problem !== null}
        >
          Ask
        </Button>
      </div>
    </form>
  );
}
