/**
 * A file as one small chip: what kind it is (or, for a picture, the picture),
 * its name with the extension kept when the name is cut, and its size. In the
 * composer it can be removed and shows how much of it has been sent; under a
 * message that was sent it downloads, and a picture shows as a tile.
 */
import { useQuery } from "@tanstack/react-query";
import {
  CircleX,
  FileArchive,
  FileAudio,
  File as FileIcon,
  FileImage,
  FileText,
  X,
} from "lucide-react";
import { useEffect, useState } from "react";

import { isPicture, splitName } from "@/lib/attach";
import { fileSize } from "@/lib/format";
import { cn } from "@/lib/utils";

function KindIcon({ mediaType, className }: { mediaType: string; className?: string }) {
  const t = mediaType.toLowerCase();
  const Icon = t.startsWith("image/")
    ? FileImage
    : t.startsWith("audio/")
      ? FileAudio
      : t.includes("zip")
        ? FileArchive
        : t === "application/pdf" || t.startsWith("text/") || t === ""
          ? FileText
          : FileIcon;
  return <Icon aria-hidden className={cn("size-3.5 shrink-0", className)} />;
}

function Name({ name }: { name: string }) {
  const [base, ext] = splitName(name);
  return (
    <span className="flex min-w-0" title={name}>
      <span className="truncate">{base}</span>
      <span className="shrink-0">{ext}</span>
    </span>
  );
}

/**
 * An object URL for bytes this page holds, revoked when the component that
 * drew them goes. Typed as a raster picture, so the browser draws it as one.
 */
function useObjectURL(bytes: Blob | null | undefined, mediaType: string): string | null {
  const [url, setURL] = useState<string | null>(null);
  useEffect(() => {
    if (!bytes || !isPicture(mediaType)) return;
    const made = URL.createObjectURL(bytes.slice(0, bytes.size, mediaType.toLowerCase()));
    setURL(made);
    return () => {
      URL.revokeObjectURL(made);
      setURL(null);
    };
  }, [bytes, mediaType]);
  return url;
}

const chip =
  "inline-flex h-7 max-w-80 min-w-0 items-center gap-1.5 rounded-lg bg-background text-xs ring-1 ring-sheet-edge ring-inset";

/** A file waiting in the composer, refused there, or going up. */
export function AttachedChip({
  file,
  refused,
  sending,
  progress,
  onRemove,
}: {
  file: File;
  refused: boolean;
  sending: boolean;
  /** How much of it has gone, 0 to 1, while it is being sent. */
  progress?: number;
  onRemove: () => void;
}) {
  const preview = useObjectURL(refused ? null : file, file.type);
  const going = sending && progress !== undefined;
  return (
    <li
      data-file={file.name}
      data-refused={refused || undefined}
      className={cn(
        chip,
        "relative overflow-hidden pr-0.5 pl-2 transition-[opacity,scale] duration-(--dur-press) ease-(--ease-out) starting:scale-96 starting:opacity-0 motion-reduce:starting:scale-100",
        refused && "bg-destructive/8 text-destructive ring-destructive/30",
      )}
    >
      {refused ? (
        <CircleX aria-hidden className="size-3.5 shrink-0" />
      ) : preview ? (
        <img
          src={preview}
          alt=""
          className="size-5 shrink-0 rounded-sm object-cover ring-1 ring-sheet-edge"
        />
      ) : (
        <KindIcon mediaType={file.type} className="text-muted-foreground" />
      )}
      <Name name={file.name} />
      {refused ? null : (
        <span className="shrink-0 text-muted-foreground tabular-nums">{fileSize(file.size)}</span>
      )}
      <button
        type="button"
        aria-label={`Remove ${file.name}`}
        disabled={sending}
        onClick={onRemove}
        className="grid size-6 shrink-0 place-items-center rounded-md text-muted-foreground transition-[background-color,color,scale,opacity] duration-(--dur-press) ease-(--ease-out) hover:bg-foreground/6 hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring active:scale-[0.97] disabled:pointer-events-none disabled:opacity-40 motion-reduce:active:scale-100"
      >
        <X aria-hidden className="size-3.5" />
      </button>
      {going ? (
        <span
          role="progressbar"
          aria-label={`Sending ${file.name}`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(progress * 100)}
          className="absolute inset-x-0 bottom-0 h-0.5 bg-rule"
        >
          {/* Constant motion, so linear; scaleX keeps it off layout. */}
          <span
            className="block h-full origin-left bg-primary transition-transform duration-(--dur-press) ease-linear motion-reduce:transition-none"
            style={{ transform: `scaleX(${progress})` }}
          />
        </span>
      ) : null}
    </li>
  );
}

/** A file sent with a message: pressing it downloads it. */
export function SentChip({
  name,
  size,
  mediaType,
  onOpen,
}: {
  name: string;
  size: number;
  mediaType: string;
  onOpen?: () => void;
}) {
  const body = (
    <>
      <KindIcon mediaType={mediaType} className="text-muted-foreground" />
      <Name name={name} />
      <span className="shrink-0 text-muted-foreground tabular-nums">{fileSize(size)}</span>
    </>
  );
  if (!onOpen) return <span className={cn(chip, "px-2")}>{body}</span>;
  return (
    <button
      type="button"
      aria-label={`Download ${name}`}
      onClick={onOpen}
      className={cn(
        chip,
        "px-2 text-left transition-[background-color,scale] duration-(--dur-press) ease-(--ease-out) hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring active:scale-[0.97] motion-reduce:active:scale-100",
      )}
    >
      {body}
    </button>
  );
}

/**
 * A picture sent with a message, drawn as a tile. Its bytes are fetched with
 * the token, as a link could not be, and drawn from memory; pressing it
 * downloads it. Until the bytes arrive, or if they never do, it is a chip.
 */
export function SentPicture({
  id,
  name,
  size,
  mediaType,
  load,
  onOpen,
}: {
  id: string;
  name: string;
  size: number;
  mediaType: string;
  load: () => Promise<Blob>;
  onOpen: () => void;
}) {
  const bytes = useQuery({
    queryKey: ["attachment-bytes", id],
    queryFn: load,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const url = useObjectURL(bytes.data, mediaType);
  if (bytes.isError)
    return <SentChip name={name} size={size} mediaType={mediaType} onOpen={onOpen} />;
  return (
    <button
      type="button"
      aria-label={`Download ${name}`}
      title={name}
      onClick={onOpen}
      className="group block h-24 w-32 overflow-hidden rounded-lg bg-muted ring-1 ring-sheet-edge ring-inset transition-[scale] duration-(--dur-press) ease-(--ease-out) focus-visible:outline-2 focus-visible:outline-ring active:scale-[0.97] motion-reduce:active:scale-100"
    >
      {url ? (
        <img
          src={url}
          alt=""
          className="size-full object-cover transition-opacity duration-(--dur-press) ease-(--ease-out) starting:opacity-0 group-hover:opacity-90"
        />
      ) : (
        <span
          role="status"
          aria-label={`Loading ${name}`}
          className="grid size-full place-items-center"
        >
          <FileImage aria-hidden className="size-4 text-muted-foreground" />
        </span>
      )}
    </button>
  );
}

/**
 * Saves what a stranger may have sent. Retyped as plain bytes and handed over
 * as a download, so the browser never renders it on this origin, whatever
 * type it claims.
 */
export function saveDownload(bytes: Blob, filename: string): void {
  const url = URL.createObjectURL(bytes.slice(0, bytes.size, "application/octet-stream"));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
