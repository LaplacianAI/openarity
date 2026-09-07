import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { open } from "@tauri-apps/plugin-shell";
import { useEffect, useState } from "react";

import { apply, nothingYet, readLines, STEPS, type Progress } from "./steps";

type Screen = "questions" | "installing" | "done";

export function App() {
  const [screen, setScreen] = useState<Screen>("questions");
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
  const [roleID, setRoleID] = useState("");
  const [roleSecret, setRoleSecret] = useState("");
  const [minioPath, setMinioPath] = useState("");

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

  const runsGateway = modelBackend === "litellm" || modelBackend === "omniroute";

  const start = async () => {
    setScreen("installing");
    try {
      await invoke("install", {
        choices: {
          root: "",
          objects,
          secrets,
          endpoint: objects === "s3" ? endpoint : "",
          bucket: objects === "s3" || objects === "minio" ? bucket : "",
          minioPath: objects === "minio" ? minioPath : "",
          region: objects === "s3" ? region : "",
          address: secrets === "static" ? "" : address,
          kvMount: secrets === "static" ? "" : kvMount,
          roleId: secrets === "static" ? "" : roleID,
          roleSecret: secrets === "static" ? "" : roleSecret,
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
          This puts everything on this machine — a database, a sign-in and the dashboard. It
          downloads about 325&nbsp;MB and takes a minute.
        </p>

        <Question
          label="Where should files be kept?"
          about="Transcripts, uploads, anything an agent produces."
          value={objects}
          onChange={setObjects}
          options={[
            ["filesystem", "On this machine"],
            ["memory", "In memory — lost on restart"],
            ["minio", "Run MinIO here — an object store of your own"],
            ["s3", "Somewhere else — a bucket you already have"],
          ]}
        />

        {objects === "minio" && (
          <div className="follow">
            <Field
              label="Where should MinIO keep its files?"
              hint="Blank puts them inside the install. An external drive is fine."
              value={minioPath}
              onChange={setMinioPath}
            />
            <Field label="Bucket" hint="Created for you." value={bucket} onChange={setBucket} />
          </div>
        )}

        {objects === "s3" && (
          <div className="follow">
            <Field label="Endpoint" hint="Blank for AWS. For MinIO, http://127.0.0.1:9000" value={endpoint} onChange={setEndpoint} />
            <Field label="Bucket" hint="It must already exist." value={bucket} onChange={setBucket} />
            <Field label="Region" hint="MinIO ignores this." value={region} onChange={setRegion} />
            <Field label="Access key" value={accessKey} onChange={setAccessKey} />
            <Field label="Secret key" value={secretKey} onChange={setSecretKey} secret />
          </div>
        )}

        <Question
          label="Where should credentials be kept?"
          about="The tokens Openarity uses to reach services you connect to it."
          value={secrets}
          onChange={setSecrets}
          options={[
            ["static", "In Openarity itself"],
            ["openbao", "OpenBao or Vault"],
          ]}
        />

        {secrets !== "static" && (
          <div className="follow">
            <Field label="Address" hint="For example http://127.0.0.1:8200" value={address} onChange={setAddress} />
            <Field label="KV mount" value={kvMount} onChange={setKVMount} />
            {/*
              The brain refuses to start without these — a secret store is a
              dependency, not a feature flag. The window used to ask only for
              the address, so choosing OpenBao failed several minutes later,
              after Postgres had been downloaded and a cluster built.
            */}
            <Field label="AppRole ID" value={roleID} onChange={setRoleID} />
            <Field label="AppRole secret" value={roleSecret} onChange={setRoleSecret} secret />
          </div>
        )}

        <Question
          label="Where should Openarity get its models from?"
          about="Anything that speaks the OpenAI API. Nothing calls it yet — recorded for when it does."
          value={modelBackend}
          onChange={setModelBackend}
          options={[
            ["external", "One you already run — nothing is downloaded"],
            ["litellm", "Run LiteLLM here — about 1GB, brings its own Python"],
            ["omniroute", "Run OmniRoute here — about 3.7GB, brings its own Node"],
          ]}
        />

        {runsGateway && (
          <div className="follow">
            <Field
              label="Where should it install?"
              hint="Anywhere with room. Blank puts it inside the install."
              value={modelPath}
              onChange={setModelPath}
            />
            {modelBackend === "omniroute" && (
              <Field
                label="Dashboard password"
                hint="Blank generates one and shows it at the end."
                value={gatewayPassword}
                onChange={setGatewayPassword}
                secret
              />
            )}
            <Field
              label="A provider API key"
              hint="Blank for none. It can be added from the gateway's own dashboard later."
              value={modelKey}
              onChange={setModelKey}
              secret
            />
          </div>
        )}

        {!runsGateway && (
          <div className="follow">
            <Field label="URL" value={gateway} onChange={setGateway} />
            <Field
              label="API key"
              hint="Blank if it needs none, which a gateway on your own machine usually does not."
              value={modelKey}
              onChange={setModelKey}
              secret
            />
          </div>
        )}

        <button type="button" className="primary" onClick={start}>
          Install
        </button>
      </main>
    );
  }

  if (screen === "installing") {
    return (
      <main>
        <h1>Setting up</h1>
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

      <p className="lede">Sign in as dev@openarity.local</p>

      <div className="passphrase">
        <span>{progress.passphrase}</span>
        <small>Write this down. It is not stored anywhere and cannot be shown again.</small>
      </div>

      {progress.gatewayPassword && (
        <div className="passphrase">
          <span>{progress.gatewayPassword}</span>
          <small>
            The model gateway&rsquo;s own dashboard password, generated because none was
            given. Kept in the credentials file.
          </small>
        </div>
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
        Open the dashboard
      </button>
    </main>
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
      <input
        type={secret ? "password" : "text"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
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
