export type Phase = "started" | "progress" | "done" | "failed";

export type Event = {
  step: string;
  state: Phase;
  detail?: string;
  percent?: number;
  url?: string;
  passphrase?: string;
};

// The order they happen in, and the words a person reads. The step names come
// from the installer; these are the labels for them.
export const STEPS: ReadonlyArray<{ id: string; label: string }> = [
  { id: "resolve", label: "Finding what it needs" },
  { id: "download", label: "Downloading PostgreSQL" },
  { id: "cluster", label: "Creating the database" },
  { id: "migrate", label: "Setting up its tables" },
  { id: "identity", label: "Creating your sign-in" },
  { id: "start", label: "Starting Openarity" },
];

export type Progress = {
  states: Record<string, Phase>;
  percent: number;
  failure: string | null;
  url: string | null;
  passphrase: string | null;
};

export const nothingYet: Progress = {
  states: {},
  percent: 0,
  failure: null,
  url: null,
  passphrase: null,
};

// A pure reducer so the whole flow can be tested without a window: feed it the
// lines the installer prints and assert what a person would see.
export function apply(progress: Progress, event: Event): Progress {
  if (event.step === "ready") {
    return {
      ...progress,
      url: event.url ?? null,
      passphrase: event.passphrase ?? null,
    };
  }

  if (event.state === "progress") {
    return { ...progress, percent: event.percent ?? 0 };
  }

  const states = { ...progress.states, [event.step]: event.state };

  return {
    ...progress,
    states,
    // A step that finished takes the bar back to zero rather than leaving it
    // at whatever the last download reported.
    percent: event.state === "done" ? 0 : progress.percent,
    failure: event.state === "failed" ? (event.detail ?? "it failed") : progress.failure,
  };
}

export function readLines(chunk: string): Event[] {
  const out: Event[] = [];

  for (const line of chunk.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) {
      continue;
    }
    try {
      out.push(JSON.parse(trimmed) as Event);
    } catch {
      // The installer writes prose to stderr and events to stdout, but a
      // partial line can still arrive when a chunk splits mid-event. Dropping
      // it is right: the next chunk carries the whole thing.
    }
  }
  return out;
}
