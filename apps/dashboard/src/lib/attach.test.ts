import { describe, expect, it } from "vitest";

import {
  type Attached,
  admit,
  fileProgress,
  isPicture,
  MAX_FILE_BYTES,
  MAX_FILES,
  refusal,
  sendContent,
  splitName,
} from "@/lib/attach";

function file(name: string, type: string, size = 10, lastModified = 1): File {
  const f = new File([new Uint8Array(Math.min(size, 16))], name, { type, lastModified });
  // Large sizes are claimed rather than allocated.
  Object.defineProperty(f, "size", { value: size });
  return f;
}

let n = 0;
const id = () => `a${++n}`;

describe("refusal", () => {
  it("takes what the brain takes, and what only the brain can tell", () => {
    for (const f of [
      file("a.png", "image/png"),
      file("a.pdf", "application/pdf"),
      file("notes.md", ""),
      file("data.csv", "text/csv"),
      file("song.mp3", "audio/mpeg"),
      file("a.zip", "application/zip"),
    ]) {
      expect(refusal(f), f.name).toBeNull();
    }
  });

  it("refuses what the brain is sure to refuse, and says why", () => {
    expect(refusal(file("e.txt", "text/plain", 0))).toBe("It is empty.");
    expect(refusal(file("big.pdf", "application/pdf", MAX_FILE_BYTES + 1))).toBe(
      "It is over 25 MB.",
    );
    expect(refusal(file("clip.mov", "video/quicktime"))).toBe("Videos are not accepted.");
    expect(refusal(file("photo.heic", "image/heic"))).toMatch(/Only PNG, JPEG, GIF and WebP/);
    expect(refusal(file("a.flac", "audio/flac"))).toMatch(/Only MP3 and WAV/);
  });

  it("takes a file of exactly 25 MB", () => {
    expect(refusal(file("edge.pdf", "application/pdf", MAX_FILE_BYTES))).toBeNull();
  });
});

describe("admit", () => {
  it("leaves out a file picked twice", () => {
    const a = file("a.png", "image/png");
    const once = admit([], [a], id);
    expect(admit(once, [file("a.png", "image/png")], id)).toHaveLength(1);
  });

  it("takes a different file of the same name", () => {
    const once = admit([], [file("a.png", "image/png", 10, 1)], id);
    expect(admit(once, [file("a.png", "image/png", 11, 1)], id)).toHaveLength(2);
  });

  it("refuses the ninth file rather than dropping it unseen", () => {
    const eight = Array.from({ length: MAX_FILES }, (_, i) => file(`f${i}.txt`, "text/plain"));
    const got = admit([], [...eight, file("ninth.txt", "text/plain")], id);
    expect(got.filter((a) => a.refused === null)).toHaveLength(MAX_FILES);
    expect(got.at(-1)).toMatchObject({ refused: "A message carries at most 8 files." });
  });

  // A refused file holds no place among the eight.
  it("counts only the files that will be sent", () => {
    const start: Attached[] = admit([], [file("clip.mov", "video/mp4")], id);
    const seven = Array.from({ length: MAX_FILES }, (_, i) => file(`f${i}.txt`, "text/plain"));
    const got = admit(start, seven, id);
    expect(got.filter((a) => a.refused === null)).toHaveLength(MAX_FILES);
  });
});

describe("sendContent", () => {
  it("is the same for the same words and files, and differs when either changes", () => {
    const a = file("a.png", "image/png");
    expect(sendContent("hi", [a])).toBe(sendContent("hi", [a]));
    expect(sendContent("hi", [a])).not.toBe(sendContent("hi", []));
    expect(sendContent("hi", [a])).not.toBe(sendContent("hi!", [a]));
  });
});

describe("splitName", () => {
  it("keeps the extension apart, so a cut name still says its kind", () => {
    expect(splitName("quarterly report.pdf")).toEqual(["quarterly report", ".pdf"]);
    expect(splitName("archive.tar.gz")).toEqual(["archive.tar", ".gz"]);
  });

  it("treats a leading dot, no dot, or a long tail as no extension", () => {
    expect(splitName(".env")).toEqual([".env", ""]);
    expect(splitName("README")).toEqual(["README", ""]);
    expect(splitName("v1.thisisnotanextension")).toEqual(["v1.thisisnotanextension", ""]);
  });
});

describe("fileProgress", () => {
  it("fills the files in the order they go", () => {
    expect(fileProgress([100, 300], 0, 400)).toEqual([0, 0]);
    expect(fileProgress([100, 300], 100, 400)).toEqual([1, 0]);
    expect(fileProgress([100, 300], 250, 400)).toEqual([1, 0.5]);
    expect(fileProgress([100, 300], 400, 400)).toEqual([1, 1]);
  });

  it("spreads the body's own bytes across the files", () => {
    // 20 bytes of text and boundaries: half the body is half of the files.
    expect(fileProgress([50, 50], 60, 120)).toEqual([1, 0]);
  });

  it("never reads past whole, nor divides by nothing", () => {
    expect(fileProgress([10], 999, 100)).toEqual([1]);
    expect(fileProgress([10], 0, 0)).toEqual([0]);
  });
});

describe("isPicture", () => {
  it("draws the raster types the brain takes, and never an SVG", () => {
    expect(isPicture("image/PNG")).toBe(true);
    expect(isPicture("image/webp")).toBe(true);
    expect(isPicture("image/svg+xml")).toBe(false);
    expect(isPicture("application/pdf")).toBe(false);
  });
});
