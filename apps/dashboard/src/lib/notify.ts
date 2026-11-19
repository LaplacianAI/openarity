/**
 * Browser notifications for what waits on a person: a tool call an agent asked
 * about, and people waiting to be let in on a channel. They come from an open
 * dashboard tab, in the background too, never from a closed one: telling a
 * closed browser takes Web Push, a later change.
 *
 * A notification is tagged by what it is about, so several open tabs show one.
 */
import { useNavigate } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import type { ToolApproval } from "@/api/types";
import { mayAnswer, toolParts } from "@/lib/approvals";

const KEY = "oa.notifications";
/**
 * How often the shell asks for what waits while notifications are on, and
 * while they are off. A background tab's timers are slowed by the browser,
 * so this is the most often, not a promise.
 */
export const NOTIFY_POLL_MS = 30_000;
export const BADGE_POLL_MS = 60_000;
/** More new requests than this at once arrive as one notification. */
export const BURST = 3;

/**
 * "unable": the browser has Notification but refuses to show one from a tab,
 * as Chrome on Android does, wanting a service worker the dashboard has not
 * got until Web Push. Found out at the first notice, which is lost; a reload
 * asks again.
 */
export type NotifyState = "unsupported" | "unable" | "blocked" | "off" | "on";

let refused = false;

/** For tests: a fresh page, as a reload is, asks the browser again. */
export function forgetRefusal() {
  refused = false;
}

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

/** Keeps the switch; false where storage cannot, and it lasts as long as the tab. */
function remember(on: boolean): boolean {
  try {
    if (on) window.localStorage.setItem(KEY, "on");
    else window.localStorage.removeItem(KEY);
    return true;
  } catch {
    return false;
  }
}

const listeners = new Set<() => void>();
// The switch, only where storage cannot keep it: where it can, storage is the
// one answer, so a choice made in another tab holds here too.
let session: boolean | null = null;

function changed() {
  for (const l of listeners) l();
}

// Another tab turning notifications off, or the browser's permission changing,
// is heard here, not only on this tab's next render.
let watching = false;
function watch() {
  if (watching || typeof window === "undefined") return;
  watching = true;
  window.addEventListener("storage", (e) => {
    if (e.key === KEY || e.key === null) changed();
  });
  navigator.permissions
    ?.query({ name: "notifications" as PermissionName })
    .then((status) => {
      status.onchange = changed;
    })
    .catch(() => {
      // A browser that cannot say is read again on the next render.
    });
}

/** Where notifications stand in this browser: on only when wanted and allowed. */
export function notifyState(): NotifyState {
  if (!supported()) return "unsupported";
  if (refused) return "unable";
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
      watch();
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
    const on = answer === "granted";
    session = remember(on) ? null : on;
    changed();
  }, []);
  const turnOff = useCallback(() => {
    session = remember(false) ? null : false;
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

/**
 * What a notification for one request says. Not its summary: that is drawn
 * from the call's arguments, and a notification reaches a lock screen and a
 * shared screen.
 */
export function requestNotice(a: ToolApproval): { title: string; body: string } {
  const { server, name } = toolParts(a.tool);
  const what = server ? `${name} on ${server}` : name;
  return { title: `An agent asks before ${what}`, body: "Open Tool approvals to answer." };
}

/** What one notification for a burst of new requests says. */
export function requestsNotice(n: number): { title: string; body: string } {
  return { title: "Agents ask before acting", body: `${n} new requests wait for your answer.` };
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

function show(title: string, body: string, tag: string, open: () => void, again = false) {
  // Someone looking at the dashboard has the badge and the title already.
  if (document.visibilityState === "visible" && document.hasFocus()) return;
  try {
    // A tag shown again replaces the last silently, unless it renotifies.
    const options = { body, tag, renotify: again } as NotificationOptions;
    const n = new Notification(title, options);
    n.onclick = () => {
      window.focus();
      open();
      n.close();
    };
  } catch (err) {
    // Only an illegal constructor says this browser cannot from a tab; any
    // other failure may pass, and leaves the switch as it is.
    if (err instanceof TypeError && !refused) {
      refused = true;
      changed();
    }
  }
}

/**
 * Tells this viewer, while notifications are on, of each new request they may
 * answer and of more people waiting to be let in. It reads what the shell's
 * badges already ask for, so turning it on asks the brain nothing more; the
 * shell asks more often while it is on. `pending` is undefined until the
 * viewer may read the requests and they have arrived.
 */
export function useApprovalNotices({
  teamId,
  can,
  me,
  pending,
  waiting,
}: {
  teamId: string | null;
  can: (permission: string) => boolean;
  me: string | null;
  pending: readonly ToolApproval[] | undefined;
  waiting: number | null;
}) {
  const { state } = useNotifySwitch();
  const on = state === "on" && Boolean(teamId);
  const navigate = useNavigate();
  // Each baseline remembers what it was taken for, and starts again from what
  // is there when that changes: a new team, or the switch turned on again,
  // for both; a new viewer for the requests, which are filtered by who they
  // are. Permissions arriving late change neither.
  const seen = useRef<{ for: string; ids: Set<string> | null }>({ for: "", ids: null });
  const senders = useRef<{ for: string; n: number | null }>({ for: "", n: null });
  const who = useRef(can);
  who.current = can;

  useEffect(() => {
    // What the viewer may answer is in it too: gaining approval:decide would
    // make every request waiting on someone else read as new.
    const may = `${who.current("approval:self")}|${who.current("approval:decide")}`;
    const now = `${on}|${teamId}|${me}|${may}`;
    if (seen.current.for !== now) seen.current = { for: now, ids: null };
    // A request looked at before the viewer was known is looked at again
    // once they are, as a new baseline: their own were not told apart.
    if (!on || pending === undefined) return;
    const mine = answerable(pending, who.current, me);
    const ids = new Set(
      fresh(
        seen.current.ids,
        mine.map((a) => a.id),
      ),
    );
    seen.current.ids = new Set(mine.map((a) => a.id));
    const news = mine.filter((a) => ids.has(a.id));
    if (news.length > BURST) {
      const { title, body } = requestsNotice(news.length);
      show(title, body, `approvals:${teamId}`, () => navigate({ to: "/ui/requests" }), true);
      return;
    }
    for (const a of news) {
      const { title, body } = requestNotice(a);
      // One request opens on itself; a burst, above, on the list.
      show(title, body, `approval:${a.id}`, () =>
        navigate({ to: "/ui/requests", search: { request: a.id } }),
      );
    }
  }, [on, teamId, me, pending, navigate]);

  useEffect(() => {
    const now = `${on}|${teamId}`;
    if (senders.current.for !== now) senders.current = { for: now, n: null };
    if (!on || waiting === null) return;
    const before = senders.current.n;
    senders.current.n = waiting;
    if (before !== null && waiting > before) {
      const { title, body } = sendersNotice(waiting);
      show(title, body, `senders:${teamId}`, () => navigate({ to: "/ui/approvals" }), true);
    }
  }, [on, waiting, teamId, navigate]);
}

const PREFIX = /^\(\d+\+?\) /;

/**
 * "(3) openarity" while people wait to be let in or agents wait on an answer
 * the viewer may read, "(50+) …" past a page; the title alone otherwise, and
 * once the shell is gone, as on signing out.
 */
export function useWaitingTitle(count: string | null) {
  useEffect(() => {
    const base = document.title.replace(PREFIX, "");
    document.title = count ? `(${count}) ${base}` : base;
  }, [count]);
  useEffect(
    () => () => {
      document.title = document.title.replace(PREFIX, "");
    },
    [],
  );
}
