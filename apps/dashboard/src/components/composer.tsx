/**
 * Writing on the dashboard: a reply in your own conversation, or the first
 * message of a new one. A send carries a key, the same for every retry of the
 * same words, so a send that seemed to fail and is tried again is one message
 * (`send-key.ts`).
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useId, useState } from "react";

import { sendSessionMessage, startConversation } from "@/api";
import type { Said } from "@/api/types";
import { isSend, Refusal } from "@/components/ask-team";
import { useSendKeys } from "@/components/send-key";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/input";
import { askProblem, askRefusal, Unanswered, unansweredRefusal } from "@/lib/ask";
import { cn } from "@/lib/utils";

export type Writes = { kind: "reply"; sessionId: string } | { kind: "start" };

/**
 * Sends what was typed, and takes the writer to the conversation it went to
 * when that is a new one.
 */
export function useSay(teamId: string, writes: Writes) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const keys = useSendKeys();
  const mutation = useMutation({
    mutationFn: async (text: string): Promise<Said> => {
      const key = keys.keyFor(text);
      if (writes.kind === "reply")
        return sendSessionMessage(teamId, writes.sessionId, { text, key });
      const said = await startConversation(teamId, { text, key });
      // As asking from Home: a new conversation nothing will answer is not opened.
      if (said.unanswered) {
        keys.done();
        throw new Unanswered(said.unanswered);
      }
      return said;
    },
    onSuccess: (said) => {
      keys.done();
      void qc.invalidateQueries({ queryKey: ["sessions", teamId] });
      void qc.invalidateQueries({ queryKey: ["runs", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      if (writes.kind === "start") {
        void navigate({ to: "/ui/sessions/$sessionId", params: { sessionId: said.session_id } });
      }
    },
  });
  return mutation;
}

export function Composer({
  teamId,
  writes,
  label,
  placeholder,
  className,
  autoFocus = false,
}: {
  teamId: string;
  writes: Writes;
  label: string;
  placeholder: string;
  className?: string;
  autoFocus?: boolean;
}) {
  const [input, setInput] = useState("");
  const say = useSay(teamId, writes);
  const id = useId();
  const text = input.trim();
  const problem = askProblem(text);
  const send = () => {
    if (text === "" || problem || say.isPending) return;
    say.mutate(text, { onSuccess: () => setInput("") });
  };
  const unanswered = say.data?.unanswered;

  return (
    <form
      aria-label={label}
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
      className={cn("space-y-2", className)}
    >
      <div className="flex items-end gap-2">
        <label htmlFor={id} className="sr-only">
          {label}
        </label>
        <Textarea
          id={id}
          value={input}
          rows={1}
          autoFocus={autoFocus}
          placeholder={placeholder}
          aria-invalid={problem !== null}
          aria-describedby={`${id}-hint`}
          onChange={(e) => {
            setInput(e.target.value);
            if (say.isError) say.reset();
          }}
          onKeyDown={(e) => {
            if (isSend(e)) {
              e.preventDefault();
              send();
            }
          }}
          className="max-h-40 min-h-9 resize-none bg-background field-sizing-content"
        />
        <Button type="submit" loading={say.isPending} disabled={text === "" || problem !== null}>
          Send
        </Button>
      </div>
      <div id={`${id}-hint`} className="text-xs text-muted-foreground">
        {problem ? (
          <p className="text-destructive">{problem}</p>
        ) : say.isError ? (
          <Refusal refusal={askRefusal(say.error)} />
        ) : unanswered ? (
          // A reply is in the conversation already; only no run will take it.
          <Refusal refusal={unansweredRefusal(unanswered)} />
        ) : (
          <p>Ctrl+Enter or ⌘+Enter sends it.</p>
        )}
      </div>
    </form>
  );
}
