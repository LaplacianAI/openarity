import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, useRouterState } from "@tanstack/react-router";
import {
  ArchiveRestore,
  BookOpen,
  Bot,
  ChevronsUpDown,
  Ellipsis,
  Home,
  Inbox,
  LogOut,
  MessagesSquare,
  Moon,
  Network,
  Radio,
  Server,
  Settings,
  Sun,
  Users,
  X,
} from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { useState } from "react";
import { fetchWhoami, listChannels, listDeletedTeams, listPendingSenders } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam, useToken } from "@/api/session";
import { setToken } from "@/api/token";
import { Mark } from "@/components/mark";
import { SignIn } from "@/components/sign-in";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useMediaQuery } from "@/lib/media";
import { isActive } from "@/lib/nav";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/ui")({
  component: UiLayout,
});

type NavItem = {
  to: string;
  label: string;
  icon: typeof Home;
  exact: boolean;
};

const HOME: NavItem = { to: "/ui", label: "Home", icon: Home, exact: true };
const SESSIONS: NavItem = {
  to: "/ui/sessions",
  label: "Sessions",
  icon: MessagesSquare,
  exact: false,
};
const APPROVALS: NavItem = { to: "/ui/approvals", label: "Approvals", icon: Inbox, exact: false };
const TEAMS: NavItem = { to: "/ui/teams", label: "Teams", icon: Users, exact: false };
const DELETED_TEAMS: NavItem = {
  to: "/ui/teams/deleted",
  label: "Deleted teams",
  icon: ArchiveRestore,
  exact: false,
};

type NavGroup = { label: string; items: readonly NavItem[] };

/**
 * The navigation, grouped by the job a screen is for rather than the noun it
 * lists. Runs joins Work once the brain can list a team's runs. Deleted teams
 * sits under Teams, for super admins only.
 */
export function navGroups(superAdmin: boolean): readonly NavGroup[] {
  return [
    { label: "Work", items: [HOME, SESSIONS, APPROVALS] },
    {
      label: "Build",
      items: [
        { to: "/ui/agents", label: "Agents", icon: Bot, exact: false },
        { to: "/ui/skills", label: "Skills", icon: BookOpen, exact: false },
        { to: "/ui/servers", label: "MCP servers", icon: Server, exact: false },
        { to: "/ui/channels", label: "Channels", icon: Radio, exact: false },
      ],
    },
    {
      label: "Understand",
      items: [{ to: "/ui/graph", label: "Graph", icon: Network, exact: false }],
    },
    {
      label: "Team",
      items: [
        { to: "/ui/settings", label: "Team settings", icon: Settings, exact: false },
        TEAMS,
        ...(superAdmin ? [DELETED_TEAMS] : []),
      ],
    },
  ];
}

/** Every entry, in the navigation's order. */
export function navFor(superAdmin: boolean): readonly NavItem[] {
  return navGroups(superAdmin).flatMap((g) => g.items);
}

/**
 * Whether the sidebar offers Deleted teams. Super admin is read from the
 * current team's permissions, so once the last live team is deleted there is
 * no team to ask; then the deleted list itself answers, because the brain
 * gives deleted rows to a super admin and live ones to anyone else.
 */
export function useOffersDeletedTeams(): boolean {
  const { teamId, isPending } = useCurrentTeam();
  const { superAdmin } = usePermissions(teamId);
  const deleted = useQuery({
    queryKey: ["teams", "deleted", "any"],
    queryFn: () => listDeletedTeams(),
    enabled: !isPending && !teamId,
    staleTime: 60_000,
  });
  return superAdmin || Boolean(deleted.data?.items.some((t) => t.deleted_at));
}

/**
 * Senders waiting across every channel. The brain counts per channel, so this
 * costs a request for each; it is read once a minute rather than per page.
 * Null until known, and a trailing + when a channel has more than one page.
 */
function useWaiting(teamId: string | null): string | null {
  const waiting = useQuery({
    queryKey: ["pending", teamId, "count"],
    queryFn: async () => {
      const channels = await listChannels(teamId as string);
      const pages = await Promise.all(
        channels.items.map((c) => listPendingSenders(teamId as string, c.id)),
      );
      const n = pages.reduce((sum, p) => sum + p.items.length, 0);
      return { n, more: pages.some((p) => Boolean(p.next_cursor)) };
    },
    enabled: Boolean(teamId),
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
  if (!waiting.data || waiting.data.n === 0) return null;
  return `${waiting.data.n}${waiting.data.more ? "+" : ""}`;
}

/** The entry a path belongs to: the longest that matches, so a nested page lights only itself. */
function activeOf(items: readonly NavItem[], pathname: string): string | undefined {
  return items.filter((i) => isActive(pathname, i)).sort((a, b) => b.to.length - a.to.length)[0]
    ?.to;
}

function UiLayout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const token = useToken();

  // The callback is the one route that must render without a token, because
  // obtaining one is what it does. Gating it sends the browser back to the
  // sign-in screen holding an authorization code nothing will ever redeem —
  // and the loop looks exactly like a provider that refused the login.
  const completingLogin = pathname.startsWith("/ui/callback");
  // The states gallery is reviewed without a brain, so it has no shell to
  // fetch for. Outside a dev build its route renders a bare "Not found".
  const bare = completingLogin || pathname.startsWith("/ui/_gallery");

  // Everything else needs a token. Rendering the shell first and letting each
  // screen 401 on its own would show five simultaneous failures and none of
  // them would say what to do about it.
  if (!token && !bare) {
    return (
      <div className="min-h-screen bg-background font-sans text-foreground">
        <SignIn />
      </div>
    );
  }

  if (bare) {
    return (
      <div className="min-h-screen bg-background font-sans text-foreground">
        <Outlet />
      </div>
    );
  }

  return <Shell pathname={pathname} />;
}

type Layout = "phone" | "rail" | "full";

/**
 * The framed sheet. The navigation and the top bar share one ground, with no
 * rule between them, and the work sits on a sheet inset from the window's
 * right and bottom edges. A wide screen has the grouped sidebar, a tablet an
 * icon rail; a phone loses the frame, and its navigation is a bar at the
 * bottom with the rest under More.
 */
function Shell({ pathname }: { pathname: string }) {
  const tablet = useMediaQuery("(min-width: 48rem)");
  const wide = useMediaQuery("(min-width: 80rem)");
  const layout: Layout = wide ? "full" : tablet ? "rail" : "phone";
  const { teamId } = useCurrentTeam();
  const groups = navGroups(useOffersDeletedTeams());
  const waiting = useWaiting(teamId);

  if (layout === "phone") {
    return (
      <div className="flex min-h-dvh flex-col bg-sheet font-sans text-foreground">
        <header className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-2 bg-frame px-4">
          <Mark className="size-5 shrink-0" />
          <TeamSwitcher />
        </header>
        <main className="min-w-0 flex-1 px-4 pt-5 pb-24">
          <Outlet />
        </main>
        <QuickBar groups={groups} pathname={pathname} waiting={waiting} />
      </div>
    );
  }

  return (
    <TooltipProvider delayDuration={300}>
      <div
        className={cn(
          "grid min-h-dvh grid-rows-[3rem_minmax(0,1fr)] bg-frame font-sans text-foreground",
          layout === "full"
            ? "grid-cols-[14.25rem_minmax(0,1fr)]"
            : "grid-cols-[3.5rem_minmax(0,1fr)]",
        )}
      >
        <aside className="row-span-2 flex min-h-0 flex-col">
          <div
            className={cn(
              "flex h-12 shrink-0 items-center",
              layout === "full" ? "px-4" : "justify-center",
            )}
          >
            {layout === "full" ? (
              <span className="flex items-center gap-2">
                <Mark className="size-5" />
                <span className="text-sm font-semibold tracking-tight">openarity</span>
              </span>
            ) : (
              <Mark className="size-5" />
            )}
          </div>
          <div className="flex-1 overflow-y-auto px-2 pt-1 pb-3">
            <Nav groups={groups} pathname={pathname} waiting={waiting} rail={layout === "rail"} />
          </div>
          <div className="px-2 pb-3">
            <Account rail={layout === "rail"} />
          </div>
        </aside>
        <header className="col-start-2 flex h-12 min-w-0 items-center gap-2 pr-2">
          <TeamSwitcher />
        </header>
        <main className="col-start-2 mr-2 mb-2 min-w-0 rounded-2xl border border-sheet-edge bg-sheet">
          <div className="px-6 py-6">
            <Outlet />
          </div>
        </main>
      </div>
    </TooltipProvider>
  );
}

function Count({ n }: { n: string }) {
  return (
    <span className="rounded-full bg-primary px-1.5 text-xs font-medium text-primary-foreground tabular-nums">
      <span className="sr-only">, </span>
      {n}
      <span className="sr-only"> waiting</span>
    </span>
  );
}

function NavLink({
  item,
  current,
  waiting,
  rail,
  onPick,
}: {
  item: NavItem;
  current: boolean;
  waiting: string | null;
  rail?: boolean;
  onPick?: () => void;
}) {
  const Icon = item.icon;
  const count = item === APPROVALS ? waiting : null;
  const link = (
    <Link
      to={item.to}
      onClick={onPick}
      aria-current={current ? "page" : undefined}
      aria-label={rail ? item.label : undefined}
      className={cn(
        "relative flex min-h-9 items-center gap-2.5 rounded-md text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
        rail ? "justify-center px-0" : "px-2.5",
        current
          ? "bg-sheet font-medium text-foreground shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
          : "text-muted-foreground hover:bg-sheet/60 hover:text-foreground",
      )}
    >
      <Icon className={cn("size-4 shrink-0", current && "text-primary")} aria-hidden />
      {rail ? (
        count ? (
          <span aria-hidden className="absolute top-1.5 right-2 size-2 rounded-full bg-primary" />
        ) : null
      ) : (
        <>
          <span className="flex-1">{item.label}</span>
          {count ? <Count n={count} /> : null}
        </>
      )}
    </Link>
  );
  if (!rail) return link;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{link}</TooltipTrigger>
      <TooltipContent side="right">
        {item.label}
        {count ? ` · ${count} waiting` : ""}
      </TooltipContent>
    </Tooltip>
  );
}

function Nav({
  groups,
  pathname,
  waiting,
  rail = false,
  onPick,
}: {
  groups: readonly NavGroup[];
  pathname: string;
  waiting: string | null;
  rail?: boolean;
  onPick?: () => void;
}) {
  const active = activeOf(
    groups.flatMap((g) => g.items),
    pathname,
  );
  return (
    <nav aria-label="Main" className="flex flex-col gap-4">
      {groups.map((g) => (
        // biome-ignore lint/a11y/useSemanticElements: a fieldset is for form controls, and these are links
        <div key={g.label} role="group" aria-label={g.label} className="flex flex-col gap-0.5">
          {rail ? (
            <span aria-hidden className="mx-auto mb-1 h-px w-5 bg-rule first:hidden" />
          ) : (
            <span aria-hidden className="px-2.5 pb-1 text-xs text-muted-foreground">
              {g.label}
            </span>
          )}
          {g.items.map((item) => (
            <NavLink
              key={item.to}
              item={item}
              current={item.to === active}
              waiting={waiting}
              rail={rail}
              onPick={onPick}
            />
          ))}
        </div>
      ))}
    </nav>
  );
}

/** The team the dashboard is about, and the reader's role in it. */
function TeamSwitcher() {
  const { teamId, teams, choose } = useCurrentTeam();
  const current = teams.find((t) => t.id === teamId);
  const role = current?.role ? (
    <span className="shrink-0 text-xs text-muted-foreground">{current.role}</span>
  ) : null;
  if (teams.length <= 1) {
    return current ? (
      <span className="flex min-w-0 items-center gap-2 px-2 text-sm font-medium">
        <span className="truncate">{current.name}</span>
        {role}
      </span>
    ) : null;
  }
  return (
    <Select value={teamId ?? undefined} onValueChange={choose}>
      <SelectTrigger
        className="h-8 w-auto max-w-64 min-w-0 gap-2 border-transparent bg-transparent text-sm font-medium shadow-none hover:bg-sheet/60 dark:bg-transparent dark:hover:bg-sheet/60"
        aria-label="Current team"
      >
        <SelectValue placeholder="Choose a team" />
        {role}
      </SelectTrigger>
      <SelectContent>
        {teams.map((t) => (
          <SelectItem key={t.id} value={t.id}>
            {t.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function usePrincipal(): string {
  const whoami = useQuery({ queryKey: ["whoami"], queryFn: ({ signal }) => fetchWhoami(signal) });
  const p = whoami.data as { subject?: string; email?: string } | undefined;
  return p?.email ?? p?.subject ?? "…";
}

/** Who is signed in, the theme, and signing out: one menu at the foot. */
function Account({ rail }: { rail: boolean }) {
  const who = usePrincipal();
  const { theme, toggle } = useTheme();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={rail ? `Account: ${who}` : undefined}
          className={cn(
            "flex min-h-9 w-full items-center gap-2 rounded-md text-left text-sm text-muted-foreground hover:bg-sheet/60 hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
            rail ? "justify-center" : "px-2.5",
          )}
        >
          <span
            aria-hidden
            className="flex size-6 shrink-0 items-center justify-center rounded-full bg-card text-xs font-medium text-foreground shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
          >
            {who.slice(0, 1).toUpperCase()}
          </span>
          {rail ? null : (
            <>
              <span className="min-w-0 flex-1 truncate">{who}</span>
              <ChevronsUpDown aria-hidden className="size-3.5 shrink-0" />
            </>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side={rail ? "right" : "top"} align="start" className="w-56">
        <DropdownMenuLabel className="truncate font-normal text-muted-foreground">
          {who}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={toggle}>
          {theme === "dark" ? <Sun aria-hidden /> : <Moon aria-hidden />}
          {theme === "dark" ? "Light theme" : "Dark theme"}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => setToken(null)}>
          <LogOut aria-hidden />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * A phone's navigation: Home, the runs and Approvals in a bar at the bottom,
 * within a thumb's reach, and everything else under More.
 */
function QuickBar({
  groups,
  pathname,
  waiting,
}: {
  groups: readonly NavGroup[];
  pathname: string;
  waiting: string | null;
}) {
  const [open, setOpen] = useState(false);
  const quick = [HOME, SESSIONS, APPROVALS];
  const active = activeOf(
    groups.flatMap((g) => g.items),
    pathname,
  );
  const inMore = !quick.some((q) => q.to === active);
  const who = usePrincipal();
  const { theme, toggle } = useTheme();
  return (
    <nav
      aria-label="Quick"
      className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-4 bg-frame pb-[env(safe-area-inset-bottom)] shadow-[0_-1px_0_var(--color-sheet-edge)]"
    >
      {quick.map((item) => {
        const Icon = item.icon;
        const current = item.to === active;
        const count = item === APPROVALS ? waiting : null;
        return (
          <Link
            key={item.to}
            to={item.to}
            aria-current={current ? "page" : undefined}
            className={cn(
              "flex h-14 flex-col items-center justify-center gap-0.5 text-xs focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
              current ? "font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            <span className="relative">
              <Icon aria-hidden className={cn("size-5", current && "text-primary")} />
              {count ? (
                <span
                  aria-hidden
                  className="absolute -top-1 -right-1.5 size-2 rounded-full bg-primary"
                />
              ) : null}
            </span>
            {item.label}
            {count ? <span className="sr-only">, {count} waiting</span> : null}
          </Link>
        );
      })}
      <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
        <DialogPrimitive.Trigger asChild>
          <button
            type="button"
            aria-current={inMore ? "page" : undefined}
            className={cn(
              "flex h-14 flex-col items-center justify-center gap-0.5 text-xs focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
              inMore ? "font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            <Ellipsis aria-hidden className={cn("size-5", inMore && "text-primary")} />
            More
          </button>
        </DialogPrimitive.Trigger>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
          <DialogPrimitive.Content className="fixed inset-x-0 bottom-0 z-50 flex max-h-[85dvh] flex-col rounded-t-2xl bg-frame pb-[env(safe-area-inset-bottom)] outline-none data-[state=open]:animate-in data-[state=open]:slide-in-from-bottom">
            <div className="flex h-12 shrink-0 items-center justify-between px-4">
              <DialogPrimitive.Title className="text-sm font-medium">
                Everything
              </DialogPrimitive.Title>
              <DialogPrimitive.Close asChild>
                <Button variant="ghost" size="icon" aria-label="Close">
                  <X className="size-5" />
                </Button>
              </DialogPrimitive.Close>
            </div>
            <DialogPrimitive.Description className="sr-only">
              Every screen of the dashboard, and your account.
            </DialogPrimitive.Description>
            <div className="flex-1 space-y-4 overflow-y-auto px-2 pb-4">
              <Nav
                groups={groups}
                pathname={pathname}
                waiting={waiting}
                onPick={() => setOpen(false)}
              />
              <div className="space-y-0.5 border-t border-rule px-0.5 pt-3">
                <p className="truncate px-2.5 pb-1 text-xs text-muted-foreground">{who}</p>
                <button
                  type="button"
                  onClick={toggle}
                  className="flex min-h-11 w-full items-center gap-2.5 rounded-md px-2.5 text-sm text-muted-foreground hover:bg-sheet/60"
                >
                  {theme === "dark" ? (
                    <Sun aria-hidden className="size-4" />
                  ) : (
                    <Moon aria-hidden className="size-4" />
                  )}
                  {theme === "dark" ? "Light theme" : "Dark theme"}
                </button>
                <button
                  type="button"
                  onClick={() => setToken(null)}
                  className="flex min-h-11 w-full items-center gap-2.5 rounded-md px-2.5 text-sm text-muted-foreground hover:bg-sheet/60"
                >
                  <LogOut aria-hidden className="size-4" />
                  Sign out
                </button>
              </div>
            </div>
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </nav>
  );
}
