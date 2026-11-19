import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ExternalLink, TriangleAlert } from "lucide-react";
import { type ReactNode, useState } from "react";
import { toast } from "sonner";

import { createChannel, deleteChannel, listChannels, updateChannel } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type {
  AnswersIn,
  CreateChannelRequest,
  CreatedChannel,
  ListedChannel,
  ReplyMode,
} from "@/api/types";
import { AnswersInChoice, answersInWords } from "@/components/answers-in";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { LinkNumber } from "@/components/link-number";
import { Field, ReadOnlyNote } from "@/components/registry-bits";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { SwitchField } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { count } from "@/lib/runs";

export const Route = createFileRoute("/ui/channels/")({
  component: Channels,
});

const WRITE = "channel:write";
const READ_ALL = "session:read_all";

/** The adapters this build ships, each with the name a person knows it by. */
export const PROVIDERS: [value: string, word: string][] = [
  ["custom", "Custom webhook"],
  ["slack", "Slack"],
  ["telegram", "Telegram"],
  ["whatsapp", "WhatsApp (linked device)"],
];

const CUSTOM = "custom";
const SLACK = "slack";
const TELEGRAM = "telegram";
const WHATSAPP = "whatsapp";
/** The providers that can tell a sender nobody has let in that they wait. */
const NOTIFIES = new Set([SLACK, TELEGRAM, WHATSAPP]);
/** The dashboard's own channel: made with the team, written to from the dashboard, never deleted. */
const DASHBOARD = "dashboard";

/** The brain's own limit on a channel's name. */
const MAX_NAME = 200;

function Channels() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [shown, setShown] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [created, setCreated] = useState<CreatedChannel | null>(null);

  const channels = useInfiniteQuery({
    queryKey: ["channels", teamId, "paged"],
    queryFn: ({ pageParam }) => listChannels(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => qc.invalidateQueries({ queryKey: ["channels", teamId] });
  const rows = channels.data?.pages.flatMap((p) => p.items) ?? [];
  const open = rows.find((c) => c.id === shown) ?? null;
  // The list counts only the sessions this person may open, so only someone
  // who reads every conversation can call a channel quiet; and only a whole
  // list can say how many are.
  const quiet =
    channels.data && !channels.hasNextPage && perms.can(READ_ALL) ? rows.filter(isQuiet).length : 0;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Channels"
        finding={quiet > 0 ? `${count(quiet, "channel")} had no message in the last day.` : null}
        description="Where messages arrive from. Each belongs to one team and one provider."
        actions={
          canWrite ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              Connect channel
            </Button>
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="channels" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its channels." />
      ) : channels.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading channels">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : channels.isError ? (
        <ErrorState
          what="the channels"
          error={channels.error}
          onRetry={() => channels.refetch()}
          retrying={channels.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No channels yet"
          body="A channel is where messages reach the team: an inbox, a support tool, a webhook. Each has a signing secret that proves a request really came from it."
        />
      ) : (
        <div>
          <Table aria-label="Channels in this team">
            <TableHeader>
              <tr>
                <TableHead>Channel</TableHead>
                <TableHead>Last message</TableHead>
                <TableHead>Last 24 hours</TableHead>
              </tr>
            </TableHeader>
            <TableBody>
              {rows.map((c) => (
                <TableRow key={c.id} onOpen={() => setShown(c.id)}>
                  <TableCell className="sm:w-full sm:max-w-0">
                    <div className="min-w-0 flex-1">
                      <button
                        type="button"
                        tabIndex={-1}
                        onClick={() => setShown(c.id)}
                        className="block max-w-full truncate text-left font-medium hover:underline"
                      >
                        {c.name}
                      </button>
                      <span className="block truncate text-xs text-muted-foreground">
                        {c.provider}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell
                    label="Last message"
                    className="whitespace-nowrap text-muted-foreground"
                  >
                    {c.last_message_at ? <Time iso={c.last_message_at} /> : "Never"}
                  </TableCell>
                  <TableCell
                    label="Last 24 hours"
                    className={
                      c.sessions_24h === 0
                        ? "whitespace-nowrap text-muted-foreground tabular-nums"
                        : "whitespace-nowrap tabular-nums"
                    }
                  >
                    {count(c.sessions_24h, "session")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <LoadMore
            hasMore={Boolean(channels.hasNextPage)}
            loading={channels.isFetchingNextPage}
            onClick={() => channels.fetchNextPage()}
            loadedLabel={`${rows.length} channel${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}

      {teamId ? (
        <Sheet open={open !== null} onOpenChange={(o) => (o ? null : setShown(null))}>
          <SheetContent>
            {open ? (
              <ChannelDetail
                teamId={teamId}
                channel={open}
                canWrite={canWrite}
                onChanged={() => void changed()}
                onDeleted={() => {
                  setShown(null);
                  void changed();
                }}
              />
            ) : null}
          </SheetContent>
        </Sheet>
      ) : null}

      {teamId && canWrite ? (
        <ConnectSheet
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onCreated={(c) => {
            void changed();
            setAdding(false);
            if (opensToLink(c)) setShown(c.id);
            else setCreated(c);
          }}
        />
      ) : null}

      <CreatedDialog channel={created} onDone={() => setCreated(null)} />
    </div>
  );
}

/**
 * Whether a channel just connected opens beside the list to link its number,
 * rather than in the dialog that shows its secret once: a paired channel's
 * generated secret signs nothing, and linking is what is left to do.
 */
export function opensToLink(c: CreatedChannel): boolean {
  return Boolean(c.can_pair);
}

/**
 * Used before, and silent for the last day. A channel never used is not
 * quiet: nothing has been sent to it yet.
 */
export function isQuiet(c: ListedChannel): boolean {
  return c.last_message_at !== null && c.sessions_24h === 0;
}

/** One channel beside the list: where its webhooks go, and what it has carried. */
function ChannelDetail({
  teamId,
  channel,
  canWrite,
  onChanged,
  onDeleted,
}: {
  teamId: string;
  channel: ListedChannel;
  canWrite: boolean;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const builtIn = channel.provider === DASHBOARD;
  return (
    <>
      <SheetHeader>
        <SheetTitle>{channel.name}</SheetTitle>
        <SheetDescription>{channel.provider}</SheetDescription>
      </SheetHeader>

      <KeyValues>
        <KeyValue label="Last message">
          {channel.last_message_at ? <Time iso={channel.last_message_at} /> : "Never"}
        </KeyValue>
        <KeyValue label="Last 24 hours">{count(channel.sessions_24h, "session")}</KeyValue>
        {builtIn ? (
          <KeyValue label="Messages">
            Typed here, on the dashboard: asking the team starts a conversation on this channel, and
            the answer is read in the conversation as it is written.
          </KeyValue>
        ) : channel.can_pair ? (
          <KeyValue label="Number">
            {canWrite ? (
              <LinkNumber
                teamId={teamId}
                channelId={channel.id}
                word={channel.trigger_word ?? ""}
              />
            ) : (
              <>Linked by pairing a phone, which needs {WRITE}.</>
            )}
          </KeyValue>
        ) : (
          <>
            <KeyValue label={channel.hook_url ? "Webhook URL" : "Webhook path"}>
              <Mono className="break-all text-foreground">POST {hookAddress(channel)}</Mono>
            </KeyValue>
            <KeyValue label="Signing secret">
              {channel.provider === SLACK
                ? "In the secret store, as pasted from the Slack app's settings."
                : "In the secret store. It was shown once, when the channel was connected."}
            </KeyValue>
          </>
        )}
        {channel.provider === TELEGRAM && channel.can_reply ? (
          <KeyValue label="Replies to">
            Back into the Telegram chat each message came from.
          </KeyValue>
        ) : null}
        {channel.provider === SLACK && channel.can_reply ? (
          <KeyValue label="Replies to">
            A direct message in place, and a channel in a thread under the message that asked.
          </KeyValue>
        ) : null}
        {builtIn ? null : (
          <KeyValue label="Answers">
            {answersInWords(channel.answers_in)}.
            {canWrite ? (
              <EditAnswersIn teamId={teamId} channel={channel} onChanged={onChanged} />
            ) : null}
          </KeyValue>
        )}
        {channel.can_trigger_word ? (
          <KeyValue label="Trigger word">
            {channel.trigger_word ? (
              <>
                <span className="font-medium">{channel.trigger_word}</span>: a message must hold it
                to ask the agent.
              </>
            ) : (
              "None. Direct chats ask the agent, and groups where the number is mentioned."
            )}
            {canWrite ? (
              <EditTriggerWord teamId={teamId} channel={channel} onChanged={onChanged} />
            ) : null}
          </KeyValue>
        ) : null}
        {channel.can_notify ? (
          <KeyValue label="Waiting senders">
            {channel.notify_strangers
              ? "Told once that they are waiting, and again when let in."
              : "Not told anything. Their messages wait in Approvals."}
            {canWrite ? (
              <NotifyStrangers teamId={teamId} channel={channel} onChanged={onChanged} />
            ) : null}
          </KeyValue>
        ) : null}
        {channel.can_show_events ? (
          <KeyValue label="While it works">
            {channel.reply_mode === "events"
              ? "Each step is sent as it happens: the tools it uses, the agents it asks. Every message stays in the chat; the answer comes last."
              : "Nothing until the answer, which is sent alone."}
            {canWrite ? (
              <EditReplyMode teamId={teamId} channel={channel} onChanged={onChanged} />
            ) : null}
          </KeyValue>
        ) : null}
        {channel.takes_reply_url ? (
          <KeyValue label="Replies to">
            {channel.reply_url ? (
              <Mono className="break-all text-foreground">{channel.reply_url}</Mono>
            ) : (
              "Not set, so the team's replies are not sent."
            )}
            {canWrite ? (
              <EditReplyURL teamId={teamId} channel={channel} onChanged={onChanged} />
            ) : null}
          </KeyValue>
        ) : null}
      </KeyValues>

      <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-rule pt-4">
        <Button variant="outline" asChild>
          <Link to="/ui/sessions" search={{ channel: channel.id }}>
            Open its sessions
          </Link>
        </Button>
        {builtIn ? (
          <p className="text-xs text-muted-foreground">
            Built in: every team has this channel, so it cannot be deleted.
          </p>
        ) : canWrite ? (
          <DeleteChannel teamId={teamId} channel={channel} onDeleted={onDeleted} />
        ) : null}
      </div>
    </>
  );
}

/**
 * Whether a sender nobody has let in is told so. Off, they hear nothing, as
 * before; on, the bot says once that they are waiting and again when they are
 * in, so they know to send their message again.
 */
function NotifyStrangers({
  teamId,
  channel,
  onChanged,
}: {
  teamId: string;
  channel: ListedChannel;
  onChanged: () => void;
}) {
  const on = channel.notify_strangers === true;
  const save = useMutation({
    mutationFn: (next: boolean) => updateChannel(teamId, channel.id, { notify_strangers: next }),
    onSuccess: (c) => {
      toast.success(
        c.notify_strangers
          ? `${channel.name} tells senders they are waiting`
          : `${channel.name} no longer tells senders they are waiting`,
      );
      onChanged();
    },
  });

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="mt-2 block"
        loading={save.isPending}
        onClick={() => save.mutate(!on)}
      >
        {on ? "Stop telling them" : "Tell them"}
      </Button>
      {save.isError ? (
        <p role="alert" className="mt-1 text-sm text-destructive">
          {save.error.message}
        </p>
      ) : null}
    </>
  );
}

/**
 * Whether a run's steps are sent to the conversation before its answer. Each
 * is a message that stays, so the switch says which it will be.
 */
function EditReplyMode({
  teamId,
  channel,
  onChanged,
}: {
  teamId: string;
  channel: ListedChannel;
  onChanged: () => void;
}) {
  const events = channel.reply_mode === "events";
  const save = useMutation({
    mutationFn: (next: ReplyMode) => updateChannel(teamId, channel.id, { reply_mode: next }),
    onSuccess: (c) => {
      toast.success(
        c.reply_mode === "events"
          ? `${channel.name} sends each step, then the answer`
          : `${channel.name} sends the answer alone`,
      );
      onChanged();
    },
  });

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="mt-2 block"
        loading={save.isPending}
        onClick={() => save.mutate(events ? "final" : "events")}
      >
        {events ? "Send the answer alone" : "Send each step"}
      </Button>
      {save.isError ? (
        <p role="alert" className="mt-1 text-sm text-destructive">
          {save.error.message}
        </p>
      ) : null}
    </>
  );
}

/**
 * Where the channel answers, changed in place: each choice is saved as it is
 * made, as the reply mode is.
 */
function EditAnswersIn({
  teamId,
  channel,
  onChanged,
}: {
  teamId: string;
  channel: ListedChannel;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const save = useMutation({
    mutationFn: (next: AnswersIn) => updateChannel(teamId, channel.id, { answers_in: next }),
    onSuccess: (c) => {
      toast.success(`${channel.name} answers: ${answersInWords(c.answers_in).toLowerCase()}`);
      setOpen(false);
      onChanged();
    },
  });
  if (!open) {
    return (
      <Button variant="outline" size="sm" className="mt-2 block" onClick={() => setOpen(true)}>
        Change where it answers
      </Button>
    );
  }
  return (
    <div className="mt-2 space-y-2">
      <AnswersInChoice
        name={`answers-in-${channel.id}`}
        value={channel.answers_in ?? "everywhere"}
        onChange={(next) => save.mutate(next)}
        disabled={save.isPending}
      />
      {save.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {save.error.message}
        </p>
      ) : null}
    </div>
  );
}

/** The longest trigger word the brain takes, in characters. */
const TRIGGER_WORD_MAX = 32;
const TRIGGER_WORD_RULE = `A trigger word is one word of at most ${TRIGGER_WORD_MAX} characters, with a letter or digit in it, such as @Fduv.`;

/** Whether the brain takes w as a trigger word: one word, with a letter or digit in it. */
export function triggerWordOK(w: string): boolean {
  return [...w].length <= TRIGGER_WORD_MAX && !/\s/u.test(w) && /[\p{L}\p{N}]/u.test(w);
}

/**
 * The word that calls the channel, set or taken away in place. Taking it
 * away goes back to direct chats and mentions, which it says before it is
 * done.
 */
function EditTriggerWord({
  teamId,
  channel,
  onChanged,
}: {
  teamId: string;
  channel: ListedChannel;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [word, setWord] = useState(channel.trigger_word ?? "");
  const save = useMutation({
    mutationFn: (next: string) => updateChannel(teamId, channel.id, { trigger_word: next }),
    onSuccess: (c) => {
      toast.success(
        c.trigger_word
          ? `${channel.name} is called by ${c.trigger_word}`
          : `${channel.name} has no trigger word`,
      );
      setOpen(false);
      onChanged();
    },
  });
  if (!open) {
    return (
      <Button variant="outline" size="sm" className="mt-2 block" onClick={() => setOpen(true)}>
        {channel.trigger_word ? "Change the word" : "Set a word"}
      </Button>
    );
  }
  const typed = word.trim();
  const problem = typed !== "" && !triggerWordOK(typed) ? TRIGGER_WORD_RULE : null;
  return (
    <form
      className="mt-2 space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (typed !== "" && problem === null) save.mutate(typed);
      }}
    >
      <Input
        aria-label="Trigger word"
        placeholder="@Fduv"
        autoComplete="off"
        spellCheck={false}
        maxLength={TRIGGER_WORD_MAX}
        value={word}
        onChange={(e) => setWord(e.target.value)}
        className="w-48"
      />
      {problem ? <p className="text-sm text-destructive">{problem}</p> : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          size="sm"
          loading={save.isPending}
          disabled={typed === "" || problem !== null}
        >
          Save
        </Button>
        {channel.trigger_word ? (
          <Button type="button" size="sm" variant="ghost" onClick={() => save.mutate("")}>
            Take it away
          </Button>
        ) : null}
        <Button type="button" size="sm" variant="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
      </div>
      {channel.trigger_word ? (
        <p className="text-sm text-muted-foreground">
          Without a word, every direct chat and every mention of the number asks the agent.
        </p>
      ) : null}
      {save.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {save.error.message}
        </p>
      ) : null}
    </form>
  );
}

/**
 * Where the channel's replies go, set or stopped. Stopping gives up any reply
 * still waiting, so it says so before it is done.
 */
function EditReplyURL({
  teamId,
  channel,
  onChanged,
}: {
  teamId: string;
  channel: ListedChannel;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [url, setURL] = useState(channel.reply_url ?? "");
  const save = useMutation({
    mutationFn: (replyURL: string | null) =>
      updateChannel(teamId, channel.id, { reply_url: replyURL }),
    onSuccess: (c) => {
      setOpen(false);
      toast.success(
        c.reply_url
          ? `Replies from ${channel.name} go to ${c.reply_url}`
          : `Replies from ${channel.name} stopped`,
      );
      onChanged();
    },
  });
  // A url input drops the whitespace around what is typed or pasted, so the
  // value is already what the brain is sent.
  const typed = url;

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="mt-2 block"
        onClick={() => {
          setURL(channel.reply_url ?? "");
          save.reset();
          setOpen(true);
        }}
      >
        {channel.reply_url ? "Change where replies go" : "Set where replies go"}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Replies from {channel.name}</DialogTitle>
            <DialogDescription>
              The team's answers are posted here, signed with the channel's secret. An address with
              a query, a fragment or credentials in it is refused: everyone in the team can read it,
              and the signature is what proves a reply is real. Stopping replies gives up any reply
              still waiting to be sent.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (typed) save.mutate(typed);
            }}
            className="space-y-3"
          >
            <Field label="Reply URL" htmlFor="reply-url" hint="http or https.">
              <Input
                id="reply-url"
                type="url"
                value={url}
                placeholder="https://support.example.com/openarity/replies"
                onChange={(e) => setURL(e.target.value)}
              />
            </Field>
            {save.isError ? (
              <p role="alert" className="text-sm text-destructive">
                {save.error.message}
              </p>
            ) : null}
            <DialogFooter className="gap-2 sm:justify-between">
              {channel.reply_url ? (
                <Button
                  type="button"
                  variant="outline"
                  className="text-destructive"
                  loading={save.isPending && save.variables === null}
                  onClick={() => save.mutate(null)}
                >
                  Stop replies
                </Button>
              ) : (
                <span />
              )}
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  disabled={!typed}
                  loading={save.isPending && save.variables !== null}
                >
                  Save
                </Button>
              </div>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** Disconnecting asks first: the signing secret goes with the channel. */
function DeleteChannel({
  teamId,
  channel,
  onDeleted,
}: {
  teamId: string;
  channel: ListedChannel;
  onDeleted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteChannel(teamId, channel.id),
    onSuccess: () => {
      setOpen(false);
      toast.success(`Disconnected ${channel.name}`);
      onDeleted();
    },
  });

  return (
    <>
      <Button variant="outline" className="ml-auto text-destructive" onClick={() => setOpen(true)}>
        Disconnect
      </Button>
      <Dialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) remove.reset();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Disconnect {channel.name}?</DialogTitle>
            <DialogDescription>
              Its signing secret is deleted with it, and the provider's webhooks stop being
              accepted. Connecting it again means a new address and a new secret.
            </DialogDescription>
          </DialogHeader>
          {remove.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {remove.error.message}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Keep it
            </Button>
            <Button
              type="button"
              variant="destructive"
              loading={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Disconnect
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export type ChannelDraft = {
  name: string;
  provider: string;
  ownSecret: boolean;
  secret: string;
  /** Telegram's bot token, from @BotFather, or a Slack app's Bot User OAuth Token. */
  botToken: string;
  answersIn: AnswersIn;
  /** Whether strangers are told they wait, where the provider can tell them. */
  notify: boolean;
  /** A WhatsApp number that is the person's own: strangers are never told. */
  mine: boolean;
  /** WhatsApp's word that calls the agent, such as @Fduv; empty is none. */
  triggerWord: string;
};

const EMPTY: ChannelDraft = {
  name: "",
  provider: "custom",
  ownSecret: false,
  secret: "",
  botToken: "",
  answersIn: "everywhere",
  notify: true,
  mine: false,
  triggerWord: "",
};

/**
 * Where it answers and whether strangers are told, as the request carries
 * them: only what differs from the brain's defaults, and the notices only
 * for a provider that sends them.
 */
function settingsOf(d: ChannelDraft, provider: string): Partial<CreateChannelRequest> {
  const out: Partial<CreateChannelRequest> = {};
  if (d.answersIn !== "everywhere") out.answers_in = d.answersIn;
  if (NOTIFIES.has(provider) && (d.mine || !d.notify)) out.notify_strangers = false;
  if (provider === WHATSAPP && d.triggerWord.trim() !== "") out.trigger_word = d.triggerWord.trim();
  return out;
}

/**
 * The request the form sends, or the sentence saying why it cannot. The
 * secret is sent exactly as typed — trimming a credential changes it — and
 * left out entirely unless the provider issued one, so the brain generates it.
 * Telegram's secret is always the brain's to generate, and it needs the bot
 * token, which has no spaces in it, so the ones pasted around it are dropped.
 */
export function channelRequest(d: ChannelDraft): CreateChannelRequest | string {
  const name = d.name.trim();
  const provider = d.provider.trim();
  if (name === "") return "A channel needs a name.";
  if (name.length > MAX_NAME) return `A name is at most ${MAX_NAME} characters.`;
  if (provider === "") return "A channel needs a provider: the adapter that reads its webhooks.";
  const settings = settingsOf(d, provider);
  // A linked number has no webhook to sign; it is paired once the channel is made.
  if (provider === WHATSAPP) {
    const word = d.triggerWord.trim();
    // Without a word, the agent on someone's own number answers every
    // direct chat and every tag of them, as them.
    if (d.mine && word === "") return "Pick the word that calls the agent, such as @Fduv.";
    if (word !== "" && !triggerWordOK(word)) return TRIGGER_WORD_RULE;
    return { name, provider, ...settings };
  }
  if (provider === TELEGRAM) {
    if (d.botToken.trim() === "") return "Paste the bot token from @BotFather.";
    return { name, provider, credentials: { bot_token: d.botToken.trim() }, ...settings };
  }
  // Slack signs with its own secret, so the brain cannot make one.
  if (provider === SLACK) {
    if (d.botToken.trim() === "")
      return "Paste the bot token from the Slack app's OAuth & Permissions.";
    if (d.secret.trim() === "")
      return "Paste the signing secret from the Slack app's Basic Information.";
    return {
      name,
      provider,
      signing_secret: d.secret,
      credentials: { bot_token: d.botToken.trim() },
      ...settings,
    };
  }
  if (!d.ownSecret) return { name, provider, ...settings };
  if (d.secret.trim() === "")
    return "Paste the provider's signing secret, or let the brain make one.";
  return { name, provider, signing_secret: d.secret, ...settings };
}

/**
 * Slack's own page for making an app from a manifest, filled in: a bot that
 * reads the messages it is sent and the channels it is in, answers, and
 * reads the files people share. Slack will not take events without a
 * Request URL, and the real one exists only once the channel does, so the
 * manifest holds a placeholder the person replaces as the last step.
 */
export const SLACK_PLACEHOLDER_URL = "https://example.com/replace-with-the-openarity-request-url";

export function slackManifestURL(channelName: string): string {
  const name = channelName.trim().slice(0, 35) || "Openarity";
  const manifest = {
    display_information: {
      name,
      description: "The team's agents, answering in Slack.",
    },
    features: {
      app_home: {
        home_tab_enabled: false,
        messages_tab_enabled: true,
        messages_tab_read_only_enabled: false,
      },
      bot_user: { display_name: name, always_online: true },
    },
    oauth_config: {
      scopes: {
        bot: [
          "channels:history",
          "groups:history",
          "im:history",
          "mpim:history",
          "chat:write",
          "files:read",
        ],
      },
    },
    settings: {
      event_subscriptions: {
        request_url: SLACK_PLACEHOLDER_URL,
        bot_events: ["message.channels", "message.groups", "message.im", "message.mpim"],
      },
      org_deploy_enabled: false,
      socket_mode_enabled: false,
      token_rotation_enabled: false,
    },
  };
  return `https://api.slack.com/apps?new_app=1&manifest_json=${encodeURIComponent(JSON.stringify(manifest))}`;
}

/** Where the provider sends this channel's webhooks, on the brain's webhook listener. */
export function hookPath(c: { provider: string; id: string }): string {
  return `/hooks/${c.provider}/${c.id}`;
}

/**
 * The address to give the provider: the brain's own full URL when it knows
 * its public address, else the path alone. Never a URL built here from the
 * page's origin: the dashboard and the webhook listener are different hosts.
 */
export function hookAddress(c: { provider: string; id: string; hook_url?: string }): string {
  return c.hook_url ?? hookPath(c);
}

/**
 * The step Telegram leaves to the person: pointing the bot at the channel.
 * The bot token is a placeholder; this page never has it after the form.
 */
export function setWebhookCommand(c: {
  provider: string;
  id: string;
  hook_url?: string;
  signing_secret?: string;
}): string {
  return [
    "curl https://api.telegram.org/bot<bot token>/setWebhook \\",
    `  -d url=${hookAddress(c)} \\`,
    `  -d secret_token=${c.signing_secret ?? ""} \\`,
    `  -d 'allowed_updates=["message"]'`,
  ].join("\n");
}

/** Connecting a channel, in a sheet beside the list. */
function ConnectSheet({
  teamId,
  open,
  onClose,
  onCreated,
}: {
  teamId: string;
  open: boolean;
  onClose: () => void;
  onCreated: (c: CreatedChannel) => void;
}) {
  const [draft, setDraft] = useState<ChannelDraft>(EMPTY);
  const [problem, setProblem] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const set = (patch: Partial<ChannelDraft>) => setDraft((d) => ({ ...d, ...patch }));

  // Closing forgets everything typed, the secret included, rather than
  // keeping it for the next time the form opens.
  const close = () => {
    setDraft(EMPTY);
    setProblem(null);
    onClose();
  };

  // A plain call rather than useMutation: a mutation keeps its variables and
  // its result in the query client's cache, and both can hold a secret.
  const submit = async (body: CreateChannelRequest) => {
    setPending(true);
    setProblem(null);
    try {
      const created = await createChannel(teamId, body);
      setDraft(EMPTY);
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  };

  return (
    <Sheet open={open} onOpenChange={(o) => (o ? null : close())}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>Connect a channel</SheetTitle>
          <SheetDescription>
            The brain keeps the signing secret in its secret store and never shows it again.
          </SheetDescription>
        </SheetHeader>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            const body = channelRequest(draft);
            if (typeof body === "string") {
              setProblem(body);
              return;
            }
            void submit(body);
          }}
        >
          <Field label="Name" htmlFor="channel-name" hint="Unique in the team.">
            <Input
              id="channel-name"
              value={draft.name}
              maxLength={MAX_NAME}
              placeholder="support-tickets"
              onChange={(e) => set({ name: e.target.value })}
            />
          </Field>
          <Field label="Provider" htmlFor="channel-provider" hint="What sends its webhooks.">
            {/* A choice, not a field: a provider this build lacks could only be refused. */}
            <Select
              value={draft.provider}
              onValueChange={(provider) =>
                set({
                  provider,
                  ownSecret: false,
                  secret: "",
                  botToken: "",
                  answersIn: "everywhere",
                  notify: true,
                  mine: false,
                  triggerWord: "",
                })
              }
            >
              <SelectTrigger id="channel-provider" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PROVIDERS.map(([value, word]) => (
                  <SelectItem key={value} value={value}>
                    {word}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {draft.provider === SLACK ? (
            <SlackSetup draft={draft} set={set} />
          ) : draft.provider === TELEGRAM ? (
            <Field
              label="Bot token"
              htmlFor="channel-bot-token"
              hint="From @BotFather. Kept in the secret store; never shown again."
            >
              <Input
                id="channel-bot-token"
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                value={draft.botToken}
                onChange={(e) => set({ botToken: e.target.value })}
              />
            </Field>
          ) : draft.provider === WHATSAPP ? (
            <p className="text-sm text-muted-foreground">
              A WhatsApp number linked as a device, as WhatsApp Web is. You link it next, from the
              phone. Use a number of the bot's own: a linked device is unofficial, and a number can
              be banned.
            </p>
          ) : (
            <SwitchField
              label="The provider gave me a signing secret"
              checked={draft.ownSecret}
              onCheckedChange={(on) => set({ ownSecret: on, secret: "" })}
            />
          )}
          {draft.provider === CUSTOM && draft.ownSecret ? (
            <Field
              label="Signing secret"
              htmlFor="channel-secret"
              hint="Slack's Signing Secret, Meta's App Secret. Sent once, kept in the secret store."
            >
              {/* new-password, not off: browsers ignore off on a password field
                  and would offer the sign-in password saved for this site. */}
              <Input
                id="channel-secret"
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                value={draft.secret}
                onChange={(e) => set({ secret: e.target.value })}
              />
            </Field>
          ) : null}
          {draft.provider === WHATSAPP ? (
            <fieldset className="space-y-1.5">
              <legend className="text-sm font-medium">Whose number is it?</legend>
              <div className="grid gap-1.5 text-sm">
                {(
                  [
                    [false, "The bot's own: a number only the team uses"],
                    [true, "Mine: my own WhatsApp"],
                  ] as const
                ).map(([mine, words]) => (
                  <label key={String(mine)} className="flex cursor-pointer items-center gap-2">
                    <input
                      type="radio"
                      name="channel-whose"
                      checked={draft.mine === mine}
                      onChange={() =>
                        set(
                          mine
                            ? { mine, answersIn: "everywhere", notify: false }
                            : { mine, answersIn: "everywhere", notify: true },
                        )
                      }
                      className="size-4 accent-primary"
                    />
                    {words}
                  </label>
                ))}
              </div>
            </fieldset>
          ) : null}
          {draft.provider === WHATSAPP ? (
            <Field
              label="Trigger word"
              htmlFor="channel-trigger-word"
              hint={
                draft.mine
                  ? "The agent answers only a message with this word in it, from you in any chat or from someone you let in."
                  : "Optional. With one, a message must hold it to ask the agent, in groups and direct chats."
              }
            >
              <Input
                id="channel-trigger-word"
                placeholder="@Fduv"
                autoComplete="off"
                spellCheck={false}
                maxLength={TRIGGER_WORD_MAX}
                value={draft.triggerWord}
                onChange={(e) => set({ triggerWord: e.target.value })}
              />
            </Field>
          ) : null}
          <AnswersInChoice
            name="channel-answers-in"
            value={draft.answersIn}
            onChange={(answersIn) => set({ answersIn })}
          />
          {NOTIFIES.has(draft.provider) && !draft.mine ? (
            <SwitchField
              label="Tell someone not yet let in that they are waiting"
              checked={draft.notify}
              onCheckedChange={(notify) => set({ notify })}
            />
          ) : null}
          {draft.mine ? (
            <p role="note" className="flex gap-2 text-sm">
              <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
              <span>
                The agent answers as you, with typing shown, wherever it is called. Someone not yet
                let in is never told anything.
              </span>
            </p>
          ) : null}
          {problem ? (
            <p role="alert" className="text-xs text-destructive">
              {problem}
            </p>
          ) : null}
          <SheetFooter>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" loading={pending}>
              Connect
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}

/** One numbered step of a setup, its number in the trace's step circle. */
function SetupStep({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[1.5rem_1fr] gap-x-3 gap-y-1.5">
      <span
        aria-hidden
        className="flex size-6 items-center justify-center rounded-full bg-card text-xs font-medium tabular-nums shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
      >
        {n}
      </span>
      <p className="self-center text-sm font-medium">{title}</p>
      <div className="col-start-2 space-y-2">{children}</div>
    </li>
  );
}

/**
 * Connecting Slack is a Slack app of the team's own: made from a manifest
 * on Slack's page, installed, and its two values pasted back here. Both are
 * credentials, so both are password fields that the browser never fills.
 */
function SlackSetup({
  draft,
  set,
}: {
  draft: ChannelDraft;
  set: (patch: Partial<ChannelDraft>) => void;
}) {
  return (
    <ol className="space-y-4" aria-label="Connecting Slack">
      <SetupStep n={1} title="Create the app in Slack">
        <p className="text-xs text-muted-foreground">
          Opens Slack with everything filled in. Pick the workspace, then Create.
        </p>
        <Button asChild variant="outline" size="sm">
          <a href={slackManifestURL(draft.name)} target="_blank" rel="noopener noreferrer">
            Create the Slack app
            <ExternalLink aria-hidden className="size-3.5" />
          </a>
        </Button>
      </SetupStep>
      <SetupStep n={2} title="Install it, then copy two values">
        <p className="text-xs text-muted-foreground">
          Choose Install to Workspace. The bot token is under OAuth & Permissions, the signing
          secret under Basic Information.
        </p>
      </SetupStep>
      <SetupStep n={3} title="Paste them here">
        <Field label="Bot token" htmlFor="channel-bot-token" hint="Starts with xoxb-.">
          <Input
            id="channel-bot-token"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={draft.botToken}
            onChange={(e) => set({ botToken: e.target.value })}
          />
        </Field>
        <Field label="Signing secret" htmlFor="channel-secret" hint="Kept in the secret store.">
          <Input
            id="channel-secret"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={draft.secret}
            onChange={(e) => set({ secret: e.target.value })}
          />
        </Field>
      </SetupStep>
    </ol>
  );
}

/**
 * The step Slack leaves to the person: the manifest's placeholder Request
 * URL replaced with this channel's. Slack checks it at once, by the
 * challenge the brain echoes back.
 */
function SlackLastStep({ channel }: { channel: CreatedChannel }) {
  return (
    <ol className="space-y-4 text-sm" aria-label="One step left in Slack">
      <SetupStep n={1} title="Open Event Subscriptions in the app">
        <p className="text-xs text-muted-foreground">
          In the Slack app's settings, under Features.
        </p>
      </SetupStep>
      <SetupStep n={2} title="Replace the Request URL with this one">
        {channel.hook_url ? (
          <CopyId value={channel.hook_url} label="Request URL" />
        ) : (
          <p className="text-xs text-muted-foreground">
            This brain has no public address, so Slack cannot reach it. The path is{" "}
            <Mono className="break-all">{hookPath(channel)}</Mono> on its webhook listener.
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          Slack checks it straight away and shows Verified.
        </p>
      </SetupStep>
      <SetupStep n={3} title="Save Changes">
        <p className="text-xs text-muted-foreground">
          Then message the bot, or mention it in a channel it is in: someone writing for the first
          time waits under Approvals until you let them in.
        </p>
      </SetupStep>
    </ol>
  );
}

/**
 * What to configure at the provider. A generated secret is shown here and
 * nowhere else: it lives in this dialog's props until it closes.
 */
/**
 * What the brain said about how the provider's account is set up, such as a
 * Telegram bot whose privacy mode hides group messages. The channel is made;
 * this is what will keep it from hearing everything until it is fixed.
 */
function SetupWarnings({ warnings }: { warnings?: string[] }) {
  if (!warnings?.length) return null;
  return (
    <ul className="space-y-2">
      {warnings.map((w) => (
        <li key={w} role="alert" className="flex gap-2 text-sm">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
          <span>{w}</span>
        </li>
      ))}
    </ul>
  );
}

function CreatedDialog({
  channel,
  onDone,
}: {
  channel: CreatedChannel | null;
  onDone: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const [copiedCommand, setCopiedCommand] = useState(false);
  const done = () => {
    setCopied(false);
    setCopiedCommand(false);
    onDone();
  };

  return (
    <Dialog open={channel !== null} onOpenChange={(o) => (o ? null : done())}>
      <DialogContent className="sm:max-w-lg">
        {channel?.connected ? (
          <>
            <DialogHeader>
              <DialogTitle>Connected {channel.name}</DialogTitle>
              <DialogDescription>
                The bot now sends its messages here. Message it to try it: someone writing for the
                first time waits under Approvals until you let them in.
              </DialogDescription>
            </DialogHeader>
            <SetupWarnings warnings={channel.warnings} />
            <DialogFooter>
              <Button type="button" onClick={done}>
                Done
              </Button>
            </DialogFooter>
          </>
        ) : channel?.provider === SLACK ? (
          <>
            <DialogHeader>
              <DialogTitle>One step left in Slack</DialogTitle>
              <DialogDescription>
                {channel.name} is connected here. Slack sends its messages once it has this
                channel's Request URL.
              </DialogDescription>
            </DialogHeader>
            <SetupWarnings warnings={channel.warnings} />
            <SlackLastStep channel={channel} />
            <DialogFooter>
              <Button type="button" onClick={done}>
                Done
              </Button>
            </DialogFooter>
          </>
        ) : channel ? (
          <>
            <DialogHeader>
              <DialogTitle>Connected {channel.name}</DialogTitle>
              <DialogDescription>
                {channel.hook_url
                  ? "Point the provider's webhook at this URL."
                  : "Point the provider's webhook at this path on the brain's webhook listener."}
              </DialogDescription>
            </DialogHeader>
            <SetupWarnings warnings={channel.warnings} />
            <div className="space-y-3 text-sm">
              <div className="space-y-1">
                <p className="text-xs font-medium">
                  {channel.hook_url ? "Webhook URL" : "Webhook path"}
                </p>
                <Mono className="break-all">POST {hookAddress(channel)}</Mono>
              </div>
              {channel.signing_secret ? (
                <div className="space-y-1">
                  <p className="text-xs font-medium">Signing secret</p>
                  <p className="text-xs text-muted-foreground">
                    Shown this once. Store it now: the brain cannot show it again, and replacing it
                    means deleting the channel.
                  </p>
                  <div className="flex items-center gap-2">
                    <code className="min-w-0 flex-1 break-all rounded bg-secondary px-2 py-1 font-mono text-xs">
                      {channel.signing_secret}
                    </code>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        void navigator.clipboard
                          ?.writeText(channel.signing_secret ?? "")
                          .then(() => setCopied(true));
                      }}
                    >
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  </div>
                </div>
              ) : (
                <p className="text-xs text-muted-foreground">
                  The secret you pasted is in the secret store. The provider signs with it already.
                </p>
              )}
              {channel.provider === TELEGRAM && channel.signing_secret ? (
                <div className="space-y-1">
                  <p className="text-xs font-medium">Point the bot at this channel</p>
                  <p className="text-xs text-muted-foreground">
                    Run this once, replacing {"<bot token>"} with your bot's token. Telegram then
                    sends its messages here, each carrying the secret above.
                  </p>
                  <div className="flex items-start gap-2">
                    <code className="min-w-0 flex-1 whitespace-pre-wrap break-all rounded bg-secondary px-2 py-1 font-mono text-xs">
                      {setWebhookCommand(channel)}
                    </code>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        void navigator.clipboard
                          ?.writeText(setWebhookCommand(channel))
                          .then(() => setCopiedCommand(true));
                      }}
                    >
                      {copiedCommand ? "Copied" : "Copy"}
                    </Button>
                  </div>
                </div>
              ) : null}
            </div>
            <DialogFooter>
              <Button type="button" onClick={done}>
                {channel.signing_secret ? "I have stored it" : "Done"}
              </Button>
            </DialogFooter>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
