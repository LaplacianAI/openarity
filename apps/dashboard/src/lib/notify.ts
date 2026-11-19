/**
 * Browser notifications for what waits on a person: a tool call an agent asked
 * about, and people waiting to be let in on a channel. They come from an open
 * dashboard tab, in the background too, never from a closed one: telling a
 * closed browser takes Web Push, a later change.
 *
 * A notification is tagged by what it is about, so several open tabs show one.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { listToolApprovals } from "@/api";
import { useTeamSummary } from "@/api/summary";
import type { ToolApproval } from "@/api/types";
import { mayAnswer, toolParts } from "@/lib/approvals";

const KEY = "oa.notifications";
const POLL_MS = 30_000;

export type NotifyState = "unsupported" | "blocked" | "off" | "on";

function supported(): boolean {
  return typeof window !== "undefined" && "Notification" in window;
}

function wanted(): boolean {
  try {
    return window.localStorage.getItem(KEY) === "on";
  } catch {
    return false;
  }
}

function remember(on: boolean) {
  try {
    if (on) window.localStorage.setItem(KEY, "on");
    else window.localStorage.removeItem(KEY);
  } catch {
    // Without storage the switch lasts as long as the tab.
  }
}

const listeners = new Set<() => void>();
let session: boolean | null = null; // the switch, where storage cannot keep it

function changed() {
  for (const l of listeners) l();
}

/** Where notifications stand in this browser: on only when wanted and allowed. */
export function notifyState(): NotifyState {
  if (!supported()) return "unsupported";
  if (Notification.permission === "denied") return "blocked";
  const on = session ?? wanted();
  return on && Notification.permission === "granted" ? "on" : "off";
}

/** The switch: its state, and turning it on (asking the browser once) or off. */
export function useNotifySwitch(): {
  state: NotifyState;
  turnOn: () => Promise<void>;
  turnOff: () => void;
} {
  const state = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    notifyState,
    () => "unsupported" as NotifyState,
  );
  const turnOn = useCallback(async () => {
    if (!supported()) return;
    const answer =
      Notification.permission === "default"
        ? await Notification.requestPermission()
        : Notification.permission;
    session = answer === "granted";
    remember(session);
    changed();
  }, []);
  const turnOff = useCallback(() => {
    session = false;
    remember(false);
    changed();
  }, []);
  return { state, turnOn, turnOff };
}

/**
 * The ids in `now` not seen before. The first look only learns what is already
 * there (null before it): what waited before notifications were on is on the
 * badge already, and is not news.
 */
export function fresh(seen: ReadonlySet<string> | null, now: readonly string[]): string[] {
  if (seen === null) return [];
  return now.filter((id) => !seen.has(id));
}

/** The requests this viewer may answer: telling anyone else is noise. */
export function answerable(
  items: readonly ToolApproval[],
  can: (permission: string) => boolean,
  me: string | null,
): ToolApproval[] {
  return items.filter((a) =>
    mayAnswer({ requestedFor: a.requested_for, requireOther: a.require_other }, can, me),
  );
}

/** What a notification for one request says. */
export function requestNotice(a: ToolApproval): { title: string; body: string } {
  const { server, name } = toolParts(a.tool);
  const what = server ? `${name} on ${server}` : name;
  const body = a.summary.length > 140 ? `${a.summary.slice(0, 139)}…` : a.summary;
  return { title: `An agent asks before ${what}`, body };
}

/** What a notification for people waiting to be let in says. */
export function sendersNotice(n: number): { title: string; body: string } {
  return {
    title: "Waiting to be let in",
    body:
      n === 1
        ? "Someone wrote in on a channel and waits to be let in."
        : `${n} people wrote in on a channel and wait to be let in.`,
  };
}

function show(title: string, body: string, tag: string, open: () => void) {
  // Someone looking at the dashboard has the badge and the title already.
  if (document.visibilityState === "visible" && document.hasFocus()) return;
  try {
    const n = new Notification(title, { body, tag });
    n.onclick = () => {
      window.focus();
      open();
      n.close();
    };
  } catch {
    // A browser that refuses a notification here (some phones want a service
    // worker) still has the badge.
  }
}

/**
 * Tells this viewer, while notifications are on, of each new request they may
 * answer and of more people waiting to be let in, and asks for both every 30
 * seconds rather than every minute.
 */
export function useApprovalNotices(
  teamId: string | null,
  can: (permission: string) => boolean,
  me: string | null,
  mayReadRequests: boolean,
) {
  const { state } = useNotifySwitch();
  const on = state === "on" && Boolean(teamId);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const summary = useTeamSummary(teamId);
  const seen = useRef<Set<string> | null>(null);
  const senders = useRef<number | null>(null);
  // Read when a look runs, so a new render does not restart the timer.
  const who = useRef({ can, me });
  who.current = { can, me };

  useEffect(() => {
    // A new team, or notifications turned on again, starts from what is
    // there. This effect runs before the senders' one below, which then
    // takes its first count as what was already waiting.
    seen.current = null;
    senders.current = null;
    if (!on || !teamId) return;
    let stopped = false;
    const look = async () => {
      if (mayReadRequests) {
        try {
          const page = await qc.fetchQuery({
            queryKey: ["approvals", teamId, "pending-count"],
            queryFn: ({ signal }) =>
              listToolApprovals(teamId, { status: ["pending"] }, null, signal),
            staleTime: 0,
          });
          if (stopped) return;
          const mine = answerable(page.items, who.current.can, who.current.me);
          for (const id of fresh(
            seen.current,
            mine.map((a) => a.id),
          )) {
            const a = mine.find((x) => x.id === id) as ToolApproval;
            const { title, body } = requestNotice(a);
            show(title, body, `approval:${a.id}`, () => navigate({ to: "/ui/requests" }));
          }
          seen.current = new Set(mine.map((a) => a.id));
        } catch {
          // The badge's own query reports a failure; the next look tries again.
        }
      }
      if (!stopped) qc.invalidateQueries({ queryKey: ["summary", teamId] });
    };
    look();
    const timer = window.setInterval(look, POLL_MS);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [on, teamId, mayReadRequests, qc, navigate]);

  const waiting = summary.data?.waiting_senders ?? null;
  useEffect(() => {
    if (!on || waiting === null) return;
    const before = senders.current;
    senders.current = waiting;
    if (before !== null && waiting > before) {
      const { title, body } = sendersNotice(waiting);
      show(title, body, `senders:${teamId}`, () => navigate({ to: "/ui/approvals" }));
    }
  }, [on, waiting, teamId, navigate]);
}

/** "(3) openarity" while something waits on the viewer; the title alone otherwise. */
export function useWaitingTitle(count: number) {
  useEffect(() => {
    const base = document.title.replace(/^\(\d+\+?\) /, "");
    document.title = count > 0 ? `(${count}) ${base}` : base;
  }, [count]);
}
