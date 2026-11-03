import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, useRouterState } from "@tanstack/react-router";
import {
  ArchiveRestore,
  BookOpen,
  Bot,
  Inbox,
  LayoutGrid,
  Menu,
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
import { fetchWhoami, listDeletedTeams } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam, useToken } from "@/api/session";
import { setToken } from "@/api/token";
import { Mark } from "@/components/mark";
import { SignIn } from "@/components/sign-in";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { isActive } from "@/lib/nav";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/ui")({
  component: UiLayout,
});

const NAV = [
  { to: "/ui", label: "Overview", icon: LayoutGrid, exact: true },
  { to: "/ui/teams", label: "Teams", icon: Users, exact: false },
  { to: "/ui/channels", label: "Channels", icon: Radio, exact: false },
  { to: "/ui/approvals", label: "Approvals", icon: Inbox, exact: false },
  { to: "/ui/sessions", label: "Sessions", icon: MessagesSquare, exact: false },
  { to: "/ui/agents", label: "Agents", icon: Bot, exact: false },
  { to: "/ui/servers", label: "MCP servers", icon: Server, exact: false },
  { to: "/ui/skills", label: "Skills", icon: BookOpen, exact: false },
  { to: "/ui/graph", label: "Graph", icon: Network, exact: false },
  { to: "/ui/settings", label: "Team settings", icon: Settings, exact: false },
] as const;

const DELETED_TEAMS = {
  to: "/ui/teams/deleted",
  label: "Deleted teams",
  icon: ArchiveRestore,
  exact: false,
} as const;

type NavItem = (typeof NAV)[number] | typeof DELETED_TEAMS;

/** The sidebar's entries. Deleted teams sits under Teams, for super admins only. */
export function navFor(superAdmin: boolean): readonly NavItem[] {
  if (!superAdmin) return NAV;
  const at = NAV.findIndex((i) => i.to === "/ui/teams") + 1;
  return [...NAV.slice(0, at), DELETED_TEAMS, ...NAV.slice(at)];
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

  // The framed sheet: the sidebar and the top bar share one ground, with no
  // rule between them, and the work sits on a sheet inset from the window's
  // right and bottom edges. Below md the frame goes and the sheet is the
  // screen, with the navigation behind a menu.
  return (
    <div className="min-h-dvh bg-frame font-sans text-foreground md:grid md:grid-cols-[14.25rem_minmax(0,1fr)] md:grid-rows-[3rem_minmax(0,1fr)]">
      <Sidebar pathname={pathname} />
      <TopBar pathname={pathname} />
      <main className="min-h-[calc(100dvh-3rem)] min-w-0 bg-sheet md:col-start-2 md:min-h-0 md:mr-2 md:mb-2 md:rounded-2xl md:border md:border-sheet-edge">
        <div className="px-4 py-5 md:px-6 md:py-6">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

function Brand() {
  return (
    <span className="flex items-center gap-2">
      <Mark className="size-5" />
      <span className="text-sm font-semibold tracking-tight">openarity</span>
    </span>
  );
}

function NavList({ pathname, onPick }: { pathname: string; onPick?: () => void }) {
  const items = navFor(useOffersDeletedTeams());
  const active = activeOf(items, pathname);
  return (
    <nav className="flex flex-col gap-0.5" aria-label="Main">
      {items.map((item) => {
        const Icon = item.icon;
        const current = item.to === active;
        return (
          <Link
            key={item.to}
            to={item.to}
            onClick={onPick}
            aria-current={current ? "page" : undefined}
            className={cn(
              "flex min-h-9 items-center gap-2.5 rounded-md px-2.5 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
              current
                ? "bg-sheet font-medium text-foreground shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
                : "text-muted-foreground hover:bg-sheet/60 hover:text-foreground",
            )}
          >
            <Icon className={cn("size-4", current && "text-primary")} aria-hidden />
            <span className="flex-1">{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}

function Sidebar({ pathname }: { pathname: string }) {
  return (
    <aside className="hidden flex-col md:row-span-2 md:flex">
      <div className="flex h-12 shrink-0 items-center px-4">
        <Brand />
      </div>
      <div className="flex-1 px-2 pt-1 pb-4">
        <NavList pathname={pathname} />
      </div>
      {/* No pending count in the sidebar: it would cost one request per
          channel on every page, because the brain counts pending senders per
          channel and not per install. */}
    </aside>
  );
}

/** The navigation on a phone: a sheet from the left, mounted only while open. */
function MenuSheet({ pathname }: { pathname: string }) {
  const [open, setOpen] = useState(false);
  return (
    <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
      <DialogPrimitive.Trigger asChild>
        <Button variant="ghost" size="icon" className="-ml-2 md:hidden" aria-label="Open the menu">
          <Menu className="size-5" />
        </Button>
      </DialogPrimitive.Trigger>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <DialogPrimitive.Content className="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col bg-frame shadow-xl outline-none data-[state=open]:animate-in data-[state=open]:slide-in-from-left">
          <div className="flex h-12 shrink-0 items-center justify-between px-4">
            <DialogPrimitive.Title asChild>
              <span>
                <Brand />
              </span>
            </DialogPrimitive.Title>
            <DialogPrimitive.Close asChild>
              <Button variant="ghost" size="icon" aria-label="Close the menu">
                <X className="size-5" />
              </Button>
            </DialogPrimitive.Close>
          </div>
          <DialogPrimitive.Description className="sr-only">
            Every screen of the dashboard.
          </DialogPrimitive.Description>
          <div className="flex-1 overflow-y-auto px-2 pb-4">
            <NavList pathname={pathname} onPick={() => setOpen(false)} />
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

function TopBar({ pathname }: { pathname: string }) {
  const { theme, toggle } = useTheme();
  const { teamId, teams, choose } = useCurrentTeam();
  const whoami = useQuery({ queryKey: ["whoami"], queryFn: ({ signal }) => fetchWhoami(signal) });

  const principal = whoami.data as { subject?: string; email?: string } | undefined;

  return (
    <header className="sticky top-0 z-40 flex h-12 min-w-0 items-center gap-2 bg-frame px-4 md:static md:col-start-2 md:pr-2 md:pl-0">
      <MenuSheet pathname={pathname} />
      <span className="md:hidden">
        <Mark className="size-5" />
      </span>
      {teams.length > 1 ? (
        <Select value={teamId ?? undefined} onValueChange={choose}>
          <SelectTrigger
            className="h-8 w-auto max-w-48 min-w-0 flex-1 border-transparent bg-transparent text-sm font-medium shadow-none hover:bg-sheet/60 sm:flex-none dark:bg-transparent dark:hover:bg-sheet/60"
            aria-label="Current team"
          >
            <SelectValue placeholder="Choose a team" />
          </SelectTrigger>
          <SelectContent>
            {teams.map((t) => (
              <SelectItem key={t.id} value={t.id}>
                {t.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <span className="min-w-0 flex-1" />
      )}
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <span className="hidden max-w-56 truncate text-xs text-muted-foreground lg:inline">
          {principal?.email ?? principal?.subject ?? "…"}
        </span>
        <Button variant="ghost" size="sm" className="text-xs" onClick={() => setToken(null)}>
          Sign out
        </Button>
        <Button variant="ghost" size="icon" onClick={toggle} aria-label="Toggle color theme">
          {theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </Button>
      </div>
    </header>
  );
}
