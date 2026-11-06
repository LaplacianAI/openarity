/**
 * A failed run's error in the dashboard's own words. The brain stores what
 * went wrong as it saw it, and runs stored before a wording changed keep the
 * old wording, a setting's name included. So the kinds of failure the brain
 * is known to write are said here in a sentence, and the text as stored is
 * kept for whoever needs it. An error of no known kind is shown as written.
 */
export type RunError = { said: string; details: string | null };

const KINDS: [pattern: RegExp, say: (m: RegExpMatchArray) => string][] = [
  [
    /^the MCP server "([^"]+)" could not be reached/,
    (m) => `Couldn't reach the MCP server ${m[1]}.`,
  ],
  // Before the brain stopped naming its setting, it said either of these.
  [
    /^the model endpoint (?:OPENARITY_[A-Z_]+ )?could not be reached/,
    () => "Couldn't reach the model.",
  ],
  [
    /^(?:OPENARITY_[A-Z_]+: )?the model endpoint answered (\d{3})(?: [A-Za-z ]+)?/,
    (m) => `The model refused the request (${m[1]}).`,
  ],
  [/token budget/, () => "It ran out of its token budget."],
];

export function explainRunError(raw: string): RunError {
  const text = raw.trim();
  for (const [pattern, say] of KINDS) {
    const m = text.match(pattern);
    if (m) return { said: say(m), details: text };
  }
  return { said: text, details: null };
}
