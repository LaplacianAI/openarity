/**
 * Files on a message being written, wherever one is written: the reply
 * composer, Ask on Home, the builder. Picked from the paperclip (the way in
 * that works from a keyboard and on a phone), dropped on the form, or pasted
 * as a screenshot. Each shows as a chip; a refused one stays in sight with its
 * reason until it is removed, so nobody believes it went.
 */
import { Paperclip } from "lucide-react";
import { type ClipboardEvent, type DragEvent, useCallback, useId, useRef, useState } from "react";

import { AttachedChip } from "@/components/file-chip";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { type Attached, admit, fileProgress, MAX_FILES } from "@/lib/attach";
import { cn } from "@/lib/utils";

/** Whether what is dragged holds files, rather than text or a link. */
function carriesFiles(e: DragEvent): boolean {
  return Array.from(e.dataTransfer.types).includes("Files");
}

/** How much of a send has gone, for its chips; null while nothing is going. */
export function useProgress() {
  const [sent, setSent] = useState<{ sent: number; total: number } | null>(null);
  const report = useCallback((s: number, total: number) => setSent({ sent: s, total }), []);
  const reset = useCallback(() => setSent(null), []);
  return { sent, report, reset };
}

export function useAttachments({ busy, changed }: { busy: boolean; changed?: () => void }) {
  const [attached, setAttached] = useState<Attached[]>([]);
  const [dragging, setDragging] = useState(false);
  // dragenter and dragleave fire for every child crossed; this counts them so
  // the outline stays while the pointer is anywhere over the form.
  const depth = useRef(0);

  const add = (incoming: File[]) => {
    if (incoming.length === 0) return;
    setAttached((cur) => admit(cur, incoming, () => crypto.randomUUID()));
    changed?.();
  };
  const remove = (key: string) => {
    setAttached((cur) => cur.filter((a) => a.id !== key));
    changed?.();
  };

  const dropProps = {
    onDragEnter: (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      depth.current += 1;
      setDragging(true);
    },
    onDragOver: (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = busy ? "none" : "copy";
    },
    onDragLeave: (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    },
    onDrop: (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      depth.current = 0;
      setDragging(false);
      if (!busy) add(Array.from(e.dataTransfer.files));
    },
  };

  /** A screenshot or a copied file attaches; text pastes as text. */
  const onPaste = (e: ClipboardEvent) => {
    const pasted = Array.from(e.clipboardData.files);
    if (pasted.length === 0 || e.clipboardData.getData("text/plain") !== "") return;
    e.preventDefault();
    add(pasted);
  };

  return {
    attached,
    files: attached.filter((a) => a.refused === null).map((a) => a.file),
    refused: attached.filter((a) => a.refused !== null),
    add,
    remove,
    clear: () => setAttached([]),
    dragging,
    dropProps,
    onPaste,
  };
}

export type Attachments = ReturnType<typeof useAttachments>;

/** The outline a form takes while files are dragged over it. */
export const dropTarget = (dragging: boolean) =>
  cn(
    "rounded-lg outline-offset-4 transition-[background-color,outline-color] duration-(--dur-press) ease-(--ease-out)",
    dragging && "bg-accent-wash outline-2 outline-primary/60 outline-dashed",
  );

/** The paperclip, and the file picker it opens. */
export function AttachButton({
  onPick,
  disabled,
  size = "icon",
}: {
  onPick: (files: File[]) => void;
  disabled: boolean;
  size?: "icon" | "icon-sm";
}) {
  const picker = useRef<HTMLInputElement>(null);
  const id = useId();
  return (
    <>
      <input
        ref={picker}
        type="file"
        multiple
        hidden
        tabIndex={-1}
        aria-hidden
        onChange={(e) => {
          onPick(Array.from(e.target.files ?? []));
          // The same file can be picked again after it was removed.
          e.target.value = "";
        }}
      />
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size={size}
            aria-label="Attach files"
            aria-describedby={id}
            disabled={disabled}
            onClick={() => picker.current?.click()}
            className="shrink-0 text-muted-foreground"
          >
            <Paperclip aria-hidden className="size-4" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>Attach files</TooltipContent>
      </Tooltip>
      <span id={id} className="sr-only">
        Up to {MAX_FILES} files, 25 MB each: images, PDF, text, zip or audio.
      </span>
    </>
  );
}

/** The chips of what is attached, each with how much of it has gone while sending. */
export function AttachedList({
  a,
  sending,
  sent,
}: {
  a: Attachments;
  sending: boolean;
  sent: { sent: number; total: number } | null;
}) {
  if (a.attached.length === 0) return null;
  const going = sent
    ? fileProgress(
        a.files.map((f) => f.size),
        sent.sent,
        sent.total,
      )
    : null;
  return (
    <ul aria-label="Attached files" className="flex flex-wrap gap-1.5">
      {a.attached.map((x) => {
        const at = a.files.indexOf(x.file);
        return (
          <AttachedChip
            key={x.id}
            file={x.file}
            refused={x.refused !== null}
            sending={sending}
            progress={going && at >= 0 ? going[at] : undefined}
            onRemove={() => a.remove(x.id)}
          />
        );
      })}
    </ul>
  );
}

/** Why each refused file can't go, until it is removed. */
export function RefusedFiles({ a, className }: { a: Attachments; className?: string }) {
  if (a.refused.length === 0) return null;
  return (
    <ul role="alert" className={cn("space-y-0.5 text-destructive", className)}>
      {a.refused.map((x) => (
        <li key={x.id}>
          {x.file.name} can't be sent. {x.refused} Remove it to send the rest.
        </li>
      ))}
    </ul>
  );
}
