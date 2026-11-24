import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";

import { getTeamSettings } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { TeamSettings } from "@/api/types";
import { ReadOnlyNote } from "@/components/registry-bits";
import { SettingsForm } from "@/components/settings/form";
import { ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { NO_MODEL } from "@/lib/ask";
import { useMediaQuery } from "@/lib/media";
import { SETTINGS_PAGES, type SettingsPage, type SettingsPageId } from "@/lib/settings-pages";
import type { Group } from "@/lib/team-settings";
import { cn } from "@/lib/utils";

export const WRITE = "team:settings";

/** The settings groups each page holds; Approvals keeps its own. */
export const PAGE_GROUPS: Record<SettingsPageId, readonly Group[]> = {
  general: ["model", "hub", "compaction"],
  tools: ["search", "sandbox", "browser"],
  approvals: [],
  improvement: ["improvement"],
};

export function useTeamSettings(teamId: string | null) {
  return useQuery({
    queryKey: ["settings", teamId],
    queryFn: () => getTeamSettings(teamId as string),
    enabled: Boolean(teamId),
    // A room template takes minutes to build; read it again until it is done.
    refetchInterval: (q) =>
      q.state.data?.sandbox?.template_build?.status === "building" ? 15_000 : false,
  });
}

/**
 * The pages, as a list beside the page from 1024px, a row of tabs from 768px,
 * and on a phone a list of its own, each with what it decides. General is
 * marked while the team has no model, since nothing runs until it has one.
 */
export function SettingsNav({
  current,
  noModel,
  variant,
}: {
  current: SettingsPageId | null;
  noModel: boolean;
  variant: "column" | "row" | "list";
}) {
  const warn = (p: SettingsPage) =>
    p.id === "general" && noModel ? (
      <>
        <TriangleAlert aria-hidden className="size-3.5 shrink-0 text-warning" />
        <span className="sr-only">, no model is set</span>
      </>
    ) : null;

  if (variant === "list") {
    return (
      <nav aria-label="Team settings">
        <ul className="divide-y divide-rule rounded-lg bg-card">
          {SETTINGS_PAGES.map((p) => (
            <li key={p.id}>
              <Link
                to={p.to}
                className="flex min-h-14 items-center gap-3 px-3 py-2.5 focus-visible:outline-2 focus-visible:outline-offset-[-2px]"
              >
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5 text-sm font-medium">
                    {p.label}
                    {warn(p)}
                  </span>
                  <span className="block text-xs text-muted-foreground">{p.about}</span>
                </span>
                <ChevronRight aria-hidden className="size-4 shrink-0 text-muted-foreground" />
              </Link>
            </li>
          ))}
        </ul>
      </nav>
    );
  }

  const row = variant === "row";
  return (
    <nav
      aria-label="Team settings"
      className={row ? "border-b border-rule" : "lg:sticky lg:top-0 lg:self-start"}
    >
      <ul className={row ? "-mb-px flex gap-4 overflow-x-auto" : "flex flex-col gap-0.5"}>
        {SETTINGS_PAGES.map((p) => {
          const on = p.id === current;
          return (
            <li key={p.id} className="shrink-0">
              <Link
                to={p.to}
                aria-current={on ? "page" : undefined}
                className={cn(
                  "flex items-center gap-1.5 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
                  row
                    ? cn(
                        "h-10 border-b-2 px-0.5",
                        on
                          ? "border-foreground font-medium text-foreground"
                          : "border-transparent text-muted-foreground hover:text-foreground",
                      )
                    : cn(
                        "min-h-9 rounded-md px-2.5",
                        on
                          ? "bg-card font-medium text-foreground"
                          : "text-muted-foreground hover:bg-card/60 hover:text-foreground",
                      ),
                )}
              >
                {p.label}
                {warn(p)}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

type Ready = { teamId: string; canWrite: boolean; settings: UseQueryResult<TeamSettings> };

/**
 * What every page of the team's settings shares: the title, the finding when
 * the team has no model, whether the reader may change anything, and the
 * list of pages. On a phone the list is a page of its own, and each page
 * leads back to it. `page` is null for that list.
 */
export function SettingsShell({
  page,
  children,
}: {
  page: SettingsPageId | null;
  /** The page's body, once the team is known; none for the list of pages. */
  children?: (ready: Ready) => ReactNode;
}) {
  const { teamId, teams } = useCurrentTeam();
  const teamName = teams.find((t) => t.id === teamId)?.name;
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const settings = useTeamSettings(teamId);
  const noModel = settings.data?.model === null;
  const tablet = useMediaQuery("(min-width: 48rem)");
  const desk = useMediaQuery("(min-width: 64rem)");
  const label = SETTINGS_PAGES.find((p) => p.id === page)?.label;

  const body = !children ? null : teamId ? (
    children({ teamId, canWrite, settings })
  ) : (
    <EmptyState title="No team" body="Join or create a team to see its settings." />
  );

  return (
    <div className="space-y-5">
      {!tablet && label ? (
        <nav aria-label="Breadcrumb">
          <ol className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
            <li>
              <Link
                to="/ui/settings"
                className="rounded underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
              >
                Team settings
              </Link>
            </li>
            <li aria-hidden>
              <ChevronRight className="size-3" />
            </li>
            <li aria-current="page" className="min-w-0 truncate text-foreground">
              {label}
            </li>
          </ol>
        </nav>
      ) : null}
      <PageHeader
        title={teamName ? `Team settings — ${teamName}` : "Team settings"}
        finding={noModel ? NO_MODEL : null}
        description="For this team only: how its built-in agents run, the tools they reach, which tool calls wait for a person, and whether agents propose improvements to themselves. Each team has its own."
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="settings" />
      ) : null}

      {page === null ? (
        <SettingsNav current={null} noModel={noModel} variant="list" />
      ) : desk ? (
        <div className="grid grid-cols-[10rem_minmax(0,1fr)] gap-8">
          <SettingsNav current={page} noModel={noModel} variant="column" />
          <div className="min-w-0">{body}</div>
        </div>
      ) : tablet ? (
        <div className="space-y-6">
          <SettingsNav current={page} noModel={noModel} variant="row" />
          {body}
        </div>
      ) : (
        body
      )}
    </div>
  );
}

/** A page of settings groups: loading, failure, or the form for its groups. */
export function SettingsBody({ ready, page }: { ready: Ready; page: SettingsPageId }) {
  const { teamId, canWrite, settings } = ready;
  if (settings.isPending) {
    return (
      <div className="space-y-2" role="status" aria-label="Loading settings">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }
  if (settings.isError) {
    return (
      <ErrorState
        what="the settings"
        error={settings.error}
        onRetry={() => settings.refetch()}
        retrying={settings.isRefetching}
      />
    );
  }
  // Keyed on the team: another team's settings start a fresh form. A save
  // does not remount it, so another group's unsaved edits stay.
  return (
    <SettingsForm
      key={teamId}
      teamId={teamId}
      stored={settings.data}
      canWrite={canWrite}
      groups={PAGE_GROUPS[page]}
    />
  );
}
