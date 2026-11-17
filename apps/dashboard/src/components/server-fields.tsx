import { Field, TextArea } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import { Mono } from "@/components/ui-bits";
import type { ServerDraft } from "@/lib/server-draft";

/**
 * An MCP server's fields, as the Servers screen registers and edits one and
 * Blueprint drafts one. What they become is the caller's: a request now, or an
 * item in a draft.
 */
export function ServerFields({
  draft,
  set,
  withToken = false,
  tokenSet = false,
  clientSecretSet = false,
}: {
  draft: ServerDraft;
  set: (patch: Partial<ServerDraft>) => void;
  /** Offer a token to write to the secret store, as the Servers screen does. */
  withToken?: boolean;
  /** The server already names a token. */
  tokenSet?: boolean;
  /** The server already names a client secret. */
  clientSecretSet?: boolean;
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
          {withToken ? (
            <fieldset className="space-y-1.5">
              <legend className="text-sm font-medium">Signed in with</legend>
              <div className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
                {(["token", "oauth"] as const).map((a) => (
                  <label key={a} className="flex cursor-pointer items-center gap-2">
                    <input
                      type="radio"
                      name="auth"
                      checked={draft.auth === a}
                      onChange={() => set({ auth: a })}
                      className="size-4 accent-primary"
                    />
                    {a === "token" ? "A token, or nothing" : "OAuth, on the dashboard"}
                  </label>
                ))}
              </div>
            </fieldset>
          ) : null}
          {withToken && draft.auth === "oauth" ? (
            <SignInFields draft={draft} set={set} secretSet={clientSecretSet} />
          ) : null}
          {withToken && draft.auth === "token" ? (
            <Field
              label="Token"
              htmlFor="server-token"
              hint={
                tokenSet
                  ? "A token is set. Type a new one to replace it; it is never shown again."
                  : "Optional. Sent as Authorization: Bearer <token>, kept in the secret store, never shown again."
              }
            >
              <Input
                id="server-token"
                type="password"
                value={draft.token}
                autoComplete="off"
                spellCheck={false}
                placeholder={tokenSet ? "••••••••" : ""}
                onChange={(e) => set({ token: e.target.value })}
              />
            </Field>
          ) : null}
          {withToken && draft.auth === "token" ? (
            <details className="text-sm">
              <summary className="cursor-pointer text-muted-foreground">
                Use a secret you manage in the secret store yourself
              </summary>
              <div className="pt-2">
                <AuthRef draft={draft} set={set} />
              </div>
            </details>
          ) : withToken ? null : (
            <AuthRef draft={draft} set={set} />
          )}
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

function AuthRef({
  draft,
  set,
}: {
  draft: ServerDraft;
  set: (patch: Partial<ServerDraft>) => void;
}) {
  return (
    <Field
      label="Bearer token reference"
      htmlFor="server-auth"
      hint="Optional. teams/<team_id>/mcp/<name>#<key>, a secret already in the store."
    >
      <Input
        id="server-auth"
        value={draft.authRef}
        spellCheck={false}
        onChange={(e) => set({ authRef: e.target.value })}
      />
    </Field>
  );
}

/**
 * A server that signs in: the OAuth client registered for it, and the scopes
 * to ask for. The client is registered with the server's provider, with this
 * dashboard's callback as its redirect URI.
 */
function SignInFields({
  draft,
  set,
  secretSet,
}: {
  draft: ServerDraft;
  set: (patch: Partial<ServerDraft>) => void;
  secretSet: boolean;
}) {
  const callback = `${window.location.origin}/oauth/mcp/callback`;
  return (
    <>
      <p className="text-xs text-muted-foreground">
        Register an OAuth client with the server's provider, with{" "}
        <Mono className="break-all text-foreground">{callback}</Mono> as its redirect URI. Connect
        the server once it is saved.
      </p>
      <Field label="Client ID" htmlFor="server-client-id">
        <Input
          id="server-client-id"
          value={draft.clientId}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => set({ clientId: e.target.value })}
        />
      </Field>
      <Field
        label="Client secret"
        htmlFor="server-client-secret"
        hint={
          secretSet
            ? "A secret is set. Type a new one to replace it; it is never shown again."
            : "For a confidential client. Kept in the secret store, never shown again."
        }
      >
        <Input
          id="server-client-secret"
          type="password"
          value={draft.clientSecret}
          autoComplete="off"
          spellCheck={false}
          placeholder={secretSet ? "••••••••" : ""}
          onChange={(e) => set({ clientSecret: e.target.value })}
        />
      </Field>
      <Field
        label="Scopes"
        htmlFor="server-scopes"
        hint="Separated by spaces. Empty asks for what the server offers, which a server offering many refuses."
      >
        <TextArea
          id="server-scopes"
          value={draft.scopes}
          spellCheck={false}
          onChange={(e) => set({ scopes: e.target.value })}
        />
      </Field>
    </>
  );
}
