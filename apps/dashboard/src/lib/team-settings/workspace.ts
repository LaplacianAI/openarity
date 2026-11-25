import type { SandboxProvider, TeamSettings, TeamSettingsRequest } from "@/api/types";
import { parseWhole } from "@/lib/compaction";

/** The draft's workspace fields, in the order the page shows them. */
export const WORKSPACE_FIELDS = [
  "sizeCustom",
  "sizeMemory",
  "sizeCPUs",
  "sizeDisk",
  "idleOwn",
  "idleMinutes",
  "keepHours",
] as const satisfies readonly (keyof WorkspaceDraft)[];
export const WORKSPACE_FIELD_SET: ReadonlySet<string> = new Set<string>(WORKSPACE_FIELDS);

/** What the page starts a size from when the team has none of its own. */
const SIZE_START = { memory_mb: 2048, cpus: 2, disk_mb: 10240 };
const IDLE_START = 30;
/** What a brain that predates stopping keeps a stopped workspace for. */
const KEEP_HOURS = 24;

export function workspaceOf(s: TeamSettings["sandbox"] | undefined) {
  const size = s?.workspace_size ?? null;
  const shown = size ?? SIZE_START;
  return {
    sizeCustom: size !== null,
    sizeMemory: gbOf(shown.memory_mb),
    sizeCPUs: String(shown.cpus),
    sizeDisk: gbOf(shown.disk_mb),
    idleOwn: s?.idle_minutes != null,
    idleMinutes: String(s?.idle_minutes ?? IDLE_START),
    keepHours: String(s?.keep_hours ?? KEEP_HOURS),
  };
}

/** MB as the GB the page shows: 1536 is 1.5. */
function gbOf(mb: number): string {
  return String(Number((mb / 1024).toFixed(2)));
}

/** GB as typed, in MB; null when it is not a number. */
function mbOf(raw: string): number | null {
  const t = raw.trim();
  return /^\d+(\.\d+)?$/.test(t) || /^\.\d+$/.test(t) ? Math.round(Number(t) * 1024) : null;
}

/** A workspace's times and size by what they mean, a typo kept as typed so it reads as a change. */
function workspaceKey(d: WorkspaceDraft) {
  return {
    keep: parseWhole(d.keepHours) ?? d.keepHours.trim(),
    idle: d.idleOwn ? (parseWhole(d.idleMinutes) ?? d.idleMinutes.trim()) : null,
    size: d.sizeCustom
      ? [
          mbOf(d.sizeMemory) ?? d.sizeMemory.trim(),
          parseWhole(d.sizeCPUs) ?? d.sizeCPUs.trim(),
          mbOf(d.sizeDisk) ?? d.sizeDisk.trim(),
        ]
      : null,
  };
}

/** Whether saving would give the team's workspaces another size. */
export function sizeChanged(d: WorkspaceDraft, stored: TeamSettings): boolean {
  return (
    JSON.stringify(workspaceKey(d).size) !==
    JSON.stringify(workspaceKey(workspaceOf(stored.sandbox)).size)
  );
}

/** Whether saving would move the team's workspaces to another sandbox. */
export function providerChanged(d: { sandbox: SandboxProvider }, stored: TeamSettings): boolean {
  const was = stored.sandbox?.provider ?? "none";
  return was !== "none" && d.sandbox !== was;
}

/** The stored keep hours when the draft lowers them, else null. */
export function keepLowered(d: WorkspaceDraft, stored: TeamSettings): number | null {
  const was = stored.sandbox?.keep_hours ?? KEEP_HOURS;
  const now = parseWhole(d.keepHours);
  return now !== null && now >= 1 && now < was ? was : null;
}

export function sameWorkspace(a: WorkspaceDraft, b: WorkspaceDraft): boolean {
  return JSON.stringify(workspaceKey(a)) === JSON.stringify(workspaceKey(b));
}

/** The workspace fields of the sandbox a Save sends. */
export type WorkspaceRequest = Pick<
  NonNullable<TeamSettingsRequest["sandbox"]>,
  "keep_hours" | "idle_minutes" | "workspace_size"
>;

/**
 * A workspace's times and size, each sent only when it changed: left out, the
 * brain keeps what it holds, so a Save made in a tab opened earlier never
 * writes over them.
 */
export function workspaceRequest(
  d: WorkspaceDraft,
  stored: TeamSettings,
): WorkspaceRequest | string {
  const out: WorkspaceRequest = {};
  const was = workspaceKey(workspaceOf(stored.sandbox));
  const now = workspaceKey(d);

  if (JSON.stringify(now.size) !== JSON.stringify(was.size)) {
    if (!d.sizeCustom) {
      out.workspace_size = null;
    } else {
      const memory = mbOf(d.sizeMemory);
      const cpus = parseWhole(d.sizeCPUs);
      const disk = mbOf(d.sizeDisk);
      if (memory === null || memory < 256 || memory > 65536) {
        return "Memory is between 0.25 and 64 GB.";
      }
      if (cpus === null || cpus < 1 || cpus > 16) return "CPUs is a whole number from 1 to 16.";
      if (disk === null || disk < 256 || disk > 262144) return "Disk is between 0.25 and 256 GB.";
      out.workspace_size = { memory_mb: memory, cpus, disk_mb: disk };
    }
  }
  if (now.idle !== was.idle) {
    if (!d.idleOwn) {
      out.idle_minutes = null;
    } else {
      const minutes = parseWhole(d.idleMinutes);
      if (minutes === null || minutes < 1 || minutes > 1440) {
        return "Stop when idle is a whole number of minutes, from 1 to 1,440.";
      }
      out.idle_minutes = minutes;
    }
  }
  if (now.keep !== was.keep) {
    const hours = parseWhole(d.keepHours);
    if (hours === null || hours < 1 || hours > 720) {
      return "Keep stopped is a whole number of hours, from 1 to 720.";
    }
    out.keep_hours = hours;
  }
  return out;
}

/** A workspace's size and times as the page holds them, typed. */
export type WorkspaceDraft = ReturnType<typeof workspaceOf>;
