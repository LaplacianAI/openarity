/**
 * Which files a message may carry. The brain decides: it reads each file's
 * bytes for its type and refuses what it does not accept. This only refuses
 * early what the brain is sure to refuse, so a person learns it while they
 * can still change the message, not after Send. A file whose type the browser
 * cannot tell (a .md, a .log) goes to the brain, which reads it as text.
 */

/** As the brain's: a message carries at most 8 files, each at most 25 MB. */
export const MAX_FILES = 8;
export const MAX_FILE_BYTES = 25 << 20;

/** A file in the composer: one to send, or one refused with the reason why. */
export type Attached = { id: string; file: File; refused: string | null };

const IMAGES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const AUDIO = new Set(["audio/mpeg", "audio/mp3", "audio/wav", "audio/wave", "audio/x-wav"]);

/** Why the brain would refuse this file, or null when it may take it. */
export function refusal(f: File): string | null {
  if (f.size === 0) return "It is empty.";
  if (f.size > MAX_FILE_BYTES) return "It is over 25 MB.";
  const t = f.type.toLowerCase();
  if (t.startsWith("video/")) return "Videos are not accepted.";
  if (t.startsWith("image/") && !IMAGES.has(t))
    return "Only PNG, JPEG, GIF and WebP images are accepted.";
  if (t.startsWith("audio/") && !AUDIO.has(t)) return "Only MP3 and WAV audio is accepted.";
  return null;
}

/** The same file picked twice: one name, one size, one modification time. */
function same(a: File, b: File): boolean {
  return a.name === b.name && a.size === b.size && a.lastModified === b.lastModified;
}

/**
 * Adds files to those already attached. One picked again is left out. Each
 * past the eighth is refused with the reason, rather than dropped unseen.
 */
export function admit(current: Attached[], incoming: File[], id: () => string): Attached[] {
  const next = [...current];
  for (const file of incoming) {
    if (next.some((a) => same(a.file, file))) continue;
    const kept = next.filter((a) => a.refused === null).length;
    const refused =
      refusal(file) ?? (kept >= MAX_FILES ? `A message carries at most ${MAX_FILES} files.` : null);
    next.push({ id: id(), file, refused });
  }
  return next;
}

/** What a send is, for its key: the words and each file, so a retry of the same send is one message. */
export function sendContent(text: string, files: File[]): string {
  return [text, ...files.map((f) => `${f.name}\u0000${f.size}\u0000${f.lastModified}`)].join(
    "\u0001",
  );
}

/** The name without its extension, and the extension, so a cut name keeps what kind it is. */
export function splitName(name: string): [string, string] {
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || name.length - dot > 8) return [name, ""];
  return [name.slice(0, dot), name.slice(dot)];
}

/** An image the dashboard draws: a raster type the brain accepts, never an SVG, which can carry script. */
export function isPicture(mediaType: string): boolean {
  return IMAGES.has(mediaType.toLowerCase());
}

/**
 * How far each file has gone, 0 to 1, from how much of the whole body has.
 * The files go in order, so the share sent fills them one after another; the
 * few bytes of the text and the form's boundaries are spread across them.
 */
export function fileProgress(sizes: number[], sent: number, total: number): number[] {
  const all = sizes.reduce((n, s) => n + s, 0);
  let gone = total > 0 ? (Math.min(sent, total) / total) * all : 0;
  return sizes.map((s) => {
    const done = s === 0 ? 1 : Math.max(0, Math.min(1, gone / s));
    gone -= s;
    return done;
  });
}
