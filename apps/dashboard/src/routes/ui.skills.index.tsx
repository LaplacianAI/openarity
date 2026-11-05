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
import type { ListedSkill } from "@/api/types";
import { Time } from "@/components/facts";
import { Field, ReadOnlyNote } from "@/components/registry-bits";
import { ErrorState } from "@/components/states";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { fileSize } from "@/lib/format";
import { count } from "@/lib/runs";
import { folderUpload, heldBy, sourceWords } from "@/lib/skills";

export const Route = createFileRoute("/ui/skills/")({
  component: Skills,
});

const WRITE = "skill:write";

function Skills() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [shown, setShown] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const skills = useInfiniteQuery({
    queryKey: ["skills", teamId, "paged"],
    queryFn: ({ pageParam }) => listSkills(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => qc.invalidateQueries({ queryKey: ["skills", teamId] });
  const rows = skills.data?.pages.flatMap((p) => p.items) ?? [];
  const open = rows.find((s) => s.id === shown) ?? null;
  // Only a whole list can say how many no agent holds: a page is not a total.
  const unheld =
    skills.data && !skills.hasNextPage ? rows.filter((s) => s.agent_count === 0).length : 0;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        finding={unheld > 0 ? `${count(unheld, "skill")} no agent holds.` : null}
        description="Instructions and files an agent can load when a task needs them. The description is what the model reads when deciding."
        actions={
          canWrite ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              Add skill
            </Button>
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="skills" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its skills." />
      ) : skills.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading skills">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : skills.isError ? (
        <ErrorState
          what="the skills"
          error={skills.error}
          onRetry={() => skills.refetch()}
          retrying={skills.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No skills yet"
          body="A skill is a folder with SKILL.md at the top. Upload one, or import it from GitHub or a zip at a link."
        />
      ) : (
        <div>
          <Table aria-label="Skills in this team">
            <TableHeader>
              <tr>
                <TableHead>Skill</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Held by</TableHead>
                <TableHead>Updated</TableHead>
              </tr>
            </TableHeader>
            <TableBody>
              {rows.map((s) => (
                <TableRow key={s.id} onOpen={() => setShown(s.id)}>
                  <TableCell className="sm:w-full sm:max-w-0">
                    <div className="min-w-0 flex-1">
                      <button
                        type="button"
                        tabIndex={-1}
                        onClick={() => setShown(s.id)}
                        className="block max-w-full truncate text-left font-medium hover:underline"
                      >
                        {s.name}
                      </button>
                      <span className="block min-w-0 truncate text-xs text-muted-foreground">
                        {s.description}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell label="Source" className="whitespace-nowrap">
                    {sourceWords(s.source)}
                  </TableCell>
                  <TableCell
                    label="Held by"
                    className={
                      s.agent_count === 0
                        ? "whitespace-nowrap text-muted-foreground"
                        : "whitespace-nowrap"
                    }
                  >
                    {heldBy(s)}
                  </TableCell>
                  <TableCell label="Updated" className="whitespace-nowrap text-muted-foreground">
                    <Time iso={s.updated_at} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <LoadMore
            hasMore={Boolean(skills.hasNextPage)}
            loading={skills.isFetchingNextPage}
            onClick={() => skills.fetchNextPage()}
            loadedLabel={`${rows.length} skill${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}

      {teamId ? (
        <Sheet open={open !== null} onOpenChange={(o) => (o ? null : setShown(null))}>
          <SheetContent>
            {open ? (
              <SkillDetail
                teamId={teamId}
                skill={open}
                canWrite={canWrite}
                onChanged={changed}
                onDeleted={() => {
                  setShown(null);
                  void changed();
                }}
              />
            ) : null}
          </SheetContent>
        </Sheet>
      ) : null}

      {teamId && canWrite ? (
        <AddSkill
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onAdded={(id) => {
            void changed();
            setAdding(false);
            setShown(id);
          }}
        />
      ) : null}
    </div>
  );
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
      <Button variant="outline" disabled={disabled} onClick={() => folderInput.current?.click()}>
        {folderButton}
      </Button>
      <Button variant="outline" disabled={disabled} onClick={() => zipInput.current?.click()}>
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

/**
 * Adding a skill, in a sheet beside the list: imported from where it lives,
 * or uploaded from this machine. The brain's refusal is shown here, as it is.
 */
function AddSkill({
  teamId,
  open,
  onClose,
  onAdded,
}: {
  teamId: string;
  open: boolean;
  onClose: () => void;
  onAdded: (id: string) => void;
}) {
  const [source, setSource] = useState("");
  const imported = useMutation({
    mutationFn: () => importSkill(teamId, source.trim()),
    onSuccess: (skill) => {
      setSource("");
      toast.success(`Imported ${skill.name}`);
      onAdded(skill.id);
    },
  });
  const upload = useMutation({
    mutationFn: (body: SkillUpload) => uploadSkill(teamId, body),
    onSuccess: (skill) => {
      toast.success(`Uploaded ${skill.name}`);
      onAdded(skill.id);
    },
  });
  const busy = imported.isPending || upload.isPending;
  const failed = imported.error ?? upload.error;

  return (
    <Sheet
      open={open}
      onOpenChange={(o) => {
        if (o) return;
        imported.reset();
        upload.reset();
        onClose();
      }}
    >
      <SheetContent>
        <SheetHeader>
          <SheetTitle>Add a skill</SheetTitle>
          <SheetDescription>
            A folder with SKILL.md at the top. Its name and description come from that file.
          </SheetDescription>
        </SheetHeader>

        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            upload.reset();
            if (source.trim()) imported.mutate();
          }}
        >
          <Field
            label="Import from"
            htmlFor="skill-source"
            hint="A GitHub folder, as github:owner/repo/path@ref, or the https address of a zip from a host this brain allows."
          >
            <Input
              id="skill-source"
              value={source}
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => setSource(e.target.value)}
            />
          </Field>
          <Button type="submit" disabled={!source.trim() || busy} loading={imported.isPending}>
            Import
          </Button>
        </form>

        <section aria-labelledby="skill-upload" className="space-y-2 border-t border-rule pt-4">
          <h3 id="skill-upload" className="text-sm font-medium">
            Or upload it from this machine
          </h3>
          <div className="flex flex-wrap gap-2">
            <SkillPicker
              disabled={busy}
              onPick={(body) => {
                imported.reset();
                upload.mutate(body);
              }}
              folderButton="Upload folder"
              zipButton="Upload zip"
              folderLabel="Skill folder"
              zipLabel="Skill zip"
            />
          </div>
        </section>

        {failed ? (
          <p role="alert" className="text-sm text-destructive">
            {failed.message}
          </p>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}

/** One skill read whole, beside the list, with what may be done to it. */
function SkillDetail({
  teamId,
  skill,
  canWrite,
  onChanged,
  onDeleted,
}: {
  teamId: string;
  skill: ListedSkill;
  canWrite: boolean;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const [replacing, setReplacing] = useState(false);
  const detail = useQuery({
    queryKey: ["skills", teamId, skill.id],
    queryFn: () => getSkill(teamId, skill.id),
  });
  const sync = useMutation({
    mutationFn: () => syncSkill(teamId, skill.id),
    onSuccess: () => {
      onChanged();
      toast.success(`Synced ${skill.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const s = detail.data;
  return (
    <>
      <SheetHeader>
        <SheetTitle>{skill.name}</SheetTitle>
        <SheetDescription>{skill.description}</SheetDescription>
      </SheetHeader>

      <p className="flex flex-wrap gap-x-2 text-sm text-muted-foreground">
        <span>{sourceWords(skill.source)}</span>
        <span aria-hidden>·</span>
        <span className={skill.agent_count === 0 ? undefined : "text-foreground"}>
          {skill.agent_count === 0 ? "No agent holds it" : `Held by ${heldBy(skill)}`}
        </span>
        <span aria-hidden>·</span>
        <span>
          updated <Time iso={skill.updated_at} />
        </span>
      </p>
      {s?.source_ref ? (
        <p className="text-xs text-muted-foreground">
          From <Mono className="break-all text-foreground">{s.source_ref}</Mono>
          {s.source_sha ? (
            <>
              {" "}
              at <Mono>{s.source_sha.slice(0, 12)}</Mono>
            </>
          ) : null}
        </p>
      ) : null}

      {detail.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : detail.isError ? (
        <ErrorState what="this skill" error={detail.error} onRetry={() => detail.refetch()} />
      ) : s ? (
        <>
          <section aria-labelledby="skill-body" className="space-y-2">
            <h3 id="skill-body" className="text-sm font-medium">
              SKILL.md
            </h3>
            {/* The team's markdown, shown as text: rendering it as HTML would
                run whatever an imported skill's author put there. */}
            <pre className="max-h-80 overflow-auto rounded-lg bg-card p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">
              {s.body}
            </pre>
          </section>
          <section aria-labelledby="skill-files" className="space-y-2">
            <h3 id="skill-files" className="text-sm font-medium">
              Files
              <span className="ml-1.5 font-normal text-muted-foreground">{s.files.length + 1}</span>
            </h3>
            <ul className="divide-y divide-rule rounded-lg bg-card text-xs">
              <li className="px-3 py-1.5">
                <Mono className="text-foreground">SKILL.md</Mono>
              </li>
              {s.files.map((f) => (
                <li key={f.path} className="flex justify-between gap-3 px-3 py-1.5">
                  <Mono className="truncate text-foreground">{f.path}</Mono>
                  <span className="shrink-0 text-muted-foreground tabular-nums">
                    {fileSize(f.size)}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        </>
      ) : null}

      {canWrite ? (
        <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-rule pt-4">
          {skill.source !== "upload" ? (
            <Button variant="outline" loading={sync.isPending} onClick={() => sync.mutate()}>
              Sync
            </Button>
          ) : null}
          <Button variant="outline" onClick={() => setReplacing(true)}>
            Upload new version
          </Button>
          <DeleteSkill teamId={teamId} skill={skill} onDeleted={onDeleted} />
        </div>
      ) : null}

      {canWrite ? (
        <ReplaceDialog
          teamId={teamId}
          skill={skill}
          open={replacing}
          onClose={() => setReplacing(false)}
          onChanged={onChanged}
        />
      ) : null}
    </>
  );
}

/**
 * The brain refuses to delete a skill an agent holds, so a held skill says
 * so where Delete would be, rather than offering what will be refused.
 */
function DeleteSkill({
  teamId,
  skill,
  onDeleted,
}: {
  teamId: string;
  skill: ListedSkill;
  onDeleted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteSkill(teamId, skill.id),
    onSuccess: () => {
      setOpen(false);
      toast.success(`Deleted ${skill.name}`);
      onDeleted();
    },
  });

  if (skill.agent_count > 0) {
    return (
      <p className="ml-auto max-w-56 text-right text-xs text-muted-foreground">
        Held by {heldBy(skill)}: take it from them before deleting it.
      </p>
    );
  }

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
            <DialogTitle>Delete {skill.name}?</DialogTitle>
            <DialogDescription>Its files go with it. It cannot be restored.</DialogDescription>
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
  skill: ListedSkill;
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
