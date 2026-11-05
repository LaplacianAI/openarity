import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { createChannel, deleteChannel, listChannels } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { CreateChannelRequest, CreatedChannel, ListedChannel } from "@/api/types";
import { KeyValue, KeyValues, Time } from "@/components/facts";
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

/** The adapters this build ships. Others are typed; the brain refuses an unknown one by name. */
const KNOWN_PROVIDERS = ["custom"];

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
          body="A channel is a provider the brain listens to — an inbox, a support tool, a webhook. Each has a signing secret that proves a request really came from it."
          hint="oa channels create <team> support --provider custom"
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
            setCreated(c);
          }}
        />
      ) : null}

      <CreatedDialog channel={created} onDone={() => setCreated(null)} />
    </div>
  );
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
  onDeleted,
}: {
  teamId: string;
  channel: ListedChannel;
  canWrite: boolean;
  onDeleted: () => void;
}) {
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
        <KeyValue label={channel.hook_url ? "Webhook URL" : "Webhook path"}>
          <Mono className="break-all text-foreground">POST {hookAddress(channel)}</Mono>
        </KeyValue>
        <KeyValue label="Signing secret">
          In the secret store. It was shown once, when the channel was connected.
        </KeyValue>
      </KeyValues>

      <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-rule pt-4">
        <Button variant="outline" asChild>
          <Link to="/ui/sessions" search={{ channel: channel.id }}>
            Open its sessions
          </Link>
        </Button>
        {canWrite ? (
          <DeleteChannel teamId={teamId} channel={channel} onDeleted={onDeleted} />
        ) : null}
      </div>
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
};

const EMPTY: ChannelDraft = { name: "", provider: "custom", ownSecret: false, secret: "" };

/**
 * The request the form sends, or the sentence saying why it cannot. The
 * secret is sent exactly as typed — trimming a credential changes it — and
 * left out entirely unless the provider issued one, so the brain generates it.
 */
export function channelRequest(d: ChannelDraft): CreateChannelRequest | string {
  const name = d.name.trim();
  const provider = d.provider.trim();
  if (name === "") return "A channel needs a name.";
  if (name.length > MAX_NAME) return `A name is at most ${MAX_NAME} characters.`;
  if (provider === "") return "A channel needs a provider: the adapter that reads its webhooks.";
  if (!d.ownSecret) return { name, provider };
  if (d.secret.trim() === "")
    return "Paste the provider's signing secret, or let the brain make one.";
  return { name, provider, signing_secret: d.secret };
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
          <Field
            label="Provider"
            htmlFor="channel-provider"
            hint={`The adapter that reads its webhooks. This build has: ${KNOWN_PROVIDERS.join(", ")}.`}
          >
            <Input
              id="channel-provider"
              list="channel-providers"
              value={draft.provider}
              onChange={(e) => set({ provider: e.target.value })}
            />
            <datalist id="channel-providers">
              {KNOWN_PROVIDERS.map((p) => (
                <option key={p} value={p} />
              ))}
            </datalist>
          </Field>
          <SwitchField
            label="The provider gave me a signing secret"
            checked={draft.ownSecret}
            onCheckedChange={(on) => set({ ownSecret: on, secret: "" })}
          />
          {draft.ownSecret ? (
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

/**
 * What to configure at the provider. A generated secret is shown here and
 * nowhere else: it lives in this dialog's props until it closes.
 */
function CreatedDialog({
  channel,
  onDone,
}: {
  channel: CreatedChannel | null;
  onDone: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const done = () => {
    setCopied(false);
    onDone();
  };

  return (
    <Dialog open={channel !== null} onOpenChange={(o) => (o ? null : done())}>
      <DialogContent className="sm:max-w-lg">
        {channel ? (
          <>
            <DialogHeader>
              <DialogTitle>Connected {channel.name}</DialogTitle>
              <DialogDescription>
                {channel.hook_url
                  ? "Point the provider's webhook at this URL."
                  : "Point the provider's webhook at this path on the brain's webhook listener."}
              </DialogDescription>
            </DialogHeader>
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
