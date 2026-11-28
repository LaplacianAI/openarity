/**
 * The pages of a team's settings, kept apart from the pages themselves so the
 * always-loaded shell can list them in ⌘K without loading the forms.
 */
export type SettingsPageId = "general" | "tools" | "approvals" | "improvement" | "schedules";

export type SettingsPage = {
  id: SettingsPageId;
  to: `/ui/settings/${SettingsPageId}`;
  label: string;
  /** What the page decides, in a line: said in the phone's list. */
  about: string;
};

/** The pages of the team's settings, in the order they are listed. */
export const SETTINGS_PAGES: readonly SettingsPage[] = [
  {
    id: "general",
    to: "/ui/settings/general",
    label: "General",
    about: "The model, the hub, and when a long run is compacted.",
  },
  {
    id: "tools",
    to: "/ui/settings/tools",
    label: "Tools",
    about: "Web search, where commands run, and the browser.",
  },
  {
    id: "approvals",
    to: "/ui/settings/approvals",
    label: "Approvals",
    about: "Which tool calls wait for a person, and the rules that decide.",
  },
  {
    id: "improvement",
    to: "/ui/settings/improvement",
    label: "Improvement",
    about: "Whether agents propose changes to themselves, and how they are checked.",
  },
  {
    id: "schedules",
    to: "/ui/settings/schedules",
    label: "Schedules",
    about: "Whether agents may propose schedules, the team's zone, and how many and how often.",
  },
];
