import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { open } from "@tauri-apps/plugin-shell";
import { useEffect, useState } from "react";

import {
  apply,
  nothingYet,
  readLines,
  STEPS,
  whatIsMissing,
  type Existing,
  type Progress,
} from "./steps";

type Screen = "looking" | "already" | "questions" | "installing" | "done";

export function App() {
  const [screen, setScreen] = useState<Screen>("looking");
  const [progress, setProgress] = useState<Progress>(nothingYet);
  const [objects, setObjects] = useState("filesystem");
  const [secrets, setSecrets] = useState("static");
  const [modelBackend, setModelBackend] = useState("external");
  const [gateway, setGateway] = useState("http://127.0.0.1:20128/v1");
  const [modelPath, setModelPath] = useState("");
  const [gatewayPassword, setGatewayPassword] = useState("");
  const [modelKey, setModelKey] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [bucket, setBucket] = useState("openarity");
  const [region, setRegion] = useState("us-east-1");
  const [accessKey, setAccessKey] = useState("");
  const [secretKey, setSecretKey] = useState("");
  const [address, setAddress] = useState("http://127.0.0.1:8200");
  const [kvMount, setKVMount] = useState("secret");
  const [adminToken, setAdminToken] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const [here, setHere] = useState<Existing | null>(null);
  const [starting, setStarting] = useState(false);

  useEffect(() => {
    const stop = listen<{ line: string }>("install", (e) => {
      setProgress((current) => readLines(e.payload.line).reduce(apply, current));
    });
    return () => {
      stop.then((f) => f());
    };
  }, []);

  useEffect(() => {
    if (progress.url) {
      setScreen("done");
    }
  }, [progress.url]);

  // Asked before anything is shown. Opening this on a machine that already
  // has Openarity used to present the whole form, take every answer, and fail
  // at the end with "already installed at ... — run `oa stack start`": a
  // sentence about a command nobody ran, at the end of work that was never
  // going to count.
  useEffect(() => {
    invoke<Existing>("existing")
      .then((found) => {
        setHere(found);
        setScreen(found.installed ? "already" : "questions");
      })
      .catch(() => setScreen("questions"));
  }, []);

  if (screen === "looking") {
    return (
      <main>
        <h1>Set up Openarity</h1>
        <p className="lede">Looking for an existing install&hellip;</p>
      </main>
    );
  }

  if (screen === "already" && here) {
    return (
      <main>
        <h1>Openarity is already here</h1>
        <p className="lede">
          {here.running
            ? "It is installed on this computer and running."
            : "It is installed on this computer but not running at the moment."}
        </p>

        <dl className="details">
          <Detail label="Web address" value={here.url} copyable />
          <Detail label="Sign in as" value={here.sign_in} copyable />
          <Detail label="Installed in" value={here.root} />
        </dl>

        {problem && <p className="failure">{problem}</p>}

        {here.running ? (
          <button type="button" className="primary" onClick={() => void open(here.url)}>
            Open Openarity
          </button>
        ) : (
          <button
            type="button"
            className="primary"
            disabled={starting}
            onClick={() => {
              setStarting(true);
              setProblem(null);
              invoke("start")
                .then(() => setHere({ ...here, running: true }))
                .catch((err) => setProblem(String(err)))
                .finally(() => setStarting(false));
            }}
          >
            {starting ? "Starting…" : "Start Openarity"}
          </button>
        )}

        <p className="lede">
          Your password was shown once when it was installed and is not saved anywhere. If it
          is lost, delete the folder above and set it up again.
        </p>
      </main>
    );
  }

  const runsGateway = modelBackend === "litellm" || modelBackend === "omniroute";

  const start = async () => {
    // Checked before anything is spawned: setup refuses these too, but the
    // answer is in front of the person right now. The rules are in steps.ts
    // so they can be tested without a window.
    const missing = whatIsMissing({ secrets, adminToken, objects, bucket });
    if (missing) {
      setProblem(missing);
      return;
    }
    setProblem(null);

    setScreen("installing");
    try {
      await invoke("install", {
        choices: {
          root: "",
          objects,
          secrets,
          endpoint: objects === "s3" ? endpoint : "",
          bucket: objects === "s3" ? bucket : "",
          region: objects === "s3" ? region : "",
          address: secrets === "static" ? "" : address,
          kvMount: secrets === "static" ? "" : kvMount,
          adminToken: secrets === "static" ? "" : adminToken,
          modelBackend,
          modelPath: runsGateway ? modelPath : "",
          // Only sent when we point at one. A gateway we install is found at
          // the port setup picked, which is not knowable from here.
          gateway: runsGateway ? "" : gateway,
          gatewayPassword: modelBackend === "omniroute" ? gatewayPassword : "",
          accessKey: objects === "s3" ? accessKey : "",
          secretKey: objects === "s3" ? secretKey : "",
          modelKey,
        },
      });
    } catch (err) {
      // Only when nothing more specific arrived. The steps report their own
      // failures on stdout and those say what went wrong; this says the exit
      // code. Overwriting one with the other is how "a gateway of our own
      // needs a directory to install into" reached somebody as "setup exited
      // with Some(1)".
      setProgress((current) => ({
        ...current,
        failure: current.failure ?? String(err),
      }));
    }
  };

  if (screen === "questions") {
    return (
      <main>
        <h1>Set up Openarity</h1>
        <p className="lede">
          Everything runs on this computer and nothing is sent anywhere. It downloads about
          70&nbsp;MB and takes about a minute.
        </p>

        {/*
          One button. Everything below used to be on this screen — three
          questions and up to nine fields, in words nobody outside this
          repository uses: object store, KV mount, AppRole, gateway. For
          "install this on my laptop" the honest number of questions is none,
          and somebody who wants a password vault will open the section that
          says so.
        */}
        <details className="advanced">
          <summary>Advanced settings</summary>

          <Question
            label="Where should files be saved?"
            about="Conversations, uploads, and anything Openarity produces."
            value={objects}
            onChange={setObjects}
            options={[
              ["filesystem", "On this computer"],
              ["memory", "Nowhere — clear everything when it restarts"],
              ["s3", "In cloud storage I already have"],
            ]}
          />

          {objects === "s3" && (
            <div className="follow">
              <Field label="Server address" hint="Leave blank for Amazon S3." value={endpoint} onChange={setEndpoint} />
              <Field label="Bucket name" hint="It has to exist already." value={bucket} onChange={setBucket} />
              <Field label="Region" hint="Leave as it is if you are not sure." value={region} onChange={setRegion} />
              <Field label="Access key" value={accessKey} onChange={setAccessKey} />
              <Field label="Secret key" value={secretKey} onChange={setSecretKey} secret />
            </div>
          )}

          <Question
            label="Where should saved passwords be kept?"
            about="When you connect Openarity to Slack or anything else, that password is kept here."
            value={secrets}
            onChange={setSecrets}
            options={[
              ["static", "Inside Openarity"],
              ["openbao", "In a password vault I run (OpenBao or Vault)"],
            ]}
          />

          {secrets !== "static" && (
            <div className="follow">
              <Field label="Vault address" hint="For example http://127.0.0.1:8200" value={address} onChange={setAddress} />
              <Field label="Where in the vault" hint="Leave as it is if you are not sure." value={kvMount} onChange={setKVMount} />

              {/*
                Openarity needs its own login to that vault and cannot start
                without one, so it creates one — which is the only thing this
                token is for. It is used once and never written down.
              */}
              <Field
                label="A token that can administer it"
                hint="Used once to create Openarity's own login. It is not saved anywhere."
                value={adminToken}
                onChange={setAdminToken}
                secret
              />
            </div>
          )}

          <Question
            label="Where should the AI models come from?"
            about="Nothing uses them yet — this is remembered for when it does."
            value={modelBackend}
            onChange={setModelBackend}
            options={[
              ["external", "Something I already run — nothing is downloaded"],
              ["litellm", "Install LiteLLM here — about 1 GB"],
              ["omniroute", "Install OmniRoute here — about 3.7 GB"],
            ]}
          />

          {runsGateway && (
            <div className="follow">
              <Field
                label="Where should it be installed?"
                hint="Leave blank to keep it with everything else. An external drive is fine."
                value={modelPath}
                onChange={setModelPath}
              />
              {modelBackend === "omniroute" && (
                <Field
                  label="Password for its own screen"
                  hint="Leave blank and one is made for you, shown at the end."
                  value={gatewayPassword}
                  onChange={setGatewayPassword}
                  secret
                />
              )}
              <Field
                label="An API key, if you have one"
                hint="Leave blank. You can add one later."
                value={modelKey}
                onChange={setModelKey}
                secret
              />
            </div>
          )}

          {!runsGateway && (
            <div className="follow">
              <Field label="Its web address" value={gateway} onChange={setGateway} />
              <Field
                label="Its API key, if it needs one"
                hint="Leave blank. Something on your own computer usually needs none."
                value={modelKey}
                onChange={setModelKey}
                secret
              />
            </div>
          )}
        </details>

        {problem && <p className="failure">{problem}</p>}

        <button type="button" className="primary" onClick={start}>
          Install
        </button>
      </main>
    );
  }

  if (screen === "installing") {
    return (
      <main>
        <h1>Setting up Openarity</h1>
        <ol className="steps">
          {STEPS.map((step) => {
            const state = progress.states[step.id];
            return (
              <li key={step.id} data-state={state ?? "waiting"}>
                <span className="mark" />
                <span>{step.label}</span>
                {state === "started" && step.id === "download" && progress.percent > 0 && (
                  <span className="percent">{progress.percent}%</span>
                )}
              </li>
            );
          })}
        </ol>

        {progress.failure && (
          <p className="failure">
            {progress.failure}
          </p>
        )}
      </main>
    );
  }

  return (
    <main>
      <h1>Openarity is ready</h1>

      <p className="lede">
        Here is everything you need to get back in. The password is shown once and is not
        saved anywhere — write it down now.
      </p>

      <dl className="details">
        <Detail label="Web address" value={progress.url} copyable />
        <Detail label="Sign in as" value={progress.signIn} copyable />
        <Detail label="Password" value={progress.passphrase} copyable secret />
        <Detail label="Installed in" value={progress.root} />
      </dl>

      {progress.gatewayURL && (
        <>
          <h2>The AI models screen</h2>
          <p className="lede">
            Its own separate screen, with its own password. This one is saved with the
            install, so it is not lost.
          </p>
          <dl className="details">
            <Detail label="Web address" value={progress.gatewayURL} copyable />
            <Detail label="Password" value={progress.gatewayPassword} copyable secret />
          </dl>
        </>
      )}

      <button
        type="button"
        className="primary"
        onClick={() => {
          if (progress.url) {
            void open(progress.url);
          }
        }}
      >
        Open Openarity
      </button>
    </main>
  );
}

// One row of the last screen. Copyable because a twenty-character password is
// not something to retype, and shown in full rather than as dots: this is the
// only time it is ever displayed.
function Detail({
  label,
  value,
  copyable,
  secret,
}: {
  label: string;
  value: string | null;
  copyable?: boolean;
  secret?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  if (!value) {
    return null;
  }

  return (
    <div className="detail">
      <dt>{label}</dt>
      <dd>
        <span className={secret ? "value secret" : "value"}>{value}</span>
        {copyable && (
          <button
            type="button"
            className="copy"
            onClick={() => {
              void navigator.clipboard.writeText(value).then(() => {
                setCopied(true);
                setTimeout(() => setCopied(false), 1500);
              });
            }}
          >
            {copied ? "Copied" : "Copy"}
          </button>
        )}
      </dd>
    </div>
  );
}

function Field({
  label,
  hint,
  value,
  onChange,
  secret,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (value: string) => void;
  secret?: boolean;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {hint && <small>{hint}</small>}
      {/*
        A password manager that fills a field without React seeing it leaves
        the box showing dots and the state holding "", and the install then
        fails saying no token was given. None of these has any effect on a
        person typing.
      */}
      <input
        type={secret ? "password" : "text"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={secret ? "new-password" : "off"}
        data-1p-ignore
        data-lpignore="true"
      />
    </label>
  );
}

function Question({
  label,
  about,
  value,
  onChange,
  options,
}: {
  label: string;
  about: string;
  value: string;
  onChange: (value: string) => void;
  options: ReadonlyArray<readonly [string, string]>;
}) {
  return (
    <fieldset className="field">
      <legend>{label}</legend>
      <small>{about}</small>
      {options.map(([id, text]) => (
        <label key={id} className="option">
          <input
            type="radio"
            name={label}
            checked={value === id}
            onChange={() => onChange(id)}
          />
          <span>{text}</span>
        </label>
      ))}
    </fieldset>
  );
}
