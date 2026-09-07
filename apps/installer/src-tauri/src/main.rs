#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use serde::Serialize;
use tauri::{AppHandle, Emitter};
use tauri_plugin_shell::process::CommandEvent;
use tauri_plugin_shell::ShellExt;

#[derive(Clone, Serialize)]
struct Line {
    line: String,
}

// The install itself is `oa stack setup --json`, which reports one event per
// line. This window only reads that stream: reimplementing any of it in Rust
// would mean two installers that could disagree, and the Go one is the one
// with tests.
// The window is TypeScript and sends camelCase; these are Rust and are
// snake_case. Without this only the single-word fields line up, which is why
// an install taking every default worked and choosing anything did not:
// "invalid args `choices` for command `install`: missing field
// `model_backend`".
#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Choices {
    root: String,
    objects: String,
    secrets: String,
    model_backend: String,
    model_path: String,
    endpoint: String,
    bucket: String,
    region: String,
    address: String,
    kv_mount: String,
    gateway: String,
    // Never a flag: argv is readable by every process on this machine, so
    // these go into the child's environment instead.
    gateway_password: String,
    admin_token: String,
    access_key: String,
    secret_key: String,
    model_key: String,
    minio_path: String,
}

#[tauri::command]
async fn install(app: AppHandle, choices: Choices) -> Result<(), String> {
    // The bundle carries brain and dex beside oa, and setup is pointed at
    // them. Without this it has nowhere to get either: there is no published
    // release to download from, and the install stopped at "dex is not
    // published yet — build it and pass --bin-dir" after fetching 70MB of
    // Postgres. Postgres and MinIO are still downloaded; --bin-dir means look
    // here first, not only here.
    let beside_us = std::env::current_exe()
        .map_err(|e| e.to_string())?
        .parent()
        .ok_or("the installer has no directory to find its binaries in")?
        .to_path_buf();

    let mut args = vec![
        "stack".to_string(),
        "setup".to_string(),
        "--json".to_string(),
        "--bin-dir".to_string(),
        beside_us.to_string_lossy().to_string(),
        "--objects".to_string(),
        choices.objects.clone(),
        "--secrets".to_string(),
        choices.secrets.clone(),
    ];

    for (flag, value) in [
        ("--root", &choices.root),
        ("--model-backend", &choices.model_backend),
        ("--model-path", &choices.model_path),
        ("--objects-endpoint", &choices.endpoint),
        ("--objects-bucket", &choices.bucket),
        ("--objects-region", &choices.region),
        ("--secrets-addr", &choices.address),
        ("--secrets-mount", &choices.kv_mount),
        ("--model-gateway", &choices.gateway),
        ("--minio-path", &choices.minio_path),
    ] {
        if !value.is_empty() {
            args.push(flag.to_string());
            args.push(value.clone());
        }
    }

    let mut command = app
        .shell()
        .sidecar("oa")
        .map_err(|e| e.to_string())?
        .args(args);

    if !choices.access_key.is_empty() {
        command = command
            .env("OPENARITY_OBJECTS_ACCESS_KEY", &choices.access_key)
            .env("OPENARITY_OBJECTS_SECRET_KEY", &choices.secret_key);
    }

    if !choices.model_key.is_empty() {
        command = command.env("OPENARITY_MODEL_API_KEY", &choices.model_key);
    }

    // Through the environment, like every other credential here. A dashboard
    // password on the command line is readable by every process on the
    // machine, which is the thing it is meant to keep out.
    if !choices.gateway_password.is_empty() {
        command = command.env("OPENARITY_GATEWAY_PASSWORD", &choices.gateway_password);
    }

    // Used once, to create a role and a policy, and never written down. It
    // can do anything to that server, which is why it goes the same way every
    // other credential does rather than onto a command line.
    if !choices.admin_token.is_empty() {
        command = command.env("OPENARITY_SECRETS_ADMIN_TOKEN", &choices.admin_token);
    }

    let (mut rx, _child) = command.spawn().map_err(|e| e.to_string())?;

    // Kept so a failure can say why rather than only that it happened.
    let mut last_words = String::new();

    while let Some(event) = rx.recv().await {
        match event {
            CommandEvent::Stdout(bytes) => {
                let line = String::from_utf8_lossy(&bytes).to_string();
                let _ = app.emit("install", Line { line });
            }
            // The passphrase and the address are on stdout as an event; stderr
            // carries the prose meant for a terminal. The window has no use for
            // it until something fails, and then it is the only thing that says
            // what — so the last of it is kept rather than only emitted.
            CommandEvent::Stderr(bytes) => {
                let line = String::from_utf8_lossy(&bytes).to_string();
                last_words = line.trim().to_string();
                let _ = app.emit("install-log", Line { line });
            }
            CommandEvent::Terminated(payload) => {
                if payload.code != Some(0) {
                    if last_words.is_empty() {
                        return Err(format!("setup exited with {:?}", payload.code));
                    }
                    return Err(last_words);
                }
            }
            _ => {}
        }
    }

    Ok(())
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .invoke_handler(tauri::generate_handler![install])
        .run(tauri::generate_context!())
        .expect("the installer window could not start");
}

#[cfg(test)]
mod tests {
    use super::Choices;

    /// The window's side of the boundary, written out as the window writes it.
    ///
    /// This is the shape that broke: the frontend is TypeScript and sends
    /// camelCase, these are Rust and are snake_case, and serde matched names
    /// exactly. Every single-word field lined up by accident, so an install
    /// taking all the defaults worked and choosing anything at all failed with
    /// "missing field `model_backend`" — the first multi-word field that is
    /// always sent.
    ///
    /// deny_unknown_fields is what makes this test worth having in the other
    /// direction too: a field renamed on one side and not the other fails here
    /// rather than in somebody's installer.
    const FROM_THE_WINDOW: &str = r#"{
        "root": "",
        "objects": "minio",
        "secrets": "openbao",
        "modelBackend": "omniroute",
        "modelPath": "/somewhere/gateway",
        "endpoint": "http://127.0.0.1:9000",
        "bucket": "openarity",
        "region": "us-east-1",
        "address": "http://127.0.0.1:8200",
        "kvMount": "secret",
        "adminToken": "an-admin-token",
        "gateway": "",
        "gatewayPassword": "a-dashboard-password",
        "accessKey": "an-access-key",
        "secretKey": "a-secret-key",
        "modelKey": "a-model-key",
        "minioPath": "/somewhere/minio"
    }"#;

    #[test]
    fn accepts_what_the_window_sends() {
        let choices: Choices =
            serde_json::from_str(FROM_THE_WINDOW).expect("the window's own JSON must deserialize");

        assert_eq!(choices.model_backend, "omniroute");
        assert_eq!(choices.model_path, "/somewhere/gateway");
        assert_eq!(choices.gateway_password, "a-dashboard-password");
        assert_eq!(choices.kv_mount, "secret");
        assert_eq!(choices.admin_token, "an-admin-token");
        assert_eq!(choices.access_key, "an-access-key");
        assert_eq!(choices.secret_key, "a-secret-key");
        assert_eq!(choices.model_key, "a-model-key");
        assert_eq!(choices.minio_path, "/somewhere/minio");
    }

    /// snake_case is what it used to want, and is not what arrives.
    #[test]
    fn refuses_snake_case_which_the_window_never_sends() {
        let wrong = FROM_THE_WINDOW.replace("modelBackend", "model_backend");
        assert!(
            serde_json::from_str::<Choices>(&wrong).is_err(),
            "a field the window does not send was accepted, so the two sides can drift"
        );
    }

    /// A field added to the window and not here is a silent drop otherwise:
    /// the install proceeds with the answer missing.
    #[test]
    fn refuses_a_field_it_does_not_know() {
        let extra =
            FROM_THE_WINDOW.replace(r#""root": "","#, r#""root": "", "somethingNew": "x","#);
        assert!(
            serde_json::from_str::<Choices>(&extra).is_err(),
            "an unknown field was ignored, so a question could be asked and never delivered"
        );
    }
}
