import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  createMCPServer,
  deleteMCPServer,
  disconnectMCPServerOAuth,
  discoverMCPServer,
  forgetMCPServerOAuthRegistration,
  listMCPServers,
  listMCPTools,
  putMCPSecret,
  replaceMCPServer,
  setServerTrustHints,
  startMCPServerOAuth,
} from "@/api";
import { ApiError, nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { MCPServer, MCPServerRequest } from "@/api/types";
import { Time } from "@/components/facts";
import { ReadOnlyNote, Tag } from "@/components/registry-bits";
import { ServerFields } from "@/components/server-fields";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Sheet,
  SheetContent,
  SheetDescription,
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
import {
  CLIENT_SECRET_KEY,
  clientSecretName,
  clientSecretRef,
  discoveryOf,
  draftOf,
  EMPTY_SERVER,
  editRequest,
  SECRET_NAME,
  type ServerDraft,
  serverRequest,
  signInOf,
  TOKEN_KEY,
  tokenRef,
} from "@/lib/server-draft";

/** What a sign-in's callback came back with, as the brain names it. */
const OUTCOMES = [
  "connected",
  "connected-no-tools",
  "expired",
  "denied",
  "refused",
  "failed",
  "unregistered",
] as const;
type Outcome = (typeof OUTCOMES)[number];

type ServersSearch = { oauth?: Outcome; server?: string };

function serversSearch(search: Record<string, unknown>): ServersSearch {
  const { oauth, server } = search;
  return {
    oauth: OUTCOMES.includes(oauth as Outcome) ? (oauth as Outcome) : undefined,
    server: typeof server === "string" && server !== "" ? server : undefined,
  };
}

export const Route = createFileRoute("/ui/servers/")({
  validateSearch: serversSearch,
  component: Servers,
});

/** The sentence a sign-in's outcome is told in, and whether it went well. */
export function outcomeMessage(outcome: Outcome, name: string): { ok: boolean; text: string } {
  switch (outcome) {
    case "connected":
      return { ok: true, text: `${name} is connected.` };
    case "connected-no-tools":
      return {
        ok: true,
        text: `${name} is connected, but its tools could not be listed. Discover it to try again.`,
      };
    case "expired":
      return {
        ok: false,
        text: "That sign-in took longer than ten minutes, or was used already. Connect again.",
      };
    case "denied":
      return { ok: false, text: `The sign-in to ${name} was declined, so nothing changed.` };
    case "refused":
      return {
        ok: false,
        text: `The sign-in to ${name} came back from somewhere it should not have, so it was refused.`,
      };
    case "failed":
      return { ok: false, text: `The sign-in to ${name} could not be finished. Connect again.` };
    case "unregistered":
      return {
        ok: false,
        text: `${name} no longer accepts openarity's registration. Connect again to register anew.`,
      };
  }
}

/** Whether a Connect was refused because the server's sign-in registers no apps. */
function registersNobody(err: Error): boolean {
  return (
    err instanceof ApiError && err.status === 409 && err.message.includes("register themselves")
  );
}

/**
 * Sends the browser to sign in: the brain begins it, registering openarity
 * with the server's sign-in first when it has to, and says where. A server
 * whose sign-in registers nobody is offered the way to a client of its own.
 */
function useConnect(teamId: string, server: MCPServer, onNeedsClient?: (s: MCPServer) => void) {
  return useMutation({
    mutationFn: () => startMCPServerOAuth(teamId, server.id),
    onSuccess: ({ authorize_url }) => window.location.assign(authorize_url),
    onError: (err: Error) => {
      if (registersNobody(err)) {
        toast.error(
          `${server.name} can't register automatically: its sign-in needs a client you register yourself.`,
          onNeedsClient
            ? { action: { label: "Edit server", onClick: () => onNeedsClient(server) } }
            : undefined,
        );
        return;
      }
      toast.error(`${server.name} could not be connected: ${err.message}`);
    },
  });
}

/**
 * What needs the reader: servers whose last discovery failed, else the
 * servers nobody has asked, whose tools no one here has seen.
 */
export function serversFinding(failing: number, rows: MCPServer[]): string | null {
  if (failing > 0) {
    return `${count(failing, "server")} failed ${failing === 1 ? "its" : "their"} last discovery.`;
  }
  const unasked = rows.filter((s) => discoveryOf(s) === "not discovered").length;
  if (unasked === 0) return null;
  return `${count(unasked, "server")} ${unasked === 1 ? "has" : "have"} never been asked for ${unasked === 1 ? "its" : "their"} tools.`;
}

const WRITE = "tool:write";

/** What a server is reached at: its URL, or its command as it would be typed. */
function addressOf(s: MCPServer): string {
  return s.url ?? s.command?.join(" ") ?? "";
}

function Servers() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const summary = useTeamSummary(teamId);
  const [shown, setShown] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<MCPServer | null>(null);
  // Opened from a Connect that needs a client registered by hand: the form
  // starts there rather than on registering automatically.
  const [editOwnClient, setEditOwnClient] = useState(false);
  const edit = (s: MCPServer, ownClient = false) => {
    setShown(null);
    setEditOwnClient(ownClient);
    setEditing(s);
  };
  const needsClient = (s: MCPServer) => edit(s, true);
  const { oauth, server: signedInto } = Route.useSearch();
  const navigate = Route.useNavigate();

  const servers = useInfiniteQuery({
    queryKey: ["servers", teamId, "paged"],
    queryFn: ({ pageParam }) => listMCPServers(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => {
    void qc.invalidateQueries({ queryKey: ["servers", teamId] });
    void qc.invalidateQueries({ queryKey: ["summary", teamId] });
  };
  const rows = servers.data?.pages.flatMap((p) => p.items) ?? [];
  const failing = summary.data?.servers_failing ?? 0;
  const open = rows.find((s) => s.id === shown) ?? null;

  // Back from a sign-in: say how it went, once, and take it out of the URL so
  // a reload does not say it again.
  const returned = rows.find((s) => s.id === signedInto);
  const loaded = !servers.isPending;
  useEffect(() => {
    if (!oauth || !loaded) return;
    const { ok, text } = outcomeMessage(oauth, returned?.name ?? "The server");
    if (ok) toast.success(text);
    else toast.error(text);
    void navigate({ search: {}, replace: true });
  }, [oauth, loaded, returned?.name, navigate]);

  return (
    <div className="space-y-5">
      <PageHeader
        title="MCP servers"
        finding={serversFinding(failing, rows)}
        description="The tool servers a team's agents can be granted. Credentials are references into the secret store, never the secret."
        actions={
          canWrite ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              Register server
            </Button>
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="MCP servers" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its MCP servers." />
      ) : servers.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading servers">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : servers.isError ? (
        <ErrorState
          what="the MCP servers"
          error={servers.error}
          onRetry={() => servers.refetch()}
          retrying={servers.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No MCP servers yet"
          body="A server is a URL spoken to over streamable HTTP, or a command started over stdio. Its tools are found by discovering it."
        />
      ) : (
        <div>
          <Table aria-label="MCP servers in this team">
            <TableHeader>
              <tr>
                <TableHead>Server</TableHead>
                <TableHead>Sign-in</TableHead>
                <TableHead>Discovery</TableHead>
                <TableHead>Last tried</TableHead>
                {canWrite ? (
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                ) : null}
              </tr>
            </TableHeader>
            <TableBody>
              {rows.map((s) => (
                <ServerRow
                  key={s.id}
                  server={s}
                  teamId={teamId}
                  canWrite={canWrite}
                  onOpen={() => setShown(s.id)}
                  onNeedsClient={needsClient}
                  onChanged={changed}
                />
              ))}
            </TableBody>
          </Table>
          <LoadMore
            hasMore={Boolean(servers.hasNextPage)}
            loading={servers.isFetchingNextPage}
            onClick={() => servers.fetchNextPage()}
            loadedLabel={`${rows.length} server${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}

      {teamId ? (
        <ServerSheet
          teamId={teamId}
          server={open}
          canWrite={canWrite}
          onClose={() => setShown(null)}
          onEdit={(s) => edit(s)}
          onNeedsClient={needsClient}
          onChanged={changed}
        />
      ) : null}

      {teamId && canWrite ? (
        <ServerDialog
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onSaved={(s) => {
            changed();
            setShown(s.id);
          }}
        />
      ) : null}
      {/* Mounted per server, so each edit starts from that server as it is. */}
      {teamId && canWrite && editing ? (
        <ServerDialog
          key={editing.id}
          teamId={teamId}
          server={editing}
          ownClient={editOwnClient}
          open
          onClose={() => setEditing(null)}
          onSaved={(s) => {
            changed();
            // A changed transport is discovered again; its tools may be new.
            void qc.invalidateQueries({ queryKey: ["tools", teamId, s.id] });
          }}
        />
      ) : null}
    </div>
  );
}

/** Asks the server for its tools now, and says what came back. */
function useDiscover(teamId: string, server: MCPServer, onChanged: () => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => discoverMCPServer(teamId, server.id),
    onSuccess: (tools) => {
      onChanged();
      qc.setQueryData(["tools", teamId, server.id], tools);
      toast.success(`${server.name} answered with ${count(tools.items.length, "tool")}.`);
    },
    onError: (err: Error) => {
      // The brain records the failure on the server, so the row says it too.
      onChanged();
      toast.error(`${server.name} did not answer: ${err.message}`);
    },
  });
}

function ServerRow({
  server,
  teamId,
  canWrite,
  onOpen,
  onNeedsClient,
  onChanged,
}: {
  server: MCPServer;
  teamId: string;
  canWrite: boolean;
  onOpen: () => void;
  onNeedsClient: (s: MCPServer) => void;
  onChanged: () => void;
}) {
  const discover = useDiscover(teamId, server, onChanged);
  const connect = useConnect(teamId, server, onNeedsClient);
  const tried = server.last_discovery_at ?? server.discovered_at;
  const signIn = signInOf(server);

  return (
    <TableRow onOpen={onOpen}>
      <TableCell className="sm:w-full sm:max-w-0">
        <div className="min-w-0 flex-1">
          <span className="flex min-w-0 items-baseline gap-2">
            <button
              type="button"
              tabIndex={-1}
              onClick={onOpen}
              className="truncate text-left font-medium hover:underline"
            >
              {server.name}
            </button>
            <Tag>{server.command ? "command" : "url"}</Tag>
          </span>
          <Mono className="block min-w-0 truncate">{addressOf(server)}</Mono>
        </div>
      </TableCell>
      <TableCell label="Sign-in">
        {signIn ? (
          <StatusPill status={signIn} />
        ) : (
          <span className="text-xs text-muted-foreground">
            {server.auth_secret_ref || Object.keys(server.env ?? {}).length > 0 ? "Token" : "None"}
          </span>
        )}
      </TableCell>
      <TableCell label="Discovery">
        <StatusPill status={discoveryOf(server)} />
      </TableCell>
      <TableCell label="Last tried" className="whitespace-nowrap text-muted-foreground">
        {tried ? <Time iso={tried} /> : "Never"}
      </TableCell>
      {canWrite ? (
        <TableCell className="text-right">
          {/* A command server is asked in the team's sandbox, which starts it. */}
          {signIn && signIn !== "connected" ? (
            <Button
              variant="outline"
              size="sm"
              loading={connect.isPending}
              onClick={() => connect.mutate()}
            >
              {signIn === "needs sign-in" ? "Reconnect" : "Connect"}
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              loading={discover.isPending}
              onClick={() => discover.mutate()}
            >
              Discover
            </Button>
          )}
        </TableCell>
      ) : null}
    </TableRow>
  );
}

/**
 * One server read whole, beside the list: where it is, what it answered last,
 * the references it is given, and the tools it offers.
 */
function ServerSheet({
  teamId,
  server,
  canWrite,
  onClose,
  onEdit,
  onNeedsClient,
  onChanged,
}: {
  teamId: string;
  server: MCPServer | null;
  canWrite: boolean;
  onClose: () => void;
  onEdit: (s: MCPServer) => void;
  onNeedsClient: (s: MCPServer) => void;
  onChanged: () => void;
}) {
  return (
    <Sheet open={server !== null} onOpenChange={(o) => (o ? null : onClose())}>
      <SheetContent>
        {server ? (
          <ServerDetail
            teamId={teamId}
            server={server}
            canWrite={canWrite}
            onEdit={() => onEdit(server)}
            onNeedsClient={onNeedsClient}
            onDeleted={() => {
              onChanged();
              onClose();
            }}
            onChanged={onChanged}
          />
        ) : null}
      </SheetContent>
    </Sheet>
  );
}

function ServerDetail({
  teamId,
  server,
  canWrite,
  onEdit,
  onNeedsClient,
  onDeleted,
  onChanged,
}: {
  teamId: string;
  server: MCPServer;
  canWrite: boolean;
  onEdit: () => void;
  onNeedsClient: (s: MCPServer) => void;
  onDeleted: () => void;
  onChanged: () => void;
}) {
  const discover = useDiscover(teamId, server, onChanged);
  const tools = useQuery({
    queryKey: ["tools", teamId, server.id],
    queryFn: () => listMCPTools(teamId, server.id),
  });
  const tried = server.last_discovery_at ?? server.discovered_at;
  const env = Object.entries(server.env ?? {});

  return (
    <>
      <SheetHeader>
        <SheetTitle className="flex items-center gap-2">
          {server.name}
          <Tag>{server.command ? "command" : "url"}</Tag>
        </SheetTitle>
        <SheetDescription asChild>
          <Mono className="block break-all">{addressOf(server)}</Mono>
        </SheetDescription>
      </SheetHeader>

      <section aria-labelledby="server-discovery" className="space-y-2">
        <h3 id="server-discovery" className="text-sm font-medium">
          Discovery
        </h3>
        <p className="flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
          <StatusPill status={discoveryOf(server)} className="text-sm text-foreground" />
          {tried ? (
            <span>
              last tried <Time iso={tried} />
            </span>
          ) : (
            <span>Never tried.</span>
          )}
        </p>
        {server.command ? (
          <p className="text-sm text-muted-foreground">
            It runs in the team's sandbox, chosen in Settings. The first discovery starts it, which
            can take a minute and a half.
          </p>
        ) : null}
        {server.last_discovery_error ? (
          // The brain's words, as the caller was told them.
          <p className="rounded-lg bg-failed/8 px-3 py-2 font-mono text-xs text-failed">
            {server.last_discovery_error}
          </p>
        ) : null}
        {server.bare ? (
          <p className="text-xs text-muted-foreground">
            Bare: its tools reach the model without the server's name in front.
          </p>
        ) : null}
      </section>

      {signInOf(server) ? (
        <SignInSection
          teamId={teamId}
          server={server}
          canWrite={canWrite}
          onNeedsClient={onNeedsClient}
          onChanged={onChanged}
        />
      ) : null}

      {server.auth_secret_ref || env.length > 0 ? (
        <section aria-labelledby="server-secrets" className="space-y-2">
          <h3 id="server-secrets" className="text-sm font-medium">
            Credentials
          </h3>
          <ul className="space-y-1 text-xs text-muted-foreground">
            {server.auth_secret_ref ? (
              <li>
                Bearer token from <Mono className="text-foreground">{server.auth_secret_ref}</Mono>
              </li>
            ) : null}
            {env.map(([k, v]) => (
              <li key={k}>
                <Mono className="text-foreground">{k}</Mono> from <Mono>{v}</Mono>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section aria-labelledby="server-tools" className="space-y-2">
        <h3 id="server-tools" className="text-sm font-medium">
          Tools
          {tools.data ? (
            <span className="ml-1.5 font-normal text-muted-foreground">
              {tools.data.items.length}
            </span>
          ) : null}
        </h3>
        {tools.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : tools.isError ? (
          <ErrorState what="its tools" error={tools.error} onRetry={() => tools.refetch()} />
        ) : tools.data.items.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No tools recorded yet. Discovering the server lists them.
          </p>
        ) : (
          <ul className="divide-y divide-rule rounded-lg bg-card">
            {tools.data.items.map((t) => (
              <li key={t.name} className="space-y-0.5 px-3 py-2">
                {/* The server's own words, shown as text and nothing else. */}
                <Mono className="text-foreground">{t.name}</Mono>
                {t.description ? (
                  <p className="line-clamp-2 text-xs text-muted-foreground">{t.description}</p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </section>

      <TrustHints teamId={teamId} server={server} onChanged={onChanged} />

      {canWrite ? (
        <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-rule pt-4">
          {/* A server that signs in has nothing to say until it is connected:
              Connect, above, is what finds its tools. */}
          {(signInOf(server) ?? "connected") === "connected" ? (
            <Button
              variant="outline"
              loading={discover.isPending}
              onClick={() => discover.mutate()}
            >
              Discover
            </Button>
          ) : null}
          <Button variant="outline" onClick={onEdit}>
            Edit
          </Button>
          <DeleteServer teamId={teamId} server={server} onDeleted={onDeleted} />
        </div>
      ) : null}
    </>
  );
}

const TRUST = "team:settings";

/**
 * Whether the server's own read-only hints are believed when its calls are
 * classed for approval. A server that calls a tool read-only is taking its own
 * word for it, so this is a team setting rather than a server edit.
 */
function TrustHints({
  teamId,
  server,
  onChanged,
}: {
  teamId: string;
  server: MCPServer;
  onChanged: () => void;
}) {
  const perms = usePermissions(teamId);
  const may = perms.can(TRUST);
  const trust = useMutation({
    mutationFn: (on: boolean) => setServerTrustHints(teamId, server.id, on),
    onSuccess: () => onChanged(),
    onError: (err: Error) => toast.error(err.message),
  });
  const on = trust.data?.trust_hints ?? server.trust_hints ?? false;
  return (
    <section aria-labelledby="server-trust" className="space-y-2">
      <h3 id="server-trust" className="text-sm font-medium">
        Approvals
      </h3>
      <SwitchField
        label="Trust this server's tool hints"
        description="A server marks some of its tools read-only. Trusting that lets those tools past approvals on its word alone, so turn it on only for a server you run or have reviewed."
        checked={on}
        disabled={!may || perms.isPending || trust.isPending}
        onCheckedChange={(v) => trust.mutate(v)}
      />
      {!may && !perms.isPending ? (
        <p className="text-xs text-muted-foreground">
          Changing it needs <span className="font-mono text-foreground">{TRUST}</span> in the team.
        </p>
      ) : null}
    </section>
  );
}

/**
 * A server that signs in: whether it is, who connected it and when, what it
 * may do, and why it stopped when it did.
 */
function SignInSection({
  teamId,
  server,
  canWrite,
  onNeedsClient,
  onChanged,
}: {
  teamId: string;
  server: MCPServer;
  canWrite: boolean;
  onNeedsClient: (s: MCPServer) => void;
  onChanged: () => void;
}) {
  const connect = useConnect(teamId, server, onNeedsClient);
  const registration = server.oauth_client_id ? null : server.oauth_registration;
  const signIn = signInOf(server) ?? "not connected";
  const c = server.connection;

  return (
    <section aria-labelledby="server-sign-in" className="space-y-2">
      <h3 id="server-sign-in" className="text-sm font-medium">
        Sign-in
      </h3>
      <p className="flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
        <StatusPill status={signIn} className="text-sm text-foreground" />
        {c ? (
          <span>
            connected <Time iso={c.connected_at} />
          </span>
        ) : (
          <span>Nobody has connected it yet.</span>
        )}
      </p>
      {c?.status === "needs_sign_in" && c.reason ? (
        <p className="rounded-lg bg-failed/8 px-3 py-2 font-mono text-xs text-failed">{c.reason}</p>
      ) : null}
      <ul className="space-y-1 text-xs text-muted-foreground">
        {server.oauth_client_id ? (
          <li>
            Client <Mono className="text-foreground">{server.oauth_client_id}</Mono>
          </li>
        ) : registration ? (
          <li>
            Registered automatically <Time iso={registration.registered_at} /> · client{" "}
            <Mono className="text-foreground">{registration.client_id}</Mono>
          </li>
        ) : (
          <li>openarity registers a client with its sign-in when you connect.</li>
        )}
        {c && c.scopes_granted.length > 0 ? (
          <li>
            Granted <Mono className="text-foreground">{c.scopes_granted.join(" ")}</Mono>
          </li>
        ) : server.oauth_scopes?.length ? (
          <li>
            Asks for <Mono className="text-foreground">{server.oauth_scopes.join(" ")}</Mono>
          </li>
        ) : null}
      </ul>
      {canWrite ? (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            loading={connect.isPending}
            onClick={() => connect.mutate()}
          >
            {signIn === "not connected" ? "Connect" : "Reconnect"}
          </Button>
          {c ? <DisconnectServer teamId={teamId} server={server} onChanged={onChanged} /> : null}
          {registration ? (
            <RegisterAgain
              teamId={teamId}
              server={server}
              onForgotten={() => {
                onChanged();
                connect.mutate();
              }}
            />
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function DisconnectServer({
  teamId,
  server,
  onChanged,
}: {
  teamId: string;
  server: MCPServer;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const disconnect = useMutation({
    mutationFn: () => disconnectMCPServerOAuth(teamId, server.id),
    onSuccess: () => {
      setOpen(false);
      toast.success(`Disconnected ${server.name}`);
      onChanged();
    },
  });

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="text-destructive"
        onClick={() => setOpen(true)}
      >
        Disconnect
      </Button>
      <Dialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) disconnect.reset();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Disconnect {server.name}?</DialogTitle>
            <DialogDescription>
              Its tokens are deleted and revoked with its provider. Agents' calls to it fail until
              someone connects it again; the server and its tools stay.
            </DialogDescription>
          </DialogHeader>
          {disconnect.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {disconnect.error.message}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Keep it
            </Button>
            <Button
              type="button"
              variant="destructive"
              loading={disconnect.isPending}
              onClick={() => disconnect.mutate()}
            >
              Disconnect
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/**
 * Forgets the client openarity registered and connects again, registering a
 * new one: for a provider that stopped accepting the old client on its own
 * page, which never sends the person back to say so.
 */
function RegisterAgain({
  teamId,
  server,
  onForgotten,
}: {
  teamId: string;
  server: MCPServer;
  onForgotten: () => void;
}) {
  const [open, setOpen] = useState(false);
  const forget = useMutation({
    mutationFn: () => forgetMCPServerOAuthRegistration(teamId, server.id),
    onSuccess: () => {
      setOpen(false);
      onForgotten();
    },
  });

  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
        Register again
      </Button>
      <Dialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) forget.reset();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Register {server.name} again?</DialogTitle>
            <DialogDescription>
              openarity forgets the client it registered and ends the current sign-in, then
              registers a new client and sends you to sign in. Use this when the sign-in page says
              it doesn't know openarity.
            </DialogDescription>
          </DialogHeader>
          {forget.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {forget.error.message}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Keep it
            </Button>
            <Button type="button" loading={forget.isPending} onClick={() => forget.mutate()}>
              Register again
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function DeleteServer({
  teamId,
  server,
  onDeleted,
}: {
  teamId: string;
  server: MCPServer;
  onDeleted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteMCPServer(teamId, server.id),
    onSuccess: () => {
      setOpen(false);
      toast.success(`Deleted ${server.name}`);
      onDeleted();
    },
  });

  return (
    <>
      <Button variant="outline" className="ml-auto text-destructive" onClick={() => setOpen(true)}>
        Delete
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
            <DialogTitle>Delete {server.name}?</DialogTitle>
            <DialogDescription>
              A server an agent is granted cannot be deleted; take it from the agent first.
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
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** Registers a server, or, given one, edits it. */
function ServerDialog({
  teamId,
  server,
  ownClient = false,
  open,
  onClose,
  onSaved,
}: {
  teamId: string;
  server?: MCPServer;
  /** Start on a client registered by hand: the server's sign-in registers nobody. */
  ownClient?: boolean;
  open: boolean;
  onClose: () => void;
  onSaved: (s: MCPServer) => void;
}) {
  const [draft, setDraft] = useState<ServerDraft>(() => {
    if (!server) return EMPTY_SERVER;
    const d = draftOf(server);
    return ownClient ? { ...d, client: "own" } : d;
  });
  const [problem, setProblem] = useState<string | null>(null);
  const set = (patch: Partial<ServerDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const qc = useQueryClient();
  const save = useMutation({
    // A token is written first and the server then names it: a server saved
    // before its secret exists would fail its first discovery.
    mutationFn: async ({
      body,
      token,
      clientSecret,
    }: {
      body: MCPServerRequest;
      token: string;
      clientSecret: string;
    }) => {
      if (token) {
        await putMCPSecret(teamId, body.name, { [TOKEN_KEY]: token });
        body.auth_secret_ref = tokenRef(teamId, body.name);
      }
      if (clientSecret) {
        await putMCPSecret(teamId, clientSecretName(body.name), {
          [CLIENT_SECRET_KEY]: clientSecret,
        });
        body.oauth_client_secret_ref = clientSecretRef(teamId, body.name);
      }
      const s = server
        ? await replaceMCPServer(teamId, server.id, body)
        : await createMCPServer(teamId, body);
      // With a new token, or a new server, its tools are asked for at once. A
      // server that signs in has none to give until someone connects it.
      if (s.url && s.auth_kind !== "oauth" && (token || !server)) {
        await discoverMCPServer(teamId, s.id).catch(() => undefined);
        void qc.invalidateQueries({ queryKey: ["tools", teamId, s.id] });
      }
      return s;
    },
    onSuccess: (s) => {
      setDraft(EMPTY_SERVER);
      onSaved(s);
      onClose();
      toast.success(
        server
          ? `Saved ${s.name}`
          : s.auth_kind === "oauth"
            ? `Registered ${s.name}. Connect it to sign in.`
            : `Registered ${s.name}`,
      );
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{server ? "Edit server" : "Register an MCP server"}</DialogTitle>
          <DialogDescription>
            A token typed here goes to the secret store and is never shown again.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            const body = server ? editRequest(server, draft) : serverRequest(draft);
            if (typeof body === "string") {
              setProblem(body);
              return;
            }
            const url = draft.transport === "url";
            const token = url && draft.auth === "token" ? draft.token.trim() : "";
            const clientSecret =
              url && draft.auth === "oauth" && draft.client === "own"
                ? draft.clientSecret.trim()
                : "";
            if ((token || clientSecret) && !SECRET_NAME.test(clientSecretName(body.name))) {
              setProblem(
                "To keep a secret here, the server's name is letters, digits, _ and - only.",
              );
              return;
            }
            setProblem(null);
            save.mutate({ body, token, clientSecret });
          }}
        >
          <ServerFields
            draft={draft}
            set={set}
            withToken
            tokenSet={Boolean(server?.auth_secret_ref)}
            clientSecretSet={Boolean(server?.oauth_client_secret_ref)}
          />
          {problem ? (
            <p role="alert" className="text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              {server ? "Save" : "Register"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
