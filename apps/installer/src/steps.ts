export type Phase = "started" | "progress" | "done" | "failed";

// What `oa stack info` answers, and the shape Rust passes through. Named
// fields rather than a string, so a rename on either side is a type error
// here rather than an empty row in somebody's window.
export type Existing = {
  installed: boolean;
  running: boolean;
  url: string;
  sign_in: string;
  root: string;
};

export type Event = {
  step: string;
  state: Phase;
  detail?: string;
  percent?: number;
  url?: string;
  sign_in?: string;
  root?: string;
  gateway_url?: string;
  passphrase?: string;
  gateway_password?: string;
};

// The order they happen in, and the words a person reads. The step names come
// from the installer; these are the labels for them.
export const STEPS: ReadonlyArray<{ id: string; label: string }> = [
  { id: "resolve", label: "Getting ready" },
  { id: "download", label: "Downloading" },
  { id: "cluster", label: "Setting up storage" },
  { id: "migrate", label: "Preparing it" },
  { id: "gateway", label: "Installing the AI models part" },
  { id: "identity", label: "Setting up your sign-in" },
  { id: "start", label: "Starting Openarity" },
];

export type Progress = {
  states: Record<string, Phase>;
  percent: number;
  failure: string | null;
  url: string | null;
  signIn: string | null;
  root: string | null;
  gatewayURL: string | null;
  passphrase: string | null;
  gatewayPassword: string | null;
};

export const nothingYet: Progress = {
  states: {},
  percent: 0,
  failure: null,
  url: null,
  signIn: null,
  root: null,
  gatewayURL: null,
  passphrase: null,
  gatewayPassword: null,
};

// A pure reducer so the whole flow can be tested without a window: feed it the
// lines the installer prints and assert what a person would see.
export function apply(progress: Progress, event: Event): Progress {
  if (event.step === "ready") {
    return {
      ...progress,
      url: event.url ?? null,
      signIn: event.sign_in ?? null,
      root: event.root ?? null,
      gatewayURL: event.gateway_url ?? null,
      passphrase: event.passphrase ?? null,
      gatewayPassword: event.gateway_password ?? null,
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

// What the window must not send, checked before anything is spawned.
//
// Setup refuses these too, and says so within a second — but the answer is in
// front of the person here, and reaching them through a sidecar's stderr is
// the long way round. A blank admin token is the one that matters: an app
// opened from Finder inherits no shell environment, so the AppRole-already-in-
// hand path `oa` supports cannot be reached from this window, and blank can
// only mean blank — including when the box is showing dots a password manager
// put there without React seeing them.
//
// A list rather than a check per field, so the next one is a line here.
export function whatIsMissing(choices: {
  secrets: string;
  adminToken: string;
  objects: string;
  bucket: string;
}): string | null {
  const rules: Array<{ when: boolean; blank: string; say: string }> = [
    {
      when: choices.secrets !== "static",
      blank: choices.adminToken,
      say: "The vault token is empty. Openarity needs one to create its own login.",
    },
    {
      when: choices.objects === "s3",
      blank: choices.bucket,
      say: "The bucket name is empty. Cloud storage needs one to save files into.",
    },
  ];

  for (const rule of rules) {
    if (rule.when && rule.blank.trim() === "") {
      return rule.say;
    }
  }
  return null;
}
