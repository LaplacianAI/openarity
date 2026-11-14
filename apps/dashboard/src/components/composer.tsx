/**
 * Writing on the dashboard: a reply in your own conversation, or the first
 * message of a new one, with files if there are any. A send carries a key,
 * the same for every retry of the same words and files, so a send that seemed
 * to fail and is tried again is one message (`send-key.ts`).
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useId, useState } from "react";

import { type Progress, sendSessionMessage, startConversation } from "@/api";
import type { Said } from "@/api/types";
import { isSend, Refusal } from "@/components/ask-team";
import {
  AttachButton,
  AttachedList,
  dropTarget,
  RefusedFiles,
  useAttachments,
  useProgress,
} from "@/components/attachments";
import { useSendKeys } from "@/components/send-key";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/input";
import { askProblem, askRefusal, Unanswered, unansweredRefusal } from "@/lib/ask";
import { MAX_FILES, sendContent } from "@/lib/attach";
import { fileSize } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Where a message goes: a reply in a conversation, or the first of a new one, the builder's or the hub's. */
export type Writes = { kind: "reply"; sessionId: string } | { kind: "start"; builder?: boolean };

type Message = { text: string; files: File[] };

/**
 * Sends what was typed and attached, and takes the writer to the conversation
 * it went to when that is a new one.
 */
export function useSay(teamId: string, writes: Writes, onProgress?: Progress) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const keys = useSendKeys();
  const mutation = useMutation({
    mutationFn: async ({ text, files }: Message): Promise<Said> => {
      const key = keys.keyFor(sendContent(text, files));
      if (writes.kind === "reply")
        return sendSessionMessage(teamId, writes.sessionId, { text, key }, files, onProgress);
      const builder = writes.builder ? { builder: true } : {};
      const said = await startConversation(teamId, { text, key, ...builder }, files, onProgress);
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
      void qc.invalidateQueries({ queryKey: ["build", teamId] });
      if (writes.kind === "start" && writes.builder) {
        void navigate({ to: "/ui/build", search: { session: said.session_id } });
      } else if (writes.kind === "start") {
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
  id: given,
}: {
  teamId: string;
  writes: Writes;
  label: string;
  placeholder: string;
  className?: string;
  autoFocus?: boolean;
  /** The text box's id, for a page that moves focus to it. */
  id?: string;
}) {
  const [input, setInput] = useState("");
  const progress = useProgress();
  const say = useSay(teamId, writes, progress.report);
  const a = useAttachments({ busy: say.isPending, changed: () => say.isError && say.reset() });
  const own = useId();
  const id = given ?? own;
  const text = input.trim();
  const problem = askProblem(text);
  const sendable =
    (text !== "" || a.files.length > 0) && problem === null && a.refused.length === 0;

  const send = () => {
    if (!sendable || say.isPending) return;
    progress.reset();
    say.mutate(
      { text, files: a.files },
      {
        onSuccess: () => {
          setInput("");
          a.clear();
        },
      },
    );
  };
  const unanswered = say.data?.unanswered;
  const total = a.files.reduce((n, f) => n + f.size, 0);
  const share = progress.sent ? Math.round((progress.sent.sent / progress.sent.total) * 100) : null;

  return (
    <form
      aria-label={label}
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
      {...a.dropProps}
      className={cn("relative space-y-2", dropTarget(a.dragging), className)}
    >
      <AttachedList a={a} sending={say.isPending} sent={progress.sent} />
      <div className="flex items-end gap-2">
        <AttachButton onPick={a.add} disabled={say.isPending} />
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
          onPaste={a.onPaste}
          onKeyDown={(e) => {
            if (isSend(e)) {
              e.preventDefault();
              send();
            }
          }}
          className="max-h-40 min-h-9 resize-none bg-background field-sizing-content"
        />
        <Button type="submit" loading={say.isPending} disabled={!sendable}>
          Send
        </Button>
      </div>
      <div id={`${id}-hint`} className="text-xs text-muted-foreground">
        {problem ? (
          <p className="text-destructive">{problem}</p>
        ) : a.refused.length > 0 ? (
          <RefusedFiles a={a} />
        ) : say.isError ? (
          <Refusal refusal={askRefusal(say.error)} />
        ) : unanswered ? (
          // A reply is in the conversation already; only no run will take it.
          <Refusal refusal={unansweredRefusal(unanswered)} />
        ) : a.dragging ? (
          <p>Drop to attach. Up to {MAX_FILES} files, 25 MB each.</p>
        ) : say.isPending && share !== null ? (
          <p className="tabular-nums">Sending… {share}%</p>
        ) : a.files.length > 0 ? (
          <p className="tabular-nums">
            {a.files.length === 1 ? "1 file" : `${a.files.length} files`}, {fileSize(total)}.
            Ctrl+Enter or ⌘+Enter sends it.
          </p>
        ) : (
          <p>Ctrl+Enter or ⌘+Enter sends it.</p>
        )}
      </div>
    </form>
  );
}
