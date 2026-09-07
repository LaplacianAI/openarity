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
#[tauri::command]
async fn install(app: AppHandle, root: String, objects: String, secrets: String, gateway: String) -> Result<(), String> {
    let mut args = vec![
        "stack".to_string(),
        "setup".to_string(),
        "--json".to_string(),
    ];

    if !root.is_empty() {
        args.push("--root".to_string());
        args.push(root);
    }

    let _ = (&objects, &secrets, &gateway);

    let (mut rx, _child) = app
        .shell()
        .sidecar("oa")
        .map_err(|e| e.to_string())?
        .args(args)
        .spawn()
        .map_err(|e| e.to_string())?;

    while let Some(event) = rx.recv().await {
        match event {
            CommandEvent::Stdout(bytes) => {
                let line = String::from_utf8_lossy(&bytes).to_string();
                let _ = app.emit("install", Line { line });
            }
            // The passphrase and the address are on stdout as an event; stderr
            // carries the prose meant for a terminal, which this window has no
            // use for beyond a failure.
            CommandEvent::Stderr(bytes) => {
                let line = String::from_utf8_lossy(&bytes).to_string();
                let _ = app.emit("install-log", Line { line });
            }
            CommandEvent::Terminated(payload) => {
                if payload.code != Some(0) {
                    return Err(format!("setup exited with {:?}", payload.code));
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
