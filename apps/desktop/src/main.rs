//! Native window for Vesqira. The web app and the API stay exactly as in the browser (ADR 0037);
//! this shell only starts the local server through the `vesqira` launcher, shows its address in a
//! window, and stops the server on exit if it was the one that started it.
use std::process::Command;
use tauri::{Manager, WebviewUrl, WebviewWindowBuilder};

fn port() -> String {
    std::env::var("VESQIRA_PORT").unwrap_or_else(|_| "8787".into())
}

fn launcher() -> String {
    std::env::var("VESQIRA_LAUNCHER").unwrap_or_else(|_| "vesqira".into())
}

fn run(args: &[&str]) -> bool {
    Command::new(launcher()).args(args).status().map(|s| s.success()).unwrap_or(false)
}

/// True when the launcher reports a running server before we touch it.
fn already_running() -> bool {
    Command::new(launcher()).arg("status").output().map(|o| String::from_utf8_lossy(&o.stderr).contains("Running")).unwrap_or(false)
}

struct Started(bool);

/// The system folder chooser. The page can ask for a path and nothing else: the server still decides what that path
/// may be (ADR 0037), so this command grants no access by itself.
#[tauri::command]
async fn pick_folder(app: tauri::AppHandle) -> Option<String> {
    use tauri_plugin_dialog::DialogExt;
    app.dialog().file().set_title("Open project folder").blocking_pick_folder().and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().into_owned())
}

fn main() {
    let was_running = already_running();
    if !was_running && !run(&["start"]) {
        eprintln!("vesqira: the local server did not start; see `vesqira logs`");
        std::process::exit(1);
    }
    let url = format!("http://127.0.0.1:{}/", port());
    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .invoke_handler(tauri::generate_handler![pick_folder])
        .manage(Started(!was_running))
        .setup(move |app| {
            let target = url.parse().expect("loopback url");
            WebviewWindowBuilder::new(app, "main", WebviewUrl::External(target))
                .title("Vesqira")
                .inner_size(1440.0, 900.0)
                .min_inner_size(900.0, 600.0)
                .build()?;
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("could not create the window")
        .run(|app, event| {
            if let tauri::RunEvent::Exit = event {
                if app.state::<Started>().0 {
                    run(&["stop"]);
                }
            }
        });
}
