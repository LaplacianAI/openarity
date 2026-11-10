import type { Blueprint, BlueprintChange, BlueprintSkill } from "@/api/types";
import { folderUpload } from "@/lib/skills";

/** A skill folder the person added: its own name, and its files. */
export type Folder = { name: string; files: { path: string; file: File }[] };

/** A folder picker's files as one folder, .git left out, as an upload is. */
export function folderFrom(files: FileList | File[]): Folder | null {
  const upload = folderUpload(files);
  const picked = "files" in upload ? upload.files : [];
  const name = picked[0]?.path.split("/")[0];
  return name ? { name, files: picked } : null;
}

/**
 * The blueprint with every folder as a skill. A folder sits at its own name
 * in each path, which the brain reads as a skill inside one directory named
 * as the skill.
 */
export async function withSkills(blueprint: Blueprint, folders: Folder[]): Promise<Blueprint> {
  if (folders.length === 0) return blueprint;
  const skills: BlueprintSkill[] = await Promise.all(
    folders.map(async (f) => ({
      files: await Promise.all(
        f.files.map(async ({ path, file }) => ({ path, data: base64(await bytesOf(file)) })),
      ),
    })),
  );
  return { ...blueprint, skills };
}

function bytesOf(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === "function") return file.arrayBuffer();
  // jsdom's File has no arrayBuffer; FileReader is in every browser too.
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error ?? new Error(`could not read ${file.name}`));
    reader.readAsArrayBuffer(file);
  });
}

/** Base64 in slices, so a large file does not overflow the argument list. */
export function base64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}

/** How many items a plan creates, changes and leaves alone. */
export function tally(changes: BlueprintChange[]): {
  create: number;
  update: number;
  same: number;
} {
  const t = { create: 0, update: 0, same: 0 };
  for (const c of changes) {
    if (c.action === "create") t.create++;
    else if (c.action === "update") t.update++;
    else t.same++;
  }
  return t;
}

export const KIND_WORDS = { mcp_server: "MCP server", skill: "Skill", agent: "Agent" } as const;
