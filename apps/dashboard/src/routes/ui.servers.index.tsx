import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import {
  createMCPServer,
  deleteMCPServer,
  discoverMCPServer,
  listMCPServers,
  listMCPTools,
  replaceMCPServer,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { MCPServer, MCPServerRequest } from "@/api/types";
import { Time } from "@/components/facts";
import { Field, ReadOnlyNote, Tag, TextArea } from "@/components/registry-bits";
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
import { Input } from "@/components/ui/input";
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
  discoveryOf,
  draftOf,
  EMPTY_SERVER,
  editRequest,
  type ServerDraft,
  serverRequest,
} from "@/lib/server-draft";

export const Route = createFileRoute("/ui/servers/")({
  component: Servers,
});

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

  return (
    <div className="space-y-5">
      <PageHeader
        title="MCP servers"
        finding={
          failing > 0
            ? `${count(failing, "server")} failed ${failing === 1 ? "its" : "their"} last discovery.`
            : null
        }
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
          onEdit={(s) => {
            setShown(null);
            setEditing(s);
          }}
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
  onChanged,
}: {
  server: MCPServer;
  teamId: string;
  canWrite: boolean;
  onOpen: () => void;
  onChanged: () => void;
}) {
  const discover = useDiscover(teamId, server, onChanged);
  const tried = server.last_discovery_at ?? server.discovered_at;

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
      <TableCell label="Discovery">
        <StatusPill status={discoveryOf(server)} />
      </TableCell>
      <TableCell label="Last tried" className="whitespace-nowrap text-muted-foreground">
        {tried ? <Time iso={tried} /> : "Never"}
      </TableCell>
      {canWrite ? (
        <TableCell className="text-right">
          {/* The brain reaches a URL to ask; it does not start a command to. */}
          {server.url ? (
            <Button
              variant="outline"
              size="sm"
              loading={discover.isPending}
              onClick={() => discover.mutate()}
            >
              Discover
            </Button>
          ) : null}
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
  onChanged,
}: {
  teamId: string;
  server: MCPServer | null;
  canWrite: boolean;
  onClose: () => void;
  onEdit: (s: MCPServer) => void;
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
  onDeleted,
  onChanged,
}: {
  teamId: string;
  server: MCPServer;
  canWrite: boolean;
  onEdit: () => void;
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

      {canWrite ? (
        <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-rule pt-4">
          {server.url ? (
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
  open,
  onClose,
  onSaved,
}: {
  teamId: string;
  server?: MCPServer;
  open: boolean;
  onClose: () => void;
  onSaved: (s: MCPServer) => void;
}) {
  const [draft, setDraft] = useState<ServerDraft>(() => (server ? draftOf(server) : EMPTY_SERVER));
  const [problem, setProblem] = useState<string | null>(null);
  const set = (patch: Partial<ServerDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const save = useMutation({
    mutationFn: (body: MCPServerRequest) =>
      server ? replaceMCPServer(teamId, server.id, body) : createMCPServer(teamId, body),
    onSuccess: (s) => {
      setDraft(EMPTY_SERVER);
      onSaved(s);
      onClose();
      toast.success(server ? `Saved ${s.name}` : `Registered ${s.name}`);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{server ? "Edit server" : "Register an MCP server"}</DialogTitle>
          <DialogDescription>
            Secrets stay in the secret store. Write them there first, then name them here as
            teams/&lt;team_id&gt;/mcp/&lt;name&gt;#&lt;key&gt;.
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
            setProblem(null);
            save.mutate(body);
          }}
        >
          <Field
            label="Name"
            htmlFor="server-name"
            hint="Each tool reaches the model as <name>__<tool>."
          >
            <Input
              id="server-name"
              value={draft.name}
              autoComplete="off"
              onChange={(e) => set({ name: e.target.value })}
            />
          </Field>
          <fieldset className="space-y-1.5">
            <legend className="text-sm font-medium">Reached at</legend>
            <div className="flex gap-5 text-sm">
              {(["url", "command"] as const).map((t) => (
                <label key={t} className="flex cursor-pointer items-center gap-2">
                  <input
                    type="radio"
                    name="transport"
                    checked={draft.transport === t}
                    onChange={() => set({ transport: t })}
                    className="size-4 accent-primary"
                  />
                  {t === "url" ? "A URL" : "A command"}
                </label>
              ))}
            </div>
          </fieldset>
          {draft.transport === "url" ? (
            <>
              <Field label="URL" htmlFor="server-url">
                <Input
                  id="server-url"
                  value={draft.url}
                  placeholder="https://mcp.example.com/v1"
                  spellCheck={false}
                  onChange={(e) => set({ url: e.target.value })}
                />
              </Field>
              <Field label="Bearer token reference" htmlFor="server-auth" hint="Optional.">
                <Input
                  id="server-auth"
                  value={draft.authRef}
                  spellCheck={false}
                  onChange={(e) => set({ authRef: e.target.value })}
                />
              </Field>
            </>
          ) : (
            <>
              <Field label="Command" htmlFor="server-command" hint="One argument per line.">
                <TextArea
                  id="server-command"
                  value={draft.command}
                  spellCheck={false}
                  onChange={(e) => set({ command: e.target.value })}
                />
              </Field>
              <Field label="Environment" htmlFor="server-env" hint="NAME=reference, one per line.">
                <TextArea
                  id="server-env"
                  value={draft.env}
                  spellCheck={false}
                  onChange={(e) => set({ env: e.target.value })}
                />
              </Field>
            </>
          )}
          <SwitchField
            label="Bare"
            description="Its tools reach the model without the server's name in front."
            checked={draft.bare}
            onCheckedChange={(bare) => set({ bare })}
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
