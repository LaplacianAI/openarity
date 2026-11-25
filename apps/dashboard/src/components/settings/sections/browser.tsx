import type { TeamSettings } from "@/api/types";
import { Choice } from "@/components/choices";
import { Field } from "@/components/registry-bits";
import { Section, type SectionProps } from "@/components/settings/section";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { BROWSER_URL, BROWSERS } from "@/lib/team-settings/catalogue";
import type { SettingsDraft } from "@/lib/team-settings/draft";

/** What the hub, and agents granted one, drive pages in. */
export function BrowserSection({
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
  const browserKeyKept =
    stored.browser?.key_set === true &&
    stored.browser.provider === draft.browser &&
    (stored.browser.url ?? "") === draft.browserURL.trim();
  const browserAbout = BROWSERS.find((p) => p.value === draft.browser)?.about;
  const browserTakesURL = BROWSER_URL.includes(draft.browser);
  const browserSelfHosted =
    draft.browser === "cdp" || (browserTakesURL && draft.browserURL.trim() !== "");

  return (
    <Section
      title="Browser"
      about="What the hub, and agents granted one, drive pages in: open, read, click and type. A page runs its scripts, and a browser of your own is never let reach a private address."
      {...section}
    >
      <Field label="Browser provider" htmlFor="settings-browser" hint={browserAbout}>
        <Choice
          id="settings-browser"
          value={draft.browser}
          options={BROWSERS}
          onChange={(browser) =>
            // The address and key are the old provider's.
            set({ browser, browserURL: "", browserKey: "", dropBrowserKey: false })
          }
        />
      </Field>
      {browserTakesURL ? (
        <Field
          label="Address"
          htmlFor="settings-browser-url"
          hint={
            draft.browser === "cdp"
              ? "Where the brain reaches it, such as ws://headless-shell:9222."
              : draft.browser === "steel"
                ? "Empty for Steel's hosted service; your instance's API address, such as http://steel:3000, otherwise."
                : "Empty for the hosted service; your own browser's address, such as ws://lightpanda:9222, otherwise."
          }
        >
          <Input
            id="settings-browser-url"
            spellCheck={false}
            value={draft.browserURL}
            onChange={(e) => set({ browserURL: e.target.value })}
          />
        </Field>
      ) : null}
      {draft.browser !== "none" && !(browserSelfHosted && draft.dropBrowserKey) ? (
        <Field
          label={browserSelfHosted ? "Token" : "API key"}
          htmlFor="settings-browser-key"
          hint={
            browserKeyKept
              ? "A key is set. Leave this empty to keep it."
              : browserSelfHosted
                ? "Only if your browser asks for one. Kept in the secret store and never shown again."
                : "Kept in the secret store and never shown again."
          }
        >
          <Input
            id="settings-browser-key"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={draft.browserKey}
            onChange={(e) => set({ browserKey: e.target.value })}
          />
        </Field>
      ) : null}
      {browserSelfHosted && stored.browser?.key_set ? (
        <SwitchField
          label="Reach it with no key"
          description="Removes the stored key, for a browser of your own that asks for none."
          checked={draft.dropBrowserKey}
          onCheckedChange={(dropBrowserKey) => set({ dropBrowserKey, browserKey: "" })}
        />
      ) : null}
    </Section>
  );
}
