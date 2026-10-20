import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { createChannel, listChannels } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { CreateChannelRequest, CreatedChannel } from "@/api/types";
import { Field, ReadOnlyNote } from "@/components/registry-bits";
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
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/channels/")({
  component: Channels,
});

const WRITE = "channel:write";

/** The adapters this build ships. Others are typed; the brain refuses an unknown one by name. */
const KNOWN_PROVIDERS = ["custom"];

/** The brain's own limit on a channel's name. */
const MAX_NAME = 200;

function Channels() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [adding, setAdding] = useState(false);
  const [created, setCreated] = useState<CreatedChannel | null>(null);

  const channels = useInfiniteQuery({
    queryKey: ["channels", teamId, "paged"],
    queryFn: ({ pageParam }) => listChannels(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const rows = channels.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Channels"
        description="Where messages arrive from. Each belongs to one team and one provider."
        actions={
          canWrite ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              New channel
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
        <p className="text-sm text-muted-foreground">Loading channels…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No channels yet"
          body="A channel is a provider the brain listens to — an inbox, a support tool, a webhook. Each has a signing secret that proves a request really came from it."
          hint="oa channels create <team> support --provider custom"
        />
      ) : (
        <>
          <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
            <caption className="sr-only">Channels in this team</caption>
            <tbody className="divide-y divide-border">
              {rows.map((c) => (
                <tr key={c.id} className="hover:bg-secondary/60">
                  <td className="py-2 pl-3 text-sm font-medium">{c.name}</td>
                  <td className="py-2 text-xs text-muted-foreground">{c.provider}</td>
                  <td className="py-2 pr-3 text-right">
                    <Mono className="break-all">{c.hook_url ?? c.id}</Mono>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <LoadMore
            hasMore={Boolean(channels.hasNextPage)}
            loading={channels.isFetchingNextPage}
            onClick={() => channels.fetchNextPage()}
            loadedLabel={`${rows.length} channel${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}

      {teamId && canWrite ? (
        <CreateDialog
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onCreated={(c) => {
            qc.invalidateQueries({ queryKey: ["channels", teamId] });
            setAdding(false);
            setCreated(c);
          }}
        />
      ) : null}

      <CreatedDialog channel={created} onDone={() => setCreated(null)} />
    </div>
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

function CreateDialog({
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
    <Dialog open={open} onOpenChange={(o) => (o ? null : close())}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect a channel</DialogTitle>
          <DialogDescription>
            The brain keeps the signing secret in its secret store and never shows it again.
          </DialogDescription>
        </DialogHeader>
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
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={draft.ownSecret}
              onChange={(e) => set({ ownSecret: e.target.checked, secret: "" })}
            />
            The provider gave me a signing secret
          </label>
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
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Connecting…" : "Connect"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
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
                      variant="secondary"
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
