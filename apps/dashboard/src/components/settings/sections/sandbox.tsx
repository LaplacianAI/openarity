import type { SandboxTemplateBuild, TeamSettings } from "@/api/types";
import { Choice, Segments } from "@/components/choices";
import { Field } from "@/components/registry-bits";
import { Notice, Section, type SectionProps } from "@/components/settings/section";
import { Input } from "@/components/ui/input";
import { Unit } from "@/components/unit-input";
import { SANDBOXES } from "@/lib/team-settings/catalogue";
import type { SettingsDraft } from "@/lib/team-settings/draft";
import { keepLowered, providerChanged, sizeChanged } from "@/lib/team-settings/workspace";

const SIZES = [
  { value: "provider", label: "Provider's" },
  { value: "custom", label: "Custom" },
] as const;
const IDLE = [
  { value: "deployment", label: "Deployment's" },
  { value: "own", label: "After a time" },
] as const;

/** What a workspace's size will be, for the sandbox and size in the draft. */
function sizeHint(draft: SettingsDraft): string {
  if (draft.sandbox === "e2b" && draft.sandboxTemplate.trim() !== "") {
    return "Your own template sets each workspace's size, so a size here is not used.";
  }
  if (draft.sandbox === "e2b") {
    return draft.sizeCustom
      ? "E2B builds a template at this size in your account first, and new workspaces wait a few minutes for it. A size your E2B plan does not allow shows as a failed build. Disk is the free space E2B makes room for."
      : "Each conversation's workspace gets 2 CPUs and 2 GB, the size the default template is built at.";
  }
  if (!draft.sizeCustom) {
    return "Each conversation's workspace gets the size your sandbox gives one.";
  }
  const daytona =
    draft.sandbox === "daytona" ? " Daytona rounds memory and disk up to a whole GB." : "";
  return `Each conversation's workspace gets this. A size past what your sandbox allows a workspace is refused when the workspace is made.${daytona}`;
}

/**
 * How big a conversation's workspace is, and how long it lives: it stops when
 * the conversation's last run ends, and keeps its files while stopped.
 */
function Workspaces({
  draft,
  sizeChanged,
  providerChanged,
  keepLowered,
  onChange,
}: {
  draft: SettingsDraft;
  /** Whether saving would change the size, which gives every conversation a new workspace. */
  sizeChanged: boolean;
  /** Whether saving would move to another sandbox, which does the same. */
  providerChanged: boolean;
  /** The stored keep hours, when the draft lowers them. */
  keepLowered: number | null;
  onChange: (patch: Partial<SettingsDraft>) => void;
}) {
  const ownTemplate = draft.sandbox === "e2b" && draft.sandboxTemplate.trim() !== "";
  return (
    <div className="space-y-4 border-t border-rule pt-4">
      <h3 className="text-sm font-medium">Workspaces</h3>
      {providerChanged ? (
        <Notice>
          Saving gives each conversation a new workspace in this sandbox, without the files in its
          old one.
        </Notice>
      ) : null}
      <fieldset className="space-y-2" aria-describedby="settings-size-hint">
        <legend className="mb-1.5 text-sm font-medium">Size</legend>
        <Segments
          label="Size"
          value={draft.sizeCustom ? "custom" : "provider"}
          options={SIZES}
          onChange={(v) => onChange({ sizeCustom: v === "custom" })}
        />
        {draft.sizeCustom ? (
          <div className="grid gap-3 pt-1 sm:grid-cols-3">
            <Field label="Memory" htmlFor="settings-size-memory">
              <Unit
                id="settings-size-memory"
                unit="GB"
                decimal
                value={draft.sizeMemory}
                onChange={(sizeMemory) => onChange({ sizeMemory })}
              />
            </Field>
            <Field label="CPUs" htmlFor="settings-size-cpus">
              <Unit
                id="settings-size-cpus"
                unit="CPUs"
                value={draft.sizeCPUs}
                onChange={(sizeCPUs) => onChange({ sizeCPUs })}
              />
            </Field>
            <Field label="Disk" htmlFor="settings-size-disk">
              <Unit
                id="settings-size-disk"
                unit="GB"
                decimal
                value={draft.sizeDisk}
                onChange={(sizeDisk) => onChange({ sizeDisk })}
              />
            </Field>
          </div>
        ) : null}
        <p id="settings-size-hint" className="text-xs text-muted-foreground">
          {sizeHint(draft)}
        </p>
        {sizeChanged && !providerChanged && !ownTemplate ? (
          <Notice>
            Saving gives each conversation a new workspace at this size, without the files in its
            old one.
          </Notice>
        ) : null}
      </fieldset>
      <fieldset className="space-y-2" aria-describedby="settings-idle-hint">
        <legend className="mb-1.5 text-sm font-medium">Stop when idle</legend>
        <div className="flex flex-wrap items-center gap-3">
          <Segments
            label="Stop when idle"
            value={draft.idleOwn ? "own" : "deployment"}
            options={IDLE}
            onChange={(v) => onChange({ idleOwn: v === "own" })}
          />
          {draft.idleOwn ? (
            <Unit
              id="settings-idle-minutes"
              aria-label="Minutes idle"
              unit="minutes"
              value={draft.idleMinutes}
              onChange={(idleMinutes) => onChange({ idleMinutes })}
            />
          ) : null}
        </div>
        <p id="settings-idle-hint" className="text-xs text-muted-foreground">
          {draft.idleOwn
            ? "A workspace stops when its conversation's last run ends. This stops one left running, such as one waiting for an approval, after 1 to 1,440 minutes unused."
            : "A workspace stops when its conversation's last run ends. One left running, such as one waiting for an approval, stops after the time your deployment sets."}
        </p>
      </fieldset>
      <Field
        label="Keep stopped"
        htmlFor="settings-keep-hours"
        hint="A stopped workspace keeps its files for the conversation's next run, which starts it again. After this long it is deleted with them, workspaces stopped before a change included. From 1 to 720 hours."
      >
        <Unit
          id="settings-keep-hours"
          unit="hours"
          value={draft.keepHours}
          onChange={(keepHours) => onChange({ keepHours })}
        />
      </Field>
      {keepLowered !== null ? (
        <Notice>
          Lowering it from {keepLowered} hours deletes workspaces already stopped longer than{" "}
          {draft.keepHours.trim()}, with their files, at the next sweep.
        </Notice>
      ) : null}
    </div>
  );
}

/** How far the brain's build of the room template in the team's E2B account is. */
function TemplateBuildStatus({ build }: { build: SandboxTemplateBuild | null }) {
  if (build === null) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        The room template is built in your E2B account when the sandbox is saved, or when the first
        room starts.
      </p>
    );
  }
  const text =
    build.status === "ready"
      ? "Room template built: rooms start from it."
      : build.status === "failed"
        ? `The room template failed to build: ${build.error ?? "E2B gave no reason"}. It is tried again in ten minutes, or when you save.`
        : "Building the room template in your E2B account. This takes a few minutes; until it is done, agents are told to try again.";
  return (
    <p
      className={
        build.status === "failed" ? "text-sm text-destructive" : "text-sm text-muted-foreground"
      }
      role="status"
    >
      {text} <span className="font-mono text-xs">{build.name}</span>
    </p>
  );
}

/** Where the team's command servers and its agents' workspaces run. */
export function SandboxSection({
  stored,
  draft,
  set,
  section,
}: {
  stored: TeamSettings;
  draft: SettingsDraft;
  set: (patch: Partial<SettingsDraft>) => void;
  section: SectionProps;
}) {
  const sandboxKeyKept =
    stored.sandbox?.key_set === true && stored.sandbox.provider === draft.sandbox;
  const sandboxAbout = SANDBOXES.find((p) => p.value === draft.sandbox)?.about;

  return (
    <Section
      title="Sandbox"
      about="Where the team's command MCP servers and its agents' workspaces run. A command server's sandbox reaches only the network its server lists."
      {...section}
    >
      <Field label="Sandbox provider" htmlFor="settings-sandbox" hint={sandboxAbout}>
        <Choice
          id="settings-sandbox"
          value={draft.sandbox}
          options={SANDBOXES}
          onChange={(sandbox) =>
            // The address and key are the old provider's.
            set({ sandbox, sandboxURL: "", sandboxTemplate: "", sandboxKey: "" })
          }
        />
      </Field>
      {draft.sandbox === "daytona" || draft.sandbox === "sandboxd" ? (
        <Field
          label="API address"
          htmlFor="settings-sandbox-url"
          hint={
            draft.sandbox === "daytona"
              ? "Empty for Daytona's hosted service; your instance's API address otherwise."
              : "Where the brain reaches it, such as http://sandboxd:8080."
          }
        >
          <Input
            id="settings-sandbox-url"
            value={draft.sandboxURL}
            onChange={(e) => set({ sandboxURL: e.target.value })}
          />
        </Field>
      ) : null}
      {draft.sandbox === "e2b" ? (
        <Field
          label="Template"
          htmlFor="settings-sandbox-template"
          hint="Empty to start rooms from the image the brain builds in your E2B account. Name a template of your own only to replace it."
        >
          <Input
            id="settings-sandbox-template"
            spellCheck={false}
            placeholder="Built by the brain"
            value={draft.sandboxTemplate}
            onChange={(e) => set({ sandboxTemplate: e.target.value })}
          />
        </Field>
      ) : null}
      {draft.sandbox === "e2b" &&
      stored.sandbox?.provider === "e2b" &&
      !stored.sandbox.template &&
      draft.sandboxTemplate.trim() === "" ? (
        <TemplateBuildStatus build={stored.sandbox.template_build ?? null} />
      ) : null}
      {draft.sandbox !== "none" ? (
        <Field
          label={draft.sandbox === "sandboxd" ? "Token" : "API key"}
          htmlFor="settings-sandbox-key"
          hint={
            sandboxKeyKept
              ? "A key is set. Leave this empty to keep it."
              : "Kept in the secret store and never shown again."
          }
        >
          <Input
            id="settings-sandbox-key"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={draft.sandboxKey}
            onChange={(e) => set({ sandboxKey: e.target.value })}
          />
        </Field>
      ) : null}
      {draft.sandbox !== "none" ? (
        <Workspaces
          draft={draft}
          sizeChanged={sizeChanged(draft, stored)}
          providerChanged={providerChanged(draft, stored)}
          keepLowered={keepLowered(draft, stored)}
          onChange={(patch) => set(patch)}
        />
      ) : null}
    </Section>
  );
}
