import { Field, TextArea } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import type { ServerDraft } from "@/lib/server-draft";

/**
 * An MCP server's fields, as the Servers screen registers and edits one and
 * Blueprint drafts one. What they become is the caller's: a request now, or an
 * item in a draft.
 */
export function ServerFields({
  draft,
  set,
}: {
  draft: ServerDraft;
  set: (patch: Partial<ServerDraft>) => void;
}) {
  return (
    <>
      <Field
        label="Name"
        htmlFor="server-name"
        hint="Each tool reaches the model as <name>__<tool>."
      >
        <Input
          id="server-name"
          value={draft.name}
          autoComplete="off"
          onChange={(e) => set({ name: e.target.value })}
        />
      </Field>
      <fieldset className="space-y-1.5">
        <legend className="text-sm font-medium">Reached at</legend>
        <div className="flex gap-5 text-sm">
          {(["url", "command"] as const).map((t) => (
            <label key={t} className="flex cursor-pointer items-center gap-2">
              <input
                type="radio"
                name="transport"
                checked={draft.transport === t}
                onChange={() => set({ transport: t })}
                className="size-4 accent-primary"
              />
              {t === "url" ? "A URL" : "A command"}
            </label>
          ))}
        </div>
      </fieldset>
      {draft.transport === "url" ? (
        <>
          <Field label="URL" htmlFor="server-url">
            <Input
              id="server-url"
              value={draft.url}
              placeholder="https://mcp.example.com/v1"
              spellCheck={false}
              onChange={(e) => set({ url: e.target.value })}
            />
          </Field>
          <Field label="Bearer token reference" htmlFor="server-auth" hint="Optional.">
            <Input
              id="server-auth"
              value={draft.authRef}
              spellCheck={false}
              onChange={(e) => set({ authRef: e.target.value })}
            />
          </Field>
        </>
      ) : (
        <>
          <Field label="Command" htmlFor="server-command" hint="One argument per line.">
            <TextArea
              id="server-command"
              value={draft.command}
              spellCheck={false}
              onChange={(e) => set({ command: e.target.value })}
            />
          </Field>
          <Field label="Environment" htmlFor="server-env" hint="NAME=reference, one per line.">
            <TextArea
              id="server-env"
              value={draft.env}
              spellCheck={false}
              onChange={(e) => set({ env: e.target.value })}
            />
          </Field>
        </>
      )}
      <SwitchField
        label="Bare"
        description="Its tools reach the model without the server's name in front."
        checked={draft.bare}
        onCheckedChange={(bare) => set({ bare })}
      />
    </>
  );
}
