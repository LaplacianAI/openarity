import { parse, YAMLParseError } from "yaml";

import type { Blueprint, BlueprintChange, BlueprintSkill } from "@/api/types";
import { folderUpload } from "@/lib/skills";

const KEYS = ["mcp_servers", "skills", "agents"] as const;

/**
 * What a blueprint's text says, before its skills are read. `skillPaths` are
 * the folders the file names, which the page asks for, since a browser cannot
 * open a path from a file the way oa does.
 */
export type ReadBlueprint =
  | { ok: true; blueprint: Blueprint; skillPaths: string[] }
  | { ok: false; problem: string };

/**
 * Reads YAML or JSON (which is YAML). Only the shape is checked here: a key
 * the format does not have is refused, so a misspelt section is not quietly
 * left out. Every field inside is the brain's to check.
 */
export function readBlueprint(text: string): ReadBlueprint {
  if (!text.trim()) return { ok: false, problem: "Write or paste a blueprint first." };

  let doc: unknown;
  try {
    doc = parse(text);
  } catch (err) {
    if (err instanceof YAMLParseError) {
      const line = err.linePos?.[0]?.line;
      return { ok: false, problem: `${line ? `Line ${line}: ` : ""}${firstLine(err.message)}` };
    }
    throw err;
  }

  if (doc === null || typeof doc !== "object" || Array.isArray(doc)) {
    return { ok: false, problem: "A blueprint is a map of mcp_servers, skills and agents." };
  }
  const map = doc as Record<string, unknown>;
  for (const key of Object.keys(map)) {
    if (!(KEYS as readonly string[]).includes(key)) {
      return {
        ok: false,
        problem: `"${key}" is not part of a blueprint: it holds mcp_servers, skills and agents.`,
      };
    }
  }

  const blueprint: Blueprint = {};
  for (const key of ["mcp_servers", "agents"] as const) {
    const list = map[key];
    if (list === undefined || list === null) continue;
    if (!Array.isArray(list) || !list.every(isMap)) {
      return { ok: false, problem: `${key} is a list, one entry per item.` };
    }
    blueprint[key] = list;
  }

  const skillPaths: string[] = [];
  const skills = map.skills;
  if (skills !== undefined && skills !== null) {
    if (!Array.isArray(skills))
      return { ok: false, problem: "skills is a list of folders, each a path:." };
    for (const [i, s] of skills.entries()) {
      if (!isMap(s) || typeof s.path !== "string" || !s.path.trim()) {
        return {
          ok: false,
          problem: `Skill ${i + 1} has no path: name the folder that holds its SKILL.md.`,
        };
      }
      skillPaths.push(s.path);
    }
  }
  return { ok: true, blueprint, skillPaths };
}

function isMap(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === "object" && !Array.isArray(v);
}

function firstLine(s: string): string {
  return s.split("\n")[0] ?? s;
}

/** A skill folder the person added: its own name, and its files. */
export type Folder = { name: string; files: { path: string; file: File }[] };

/** A folder picker's files as one folder, .git left out, as an upload is. */
export function folderFrom(files: FileList | File[]): Folder | null {
  const upload = folderUpload(files);
  const picked = "files" in upload ? upload.files : [];
  const name = picked[0]?.path.split("/")[0];
  return name ? { name, files: picked } : null;
}

/** The folder a blueprint's path names, by its last segment. */
function nameOf(path: string): string {
  return path.replace(/\/+$/, "").split("/").pop() ?? path;
}

/** The paths the file names that no added folder answers. */
export function missingFolders(paths: string[], folders: Folder[]): string[] {
  const have = new Set(folders.map((f) => f.name));
  return paths.filter((p) => !have.has(nameOf(p)));
}

/**
 * The request: the text's servers and agents, and every added folder as a
 * skill. A folder sits at its own name in each path, which the brain reads as
 * a skill inside one directory named as the skill.
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
