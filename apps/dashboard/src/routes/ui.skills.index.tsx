import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ChangeEvent, useRef, useState } from "react";
import { toast } from "sonner";

import {
  deleteSkill,
  getSkill,
  importSkill,
  listSkills,
  replaceSkill,
  type SkillUpload,
  syncSkill,
  uploadSkill,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { SkillSummary } from "@/api/types";
import { ReadOnlyNote, Tag } from "@/components/registry-bits";
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
import { fileSize, relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/skills/")({
  component: Skills,
});

const WRITE = "skill:write";

/**
 * What a folder picker hands back, turned into what the brain reads: each
 * file under the path it sits at. A .git directory is left out — a cloned
 * skill carries hundreds of objects, and the brain would refuse the upload
 * for having more files than a skill may.
 */
export function folderUpload(files: FileList | File[]): SkillUpload {
  const picked = Array.from(files)
    .map((file) => ({ path: file.webkitRelativePath || file.name, file }))
    .filter(({ path }) => !path.split("/").includes(".git"));
  return { files: picked };
}

/**
 * A folder or a zip, chosen through the browser's own pickers: what an
 * upload and a new version of a skill both start from.
 */
function SkillPicker({
  disabled,
  onPick,
  folderButton,
  zipButton,
  folderLabel,
  zipLabel,
}: {
  disabled: boolean;
  onPick: (body: SkillUpload) => void;
  folderButton: string;
  zipButton: string;
  folderLabel: string;
  zipLabel: string;
}) {
  const folderInput = useRef<HTMLInputElement>(null);
  const zipInput = useRef<HTMLInputElement>(null);

  const onFolder = (e: ChangeEvent<HTMLInputElement>) => {
    if (e.target.files?.length) onPick(folderUpload(e.target.files));
    e.target.value = "";
  };
  const onZip = (e: ChangeEvent<HTMLInputElement>) => {
    const zip = e.target.files?.[0];
    if (zip) onPick({ zip });
    e.target.value = "";
  };

  return (
    <>
      <Button
        size="sm"
        variant="outline"
        disabled={disabled}
        onClick={() => folderInput.current?.click()}
      >
        {folderButton}
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={disabled}
        onClick={() => zipInput.current?.click()}
      >
        {zipButton}
      </Button>
      {/* webkitdirectory is not in React's types, and every browser supports it. */}
      <input
        ref={folderInput}
        type="file"
        multiple
        className="hidden"
        aria-label={folderLabel}
        onChange={onFolder}
        {...({ webkitdirectory: "", directory: "" } as Record<string, string>)}
      />
      <input
        ref={zipInput}
        type="file"
        accept=".zip,application/zip"
        className="hidden"
        aria-label={zipLabel}
        onChange={onZip}
      />
    </>
  );
}

function Skills() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [open, setOpen] = useState<string | null>(null);
  const [source, setSource] = useState("");

  const skills = useInfiniteQuery({
    queryKey: ["skills", teamId, "paged"],
    queryFn: ({ pageParam }) => listSkills(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => qc.invalidateQueries({ queryKey: ["skills", teamId] });

  const upload = useMutation({
    mutationFn: (body: SkillUpload) => uploadSkill(teamId as string, body),
    onSuccess: (skill) => {
      changed();
      setOpen(skill.id);
      toast.success(`Uploaded ${skill.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const imported = useMutation({
    mutationFn: () => importSkill(teamId as string, source.trim()),
    onSuccess: (skill) => {
      setSource("");
      changed();
      setOpen(skill.id);
      toast.success(`Imported ${skill.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const rows = skills.data?.pages.flatMap((p) => p.items) ?? [];
  const busy = upload.isPending || imported.isPending;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        description="Instructions and files an agent can load when a task needs them. The description is what the model reads when deciding."
        actions={
          canWrite ? (
            <SkillPicker
              disabled={busy}
              onPick={(body) => upload.mutate(body)}
              folderButton="Upload folder"
              zipButton="Upload zip"
              folderLabel="Skill folder"
              zipLabel="Skill zip"
            />
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="skills" />
      ) : null}

      {canWrite ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (source.trim()) imported.mutate();
          }}
        >
          <Input
            value={source}
            onChange={(e) => setSource(e.target.value)}
            placeholder="github:owner/repo/path@ref, or https://… .zip"
            aria-label="Import from"
            className="h-8 max-w-md flex-1 text-xs"
          />
          <Button type="submit" size="sm" disabled={!source.trim() || busy}>
            {imported.isPending ? "Importing…" : "Import"}
          </Button>
        </form>
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its skills." />
      ) : skills.isPending ? (
        <p className="text-sm text-muted-foreground">Loading skills…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No skills yet"
          body="A skill is a folder with SKILL.md at the top. Upload one, or import it from GitHub."
          hint="oa skills upload <team> ./my-skill"
        />
      ) : (
        <>
          <ul className="divide-y divide-border overflow-hidden rounded-md border border-border bg-card">
            {rows.map((s) => (
              <SkillRow
                key={s.id}
                skill={s}
                teamId={teamId}
                canWrite={canWrite}
                open={open === s.id}
                onToggle={() => setOpen(open === s.id ? null : s.id)}
                onChanged={changed}
              />
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(skills.hasNextPage)}
            loading={skills.isFetchingNextPage}
            onClick={() => skills.fetchNextPage()}
            loadedLabel={`${rows.length} skill${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </div>
  );
}

function SkillRow({
  skill,
  teamId,
  canWrite,
  open,
  onToggle,
  onChanged,
}: {
  skill: SkillSummary;
  teamId: string;
  canWrite: boolean;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const sync = useMutation({
    mutationFn: () => syncSkill(teamId, skill.id),
    onSuccess: () => {
      onChanged();
      toast.success(`Synced ${skill.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });
  const [replacing, setReplacing] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteSkill(teamId, skill.id),
    onSuccess: () => {
      onChanged();
      toast.success(`Deleted ${skill.name}`);
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
            <span className="text-sm font-medium">{skill.name}</span>
            <Tag>{skill.source}</Tag>
          </div>
          <p className="truncate text-xs text-muted-foreground">{skill.description}</p>
        </button>
        <span className="hidden text-xs text-muted-foreground sm:inline">
          {relativeTime(skill.updated_at)}
        </span>
        {canWrite ? (
          <>
            {skill.source !== "upload" ? (
              <Button
                variant="ghost"
                size="sm"
                className="text-xs"
                disabled={sync.isPending}
                onClick={() => sync.mutate()}
              >
                Sync
              </Button>
            ) : null}
            <Button
              variant="ghost"
              size="sm"
              className="text-xs"
              onClick={() => setReplacing(true)}
            >
              Upload new version
            </Button>
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
      {open ? <SkillDetail teamId={teamId} skillId={skill.id} /> : null}
      {canWrite ? (
        <ReplaceDialog
          teamId={teamId}
          skill={skill}
          open={replacing}
          onClose={() => setReplacing(false)}
          onChanged={onChanged}
        />
      ) : null}
    </li>
  );
}

/**
 * A new version of a skill, uploaded whole. The brain keeps the skill's id and
 * its grants, names it from the new SKILL.md, and refuses — with a sentence
 * shown here as it is — a version that does not fit.
 */
function ReplaceDialog({
  teamId,
  skill,
  open,
  onClose,
  onChanged,
}: {
  teamId: string;
  skill: SkillSummary;
  open: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const replace = useMutation({
    mutationFn: (body: SkillUpload) => replaceSkill(teamId, skill.id, body),
    onSuccess: (next) => {
      // The list and this skill's detail share the ["skills", teamId] prefix.
      onChanged();
      close();
      toast.success(`Uploaded a new version of ${next.name}`);
    },
  });

  const close = () => {
    replace.reset();
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : close())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Upload a new version of {skill.name}</DialogTitle>
          <DialogDescription>
            Pick the whole skill again: a folder with SKILL.md at the top, or a zip of one. It
            replaces every file, and its name comes from the new SKILL.md. Agents granted this skill
            keep it and get the new version on their next run.
          </DialogDescription>
        </DialogHeader>
        {replace.isError ? (
          <p role="alert" className="text-sm text-destructive">
            {replace.error.message}
          </p>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={close}>
            Cancel
          </Button>
          <SkillPicker
            disabled={replace.isPending}
            onPick={(body) => replace.mutate(body)}
            folderButton="Choose folder"
            zipButton="Choose zip"
            folderLabel="New version folder"
            zipLabel="New version zip"
          />
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SkillDetail({ teamId, skillId }: { teamId: string; skillId: string }) {
  const detail = useQuery({
    queryKey: ["skills", teamId, skillId],
    queryFn: () => getSkill(teamId, skillId),
  });

  if (detail.isPending) return <p className="px-3 pb-3 text-xs text-muted-foreground">Loading…</p>;
  if (detail.isError)
    return <p className="px-3 pb-3 text-xs text-destructive">{detail.error.message}</p>;

  const s = detail.data;
  return (
    <div className="grid gap-4 border-t border-border bg-surface px-3 py-3 md:grid-cols-[minmax(0,1fr)_16rem]">
      <div className="min-w-0 space-y-2">
        <p className="text-sm">{s.description}</p>
        {s.source_ref ? (
          <p className="text-xs text-muted-foreground">
            From <Mono>{s.source_ref}</Mono>
            {s.source_sha ? (
              <>
                {" "}
                at <Mono>{s.source_sha.slice(0, 12)}</Mono>
              </>
            ) : null}
          </p>
        ) : null}
        {/* The body is the team's markdown, shown as text: rendering it as
            HTML would run whatever an imported skill's author put there. */}
        <pre className="max-h-80 overflow-auto rounded-md border border-border bg-card p-3 text-xs whitespace-pre-wrap">
          {s.body}
        </pre>
      </div>
      <div>
        <h3 className="mb-1 text-xs font-medium">Files</h3>
        <ul className="space-y-0.5 text-xs">
          <li className="flex justify-between gap-2">
            <Mono className="text-foreground">SKILL.md</Mono>
          </li>
          {s.files.map((f) => (
            <li key={f.path} className="flex justify-between gap-2">
              <Mono className="truncate text-foreground">{f.path}</Mono>
              <span className="shrink-0 text-muted-foreground">{fileSize(f.size)}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
