import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, useRouterState } from "@tanstack/react-router";
import {
  Inbox,
  LayoutGrid,
  MessagesSquare,
  Moon,
  Radio,
  RotateCcw,
  Sun,
  Users,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { getSetupState, listPendingSenders, loadDemoInstall, resetInstall } from "@/mocks/api";

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
  const chromeless = pathname.startsWith("/ui/setup");

  if (chromeless) {
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
  const { data: pending } = useQuery({
    queryKey: ["pending", "count"],
    queryFn: () => listPendingSenders({}),
  });
  const pendingCount = pending?.items.length ?? 0;

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
              {item.label === "Approvals" && pendingCount > 0 ? (
                <span className="rounded-sm bg-primary px-1.5 py-0.5 font-mono text-[10px] text-primary-foreground">
                  {pendingCount}
                  {pending?.next_cursor ? "+" : ""}
                </span>
              ) : null}
            </Link>
          );
        })}
      </nav>
      <div className="border-t border-sidebar-border p-3">
        <Link
          to="/ui/setup"
          className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          First-run setup
        </Link>
      </div>
    </aside>
  );
}

function TopBar() {
  const { theme, toggle } = useTheme();
  const qc = useQueryClient();
  const { data: setup } = useQuery({ queryKey: ["setup"], queryFn: getSetupState });

  const demo = useMutation({
    mutationFn: loadDemoInstall,
    onSuccess: () => {
      qc.invalidateQueries();
      toast.success("Demo install loaded");
    },
  });
  const reset = useMutation({
    mutationFn: resetInstall,
    onSuccess: () => {
      qc.invalidateQueries();
      toast.success("Install reset to a fresh, empty state");
    },
  });

  return (
    <header className="flex h-12 items-center justify-between gap-3 border-b border-border px-6">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <span className="font-semibold text-foreground md:hidden">Openarity</span>
        {setup?.signed_in_user ? (
          <span className="font-mono">{setup.signed_in_user.email}</span>
        ) : (
          <span>Not signed in</span>
        )}
      </div>
      <div className="flex items-center gap-1">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => demo.mutate()}
          disabled={demo.isPending}
          className="text-xs"
        >
          Load demo data
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => reset.mutate()}
          disabled={reset.isPending}
          className="text-xs"
        >
          <RotateCcw className="size-3.5" aria-hidden /> Reset
        </Button>
        <Button variant="ghost" size="icon" onClick={toggle} aria-label="Toggle color theme">
          {theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </Button>
      </div>
    </header>
  );
}
