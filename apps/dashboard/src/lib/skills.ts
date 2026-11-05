import type { SkillUpload } from "@/api";
import type { ListedSkill } from "@/api/types";
import { count } from "@/lib/runs";

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

/** Where a skill came from, as a person would say it. */
export function sourceWords(source: string): string {
  switch (source) {
    case "github":
      return "GitHub";
    case "url":
      return "Zip from a link";
    default:
      return "Uploaded";
  }
}

/** Who holds it: "3 agents", or "No agent", which is what is worth noticing. */
export function heldBy(s: ListedSkill): string {
  return s.agent_count === 0 ? "No agent" : count(s.agent_count, "agent");
}
