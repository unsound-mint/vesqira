fn main() {
    // A page served from the loopback address is a remote origin to Tauri, and remote origins may call app commands only
    // when the command is declared here and allowed in capabilities/default.json.
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(tauri_build::AppManifest::new().commands(&["pick_folder"])))
        .expect("tauri build");
}
