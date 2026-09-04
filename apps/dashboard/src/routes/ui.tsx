import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, useRouterState } from "@tanstack/react-router";
import { Inbox, LayoutGrid, MessagesSquare, Moon, Radio, Sun, Users } from "lucide-react";
import { fetchWhoami } from "@/api";
import { useCurrentTeam, useToken } from "@/api/session";
import { setToken } from "@/api/token";
import { SignIn } from "@/components/sign-in";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
] as const;

function UiLayout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const token = useToken();

  // The callback is the one route that must render without a token, because
  // obtaining one is what it does. Gating it sends the browser back to the
  // sign-in screen holding an authorization code nothing will ever redeem —
  // and the loop looks exactly like a provider that refused the login.
  const completingLogin = pathname.startsWith("/ui/callback");

  // Everything else needs a token. Rendering the shell first and letting each
  // screen 401 on its own would show five simultaneous failures and none of
  // them would say what to do about it.
  if (!token && !completingLogin) {
    return (
      <div className="min-h-screen bg-background font-sans text-foreground">
        <SignIn />
      </div>
    );
  }

  if (completingLogin) {
    return (
      <div className="min-h-screen bg-background font-sans text-foreground">
        <Outlet />
      </div>
    );
  }

  return (
    <div className="flex min-h-screen bg-background font-sans text-foreground">
      <Sidebar pathname={pathname} />
      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar />
        <main className="mx-auto w-full max-w-6xl flex-1 px-6 py-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function Sidebar({ pathname }: { pathname: string }) {
  return (
    <aside className="hidden w-56 shrink-0 flex-col border-r border-border bg-sidebar md:flex">
      <div className="flex h-12 items-center gap-2 border-b border-sidebar-border px-4">
        <span className="size-2 rounded-[2px] bg-primary" aria-hidden />
        <span className="text-sm font-semibold tracking-tight">Openarity</span>
      </div>
      <nav className="flex flex-1 flex-col gap-0.5 p-2" aria-label="Main">
        {NAV.map((item) => {
          const active = item.exact ? pathname === item.to : pathname.startsWith(item.to);
          const Icon = item.icon;
          return (
            <Link
              key={item.to}
              to={item.to}
              className={cn(
                "flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors",
                active
                  ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                  : "text-muted-foreground hover:bg-secondary hover:text-foreground",
              )}
            >
              <Icon className="size-4" aria-hidden />
              <span className="flex-1">{item.label}</span>
            </Link>
          );
        })}
      </nav>
      {/* No pending count in the sidebar: it would cost one request per
          channel on every page, because the brain counts pending senders per
          channel and not per install. */}
    </aside>
  );
}

function TopBar() {
  const { theme, toggle } = useTheme();
  const { teamId, teams, choose } = useCurrentTeam();
  const whoami = useQuery({ queryKey: ["whoami"], queryFn: ({ signal }) => fetchWhoami(signal) });

  const principal = whoami.data as { subject?: string; email?: string } | undefined;

  return (
    <header className="flex h-12 items-center justify-between gap-3 border-b border-border px-6">
      <div className="flex items-center gap-3 text-xs text-muted-foreground">
        <span className="font-semibold text-foreground md:hidden">Openarity</span>
        <span className="font-mono">{principal?.email ?? principal?.subject ?? "…"}</span>
        {teams.length > 1 ? (
          <Select value={teamId ?? undefined} onValueChange={choose}>
            <SelectTrigger className="h-7 w-48 text-xs" aria-label="Current team">
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
        ) : null}
      </div>
      <div className="flex items-center gap-1">
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
