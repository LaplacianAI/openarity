import type { Member } from "@/api/types";

/** A name's words, lowercased: "Erik Lund" and "erik.lund@halden.co" both give erik, lund. */
function words(s: string): string[] {
  return s
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean);
}

/**
 * The member a waiting sender most likely is: the one whose email or subject
 * holds every word of the sender's name. A suggestion only when exactly one
 * member fits, since a guess between two would bind a stranger to the wrong
 * person. Before the @ only: a domain shared by the whole team matches anyone.
 */
export function suggestMember(senderName: string, members: Member[]): string | null {
  const said = words(senderName);
  if (said.length === 0) return null;
  const fits = members.filter((m) => {
    const known = [...words((m.email ?? "").split("@")[0] ?? ""), ...words(m.subject)];
    return said.every((w) => known.includes(w));
  });
  return fits.length === 1 ? (fits[0]?.user_id ?? null) : null;
}
