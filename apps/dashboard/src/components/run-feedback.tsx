/**
 * A member's word on an agent's answer: right or wrong, and for a wrong one,
 * in a line, what was wrong. It feeds the team's self-improvement, so it is
 * offered only while that is on and only to someone who may run agents.
 *
 * The list of the caller's feedback is read once per session and every run's
 * buttons read their own entry from it. A press is shown at once and rolled
 * back, with the brain's reason beside the buttons, when the brain refuses.
 */
import { type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import { ThumbsDown, ThumbsUp, TriangleAlert } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { clearRunFeedback, getSessionFeedback, getTeamSettings, giveRunFeedback } from "@/api";
import { usePermissions } from "@/api/permissions";
import type { FeedbackValue, SessionFeedback } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

const RUN_WRITE = "run:write";
/** The brain's limit on a note. */
const NOTE_MAX = 1000;

const feedbackKey = (teamId: string, sessionId: string) => ["feedback", teamId, sessionId] as const;

/** Whether this reader is offered 👍/👎 at all: improvement is on, and they hold run:write. */
export function useCanGiveFeedback(teamId: string): boolean {
  const canWrite = usePermissions(teamId).can(RUN_WRITE);
  const settings = useQuery({
    queryKey: ["settings", teamId],
    queryFn: () => getTeamSettings(teamId),
  });
  return canWrite && settings.data?.improvement === true;
}

type Entry = SessionFeedback["items"][number];

function swap(qc: QueryClient, key: readonly string[], runId: string, next: Entry | null) {
  qc.setQueryData<SessionFeedback>(key, (old) => ({
    items: [...(old?.items ?? []).filter((i) => i.run_id !== runId), ...(next ? [next] : [])],
  }));
}

export function RunFeedbackControls({
  teamId,
  sessionId,
  runId,
}: {
  teamId: string;
  sessionId: string;
  runId: string;
}) {
  const qc = useQueryClient();
  const key = feedbackKey(teamId, sessionId);
  // One request per session; each run picks out its own entry.
  const list = useQuery({
    queryKey: key,
    queryFn: () => getSessionFeedback(teamId, sessionId),
    select: (d) => d.items.find((i) => i.run_id === runId) ?? null,
  });
  const mine = list.data ?? null;

  const [noting, setNoting] = useState(false);
  const [note, setNote] = useState("");
  const [noted, setNoted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const busy = useRef(false);
  const noteField = useRef<HTMLInputElement>(null);
  const alert = useRef<HTMLParagraphElement>(null);

  useEffect(() => {
    if (noting) noteField.current?.focus();
  }, [noting]);

  // The composer is pinned over the foot of the page: bring a refusal clear of it.
  useEffect(() => {
    if (error) alert.current?.scrollIntoView?.({ block: "nearest" });
  }, [error]);

  const put = async (value: FeedbackValue, text: string) => {
    const saved = await giveRunFeedback(teamId, runId, {
      value,
      ...(text ? { note: text } : {}),
      via: "dashboard",
    });
    swap(qc, key, runId, { ...saved, run_id: runId });
  };

  // Shows the press at once; the previous entry comes back if the brain says no.
  const press = async (value: FeedbackValue) => {
    if (busy.current) return;
    busy.current = true;
    const before = mine;
    const clearing = before?.value === value;
    setError(null);
    setNoted(false);
    await qc.cancelQueries({ queryKey: key });
    swap(
      qc,
      key,
      runId,
      clearing ? null : { run_id: runId, value, note: "", updated_at: new Date().toISOString() },
    );
    setNoting(!clearing && value === "down");
    setNote("");
    try {
      if (clearing) await clearRunFeedback(teamId, runId);
      else await put(value, "");
    } catch (err) {
      swap(qc, key, runId, before);
      setNoting(false);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      busy.current = false;
    }
  };

  const send = async (e: FormEvent) => {
    e.preventDefault();
    const text = note.trim();
    if (text === "") {
      setNoting(false);
      return;
    }
    if (busy.current) return;
    busy.current = true;
    setSending(true);
    setError(null);
    try {
      await put("down", text);
      setNoting(false);
      setNoted(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      busy.current = false;
      setSending(false);
    }
  };

  const ready = !list.isPending;
  return (
    <div data-feedback className="-mt-1 -ml-2 space-y-1.5">
      <div className="flex items-center gap-0.5">
        <FeedbackButton
          label="This answer was right"
          icon={ThumbsUp}
          pressed={mine?.value === "up"}
          disabled={!ready}
          onPress={() => void press("up")}
        />
        <FeedbackButton
          label="This answer was wrong"
          icon={ThumbsDown}
          pressed={mine?.value === "down"}
          disabled={!ready}
          onPress={() => void press("down")}
        />
        {noted ? (
          <span role="status" className="pl-2 text-xs text-muted-foreground">
            Note saved
          </span>
        ) : null}
      </div>
      {noting ? (
        <form onSubmit={(e) => void send(e)} className="flex flex-wrap items-center gap-2 pl-2">
          <Input
            ref={noteField}
            aria-label="What was wrong?"
            placeholder="What was wrong? (optional)"
            maxLength={NOTE_MAX}
            value={note}
            disabled={sending}
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setNoting(false);
            }}
            className="h-8 max-w-sm min-w-0 flex-1 basis-56 scroll-mb-40 text-base md:text-sm"
          />
          <Button type="submit" variant="outline" size="sm" disabled={sending}>
            Send
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={() => setNoting(false)}>
            Skip
          </Button>
        </form>
      ) : null}
      {error ? (
        <p
          ref={alert}
          role="alert"
          className="scroll-mb-40 flex items-start gap-1.5 pl-2 text-xs text-destructive"
        >
          <TriangleAlert aria-hidden className="mt-0.5 size-3.5 shrink-0" />
          <span className="min-w-0 break-words">{error}</span>
        </p>
      ) : null}
    </div>
  );
}

function FeedbackButton({
  label,
  icon: Icon,
  pressed,
  disabled,
  onPress,
}: {
  label: string;
  icon: typeof ThumbsUp;
  pressed: boolean;
  disabled: boolean;
  onPress: () => void;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onPress}
      className={cn(
        // 36px under a thumb, 32px with a pointer, as the rest of the frame is.
        "size-9 text-muted-foreground hover:text-foreground md:size-8",
        pressed && "bg-secondary text-foreground",
      )}
    >
      {/* Filled when chosen, so the state is shape as well as tone. */}
      <Icon aria-hidden className={cn("size-4", pressed && "fill-current")} />
    </Button>
  );
}
