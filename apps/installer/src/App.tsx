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
  const [gateway, setGateway] = useState("http://127.0.0.1:20128/v1");

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

  const start = async () => {
    setScreen("installing");
    try {
      await invoke("install", { root: "", objects, secrets, gateway });
    } catch (err) {
      setProgress((current) => ({ ...current, failure: String(err) }));
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
            ["s3", "S3 or compatible"],
          ]}
        />

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

        <label className="field">
          <span>Model gateway</span>
          <small>Nothing calls it yet. Recorded for when it does.</small>
          <input value={gateway} onChange={(e) => setGateway(e.target.value)} />
        </label>

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
