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
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { MCPServer, MCPServerRequest } from "@/api/types";
import { Field, ReadOnlyNote, Tag, TextArea } from "@/components/registry-bits";
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
import { relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/servers/")({
  component: Servers,
});

const WRITE = "tool:write";

type Draft = {
  name: string;
  transport: "url" | "command";
  url: string;
  authRef: string;
  command: string;
  env: string;
  bare: boolean;
};

const EMPTY: Draft = {
  name: "",
  transport: "url",
  url: "",
  authRef: "",
  command: "",
  env: "",
  bare: false,
};

/**
 * The form as the API takes it. A command is one argument per line, because
 * splitting a line on spaces breaks every argument that has one; env is one
 * NAME=reference per line. What the brain refuses, it explains.
 */
export function serverRequest(d: Draft): MCPServerRequest | string {
  const name = d.name.trim();
  if (!name) return "A server needs a name.";
  const body: MCPServerRequest = { name };
  if (d.bare) body.bare = true;

  if (d.transport === "url") {
    if (!d.url.trim()) return "A url server needs its URL.";
    body.url = d.url.trim();
    if (d.authRef.trim()) body.auth_secret_ref = d.authRef.trim();
    return body;
  }

  const argv = d.command
    .split("\n")
    .map((a) => a.trim())
    .filter(Boolean);
  if (argv.length === 0) return "A command server needs its command, one argument per line.";
  body.command = argv;

  const env: Record<string, string> = {};
  for (const line of d.env
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)) {
    const at = line.indexOf("=");
    if (at <= 0) return `"${line}" is not NAME=reference.`;
    env[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  body.env = env;
  return body;
}

function Servers() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const servers = useInfiniteQuery({
    queryKey: ["servers", teamId, "paged"],
    queryFn: ({ pageParam }) => listMCPServers(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => qc.invalidateQueries({ queryKey: ["servers", teamId] });
  const rows = servers.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="MCP servers"
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
        <p className="text-sm text-muted-foreground">Loading servers…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No MCP servers yet"
          body="A server is a URL spoken to over streamable HTTP, or a command started over stdio. Its tools are found by discovering it."
          hint="oa mcp-servers create <team> github --url https://…"
        />
      ) : (
        <>
          <ul className="divide-y divide-border overflow-hidden rounded-md border border-border bg-card">
            {rows.map((s) => (
              <ServerRow
                key={s.id}
                server={s}
                teamId={teamId}
                canWrite={canWrite}
                open={open === s.id}
                onToggle={() => setOpen(open === s.id ? null : s.id)}
                onChanged={changed}
              />
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(servers.hasNextPage)}
            loading={servers.isFetchingNextPage}
            onClick={() => servers.fetchNextPage()}
            loadedLabel={`${rows.length} server${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}

      {teamId && canWrite ? (
        <RegisterDialog
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onCreated={(s) => {
            changed();
            setOpen(s.id);
          }}
        />
      ) : null}
    </div>
  );
}

function ServerRow({
  server,
  teamId,
  canWrite,
  open,
  onToggle,
  onChanged,
}: {
  server: MCPServer;
  teamId: string;
  canWrite: boolean;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const qc = useQueryClient();
  const discover = useMutation({
    mutationFn: () => discoverMCPServer(teamId, server.id),
    onSuccess: (tools) => {
      onChanged();
      qc.setQueryData(["tools", teamId, server.id], tools);
      toast.success(
        `${server.name}: ${tools.items.length} tool${tools.items.length === 1 ? "" : "s"}`,
      );
    },
    onError: (err: Error) => toast.error(err.message),
  });
  const remove = useMutation({
    mutationFn: () => deleteMCPServer(teamId, server.id),
    onSuccess: () => {
      onChanged();
      toast.success(`Deleted ${server.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  return (
    <li>
      <div className="flex items-center gap-3 px-3 py-2 hover:bg-secondary/60">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="min-w-0 flex-1 text-left"
        >
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{server.name}</span>
            <Tag>{server.command ? "command" : "url"}</Tag>
          </div>
          <Mono className="block truncate">{server.url ?? server.command?.join(" ")}</Mono>
        </button>
        <span className="hidden text-xs text-muted-foreground sm:inline">
          {server.discovered_at
            ? `discovered ${relativeTime(server.discovered_at)}`
            : "never discovered"}
        </span>
        {canWrite ? (
          <>
            {server.url ? (
              <Button
                variant="ghost"
                size="sm"
                className="text-xs"
                disabled={discover.isPending}
                onClick={() => discover.mutate()}
              >
                {discover.isPending ? "Discovering…" : "Discover"}
              </Button>
            ) : null}
            <Button
              variant="ghost"
              size="sm"
              className="text-xs"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Delete
            </Button>
          </>
        ) : null}
      </div>
      {open ? <Tools teamId={teamId} server={server} /> : null}
    </li>
  );
}

function Tools({ teamId, server }: { teamId: string; server: MCPServer }) {
  const tools = useQuery({
    queryKey: ["tools", teamId, server.id],
    queryFn: () => listMCPTools(teamId, server.id),
  });

  return (
    <div className="space-y-2 border-t border-border bg-surface px-3 py-3 text-xs">
      {server.auth_secret_ref ? (
        <p className="text-muted-foreground">
          Bearer token from <Mono>{server.auth_secret_ref}</Mono>
        </p>
      ) : null}
      {Object.entries(server.env).map(([k, v]) => (
        <p key={k} className="text-muted-foreground">
          <Mono className="text-foreground">{k}</Mono> from <Mono>{v}</Mono>
        </p>
      ))}
      {tools.isPending ? (
        <p className="text-muted-foreground">Loading tools…</p>
      ) : tools.isError ? (
        <p className="text-destructive">{tools.error.message}</p>
      ) : tools.data.items.length === 0 ? (
        <p className="text-muted-foreground">
          No tools recorded. Discovering the server lists them.
        </p>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border bg-card">
          {tools.data.items.map((t) => (
            <li key={t.name} className="px-3 py-1.5">
              {/* The server's own words, rendered as text and nothing else. */}
              <Mono className="text-foreground">{t.name}</Mono>
              <p className="line-clamp-2 text-muted-foreground">{t.description}</p>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function RegisterDialog({
  teamId,
  open,
  onClose,
  onCreated,
}: {
  teamId: string;
  open: boolean;
  onClose: () => void;
  onCreated: (s: MCPServer) => void;
}) {
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [problem, setProblem] = useState<string | null>(null);
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  const create = useMutation({
    mutationFn: (body: MCPServerRequest) => createMCPServer(teamId, body),
    onSuccess: (s) => {
      setDraft(EMPTY);
      onCreated(s);
      onClose();
      toast.success(`Registered ${s.name}`);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Register an MCP server</DialogTitle>
          <DialogDescription>
            Secrets stay in the secret store. Write them there first, then name them here as
            teams/&lt;team_id&gt;/mcp/&lt;name&gt;#&lt;key&gt;.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            const body = serverRequest(draft);
            if (typeof body === "string") {
              setProblem(body);
              return;
            }
            setProblem(null);
            create.mutate(body);
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
              onChange={(e) => set({ name: e.target.value })}
            />
          </Field>
          <div className="flex gap-4 text-xs">
            {(["url", "command"] as const).map((t) => (
              <label key={t} className="flex items-center gap-1.5">
                <input
                  type="radio"
                  name="transport"
                  checked={draft.transport === t}
                  onChange={() => set({ transport: t })}
                />
                {t === "url" ? "URL" : "Command"}
              </label>
            ))}
          </div>
          {draft.transport === "url" ? (
            <>
              <Field label="URL" htmlFor="server-url">
                <Input
                  id="server-url"
                  value={draft.url}
                  placeholder="https://mcp.example.com/v1"
                  onChange={(e) => set({ url: e.target.value })}
                />
              </Field>
              <Field label="Bearer token reference" htmlFor="server-auth" hint="Optional.">
                <Input
                  id="server-auth"
                  value={draft.authRef}
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
                  onChange={(e) => set({ command: e.target.value })}
                />
              </Field>
              <Field label="Environment" htmlFor="server-env" hint="NAME=reference, one per line.">
                <TextArea
                  id="server-env"
                  value={draft.env}
                  onChange={(e) => set({ env: e.target.value })}
                />
              </Field>
            </>
          )}
          <label className="flex items-center gap-1.5 text-xs">
            <input
              type="checkbox"
              checked={draft.bare}
              onChange={(e) => set({ bare: e.target.checked })}
            />
            Bare: tools reach the model without the server's name in front
          </label>
          {problem ? (
            <p role="alert" className="text-xs text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Registering…" : "Register"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
