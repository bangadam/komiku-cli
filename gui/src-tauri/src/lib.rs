use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStderr, ChildStdout, Command, ExitStatus, Stdio};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{Duration, SystemTime};

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, State};

#[derive(Serialize, Clone)]
pub struct CliOutput {
    ok: bool,
    code: Option<i32>,
    stdout: String,
    stderr: String,
}

#[derive(Serialize, Clone)]
pub struct CliContext {
    binary: String,
    cwd: String,
}

#[derive(Serialize, Clone)]
pub struct CliLine {
    run_id: String,
    stream: String,
    line: String,
}

#[derive(Clone, Default)]
pub struct RunState {
    children: Arc<Mutex<HashMap<String, RunningChild>>>,
}

struct RunningChild {
    child: Child,
    cancelled: bool,
}

impl Drop for RunningChild {
    fn drop(&mut self) {
        // Only this bridge's child is ever signalled; always reap it too.
        if !matches!(self.child.try_wait(), Ok(Some(_))) {
            let _ = self.child.kill();
            let _ = self.child.wait();
        }
    }
}

fn validate_args(args: &[String]) -> Result<(), String> {
    if args.len() > 128
        || args
            .iter()
            .any(|arg| arg.len() > 8192 || arg.contains('\0'))
        || args.iter().map(String::len).sum::<usize>() > 64 * 1024
    {
        return Err(
            "invalid CLI arguments (128 arguments, 8 KiB each, 64 KiB total maximum)".into(),
        );
    }
    Ok(())
}

fn validate_run_id(run_id: &str) -> Result<(), String> {
    if run_id.is_empty()
        || run_id.len() > 128
        || !run_id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || b"_-".contains(&c))
    {
        return Err("invalid runId".into());
    }
    Ok(())
}

impl RunState {
    fn start(
        &self,
        run_id: &str,
        command: &mut Command,
    ) -> Result<(ChildStdout, ChildStderr), String> {
        validate_run_id(run_id)?;
        let mut children = self.children.lock().map_err(|err| err.to_string())?;
        if children.contains_key(run_id) {
            return Err("runId is already active".into());
        }
        let mut running = RunningChild {
            child: command
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .map_err(|err| format!("cannot start CLI: {err}"))?,
            cancelled: false,
        };
        let stdout = running.child.stdout.take().ok_or("missing stdout pipe")?;
        let stderr = running.child.stderr.take().ok_or("missing stderr pipe")?;
        children.insert(run_id.to_owned(), running);
        Ok((stdout, stderr))
    }

    fn cancel(&self, run_id: &str) -> Result<bool, String> {
        validate_run_id(run_id)?;
        let mut children = self.children.lock().map_err(|err| err.to_string())?;
        let Some(running) = children.get_mut(run_id) else {
            return Ok(false);
        };
        if running.cancelled
            || running
                .child
                .try_wait()
                .map_err(|err| err.to_string())?
                .is_some()
        {
            return Ok(false);
        }
        running.child.kill().map_err(|err| err.to_string())?;
        running.cancelled = true;
        // The waiter owns removal/reaping, so cancellation cannot lose its handle.
        Ok(true)
    }

    fn wait(&self, run_id: &str) -> Result<(ExitStatus, bool), String> {
        loop {
            {
                let mut children = self.children.lock().map_err(|err| err.to_string())?;
                let running = children.get_mut(run_id).ok_or("process handle lost")?;
                match running.child.try_wait() {
                    Ok(Some(status)) => {
                        let cancelled = running.cancelled;
                        children.remove(run_id);
                        return Ok((status, cancelled));
                    }
                    Ok(None) => {}
                    Err(err) => {
                        children.remove(run_id);
                        return Err(format!("cannot wait for CLI: {err}"));
                    }
                }
            }
            // Never hold the map lock while waiting: unrelated runs and cancel
            // must stay responsive even during a long download.
            std::thread::sleep(Duration::from_millis(15));
        }
    }
}

fn collect_lines(reader: impl Read, mut emit: impl FnMut(String)) -> Result<String, String> {
    let mut reader = BufReader::new(reader);
    let mut collected = Vec::new();
    let mut line = Vec::new();
    loop {
        line.clear();
        if reader
            .read_until(b'\n', &mut line)
            .map_err(|err| err.to_string())?
            == 0
        {
            break;
        }
        collected.extend_from_slice(&line);
        if line.last() == Some(&b'\n') {
            line.pop();
            if line.last() == Some(&b'\r') {
                line.pop();
            }
        }
        emit(String::from_utf8_lossy(&line).into_owned());
    }
    Ok(String::from_utf8_lossy(&collected).into_owned())
}

fn resolve_binary() -> String {
    if let Ok(path) = std::env::var("KOMIKU_CLI_BIN") {
        if std::path::Path::new(&path).is_file() {
            return path;
        }
    }

    if let Ok(exe) = std::env::current_exe() {
        let mut dir = exe.parent().map(|p| p.to_path_buf());
        for _ in 0..6 {
            let Some(current) = dir.clone() else { break };
            for candidate in ["komiku-cli", "bin/komiku-cli"] {
                let path = current.join(candidate);
                if path.is_file() {
                    return path.to_string_lossy().to_string();
                }
            }
            dir = current.parent().map(|p| p.to_path_buf());
        }
    }

    for candidate in ["komiku-cli", "../komiku-cli", "../../komiku-cli"] {
        if std::path::Path::new(candidate).is_file() {
            return candidate.to_string();
        }
    }

    "komiku-cli".to_string()
}

/// Working directory used for CLI runs. `output_root: "."` in the CLI config
/// is relative, so the GUI must always run from a predictable place. We use
/// the directory that holds the binary (the repo root in development), which
/// matches where the CLI is normally invoked.
fn resolve_cwd(binary: &str) -> std::path::PathBuf {
    if let Ok(dir) = std::env::var("KOMIKU_CLI_CWD") {
        let path = std::path::PathBuf::from(dir);
        if path.is_dir() {
            return path;
        }
    }
    let path = std::path::Path::new(binary);
    if path.is_absolute() {
        if let Some(parent) = path.parent() {
            if parent.is_dir() {
                return parent.to_path_buf();
            }
        }
    }
    std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."))
}

/// Optional call tracing: when KOMIKU_CLI_TRACE is set, every invocation is
/// appended to that file. Useful to confirm the GUI really drives the binary.
fn trace_call(binary: &str, cwd: &std::path::Path, args: &[String]) {
    let Ok(path) = std::env::var("KOMIKU_CLI_TRACE") else {
        return;
    };
    let line = format!(
        "{} cwd={} {} {}\n",
        binary,
        cwd.display(),
        args.join(" "),
        ""
    );
    use std::io::Write;
    if let Ok(mut file) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    {
        let _ = file.write_all(line.as_bytes());
    }
}

#[tauri::command]
fn cli_context() -> CliContext {
    let binary = resolve_binary();
    let cwd = resolve_cwd(&binary);
    CliContext {
        binary,
        cwd: cwd.to_string_lossy().to_string(),
    }
}

#[tauri::command]
async fn run_cli(args: Vec<String>) -> Result<CliOutput, String> {
    validate_args(&args)?;
    tauri::async_runtime::spawn_blocking(move || {
        let binary = resolve_binary();
        let cwd = resolve_cwd(&binary);
        trace_call(&binary, &cwd, &args);
        let output = Command::new(&binary)
            .args(&args)
            .current_dir(&cwd)
            .output()
            .map_err(|err| format!("cannot run {binary} in {}: {err}", cwd.display()))?;
        Ok(CliOutput {
            ok: output.status.success(),
            code: output.status.code(),
            stdout: String::from_utf8_lossy(&output.stdout).to_string(),
            stderr: String::from_utf8_lossy(&output.stderr).to_string(),
        })
    })
    .await
    .map_err(|err| err.to_string())?
}

#[tauri::command]
async fn run_cli_stream(
    app: AppHandle,
    state: State<'_, RunState>,
    run_id: String,
    args: Vec<String>,
) -> Result<CliOutput, String> {
    validate_args(&args)?;
    let binary = resolve_binary();
    let cwd = resolve_cwd(&binary);
    trace_call(&binary, &cwd, &args);
    // Register before yielding, so an immediately following cancel can find it.
    let (stdout, stderr) =
        state.start(&run_id, Command::new(&binary).args(&args).current_dir(&cwd))?;
    let runs = state.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let out_handle = app.clone();
        let out_id = run_id.clone();
        let out_thread = std::thread::spawn(move || {
            collect_lines(stdout, |line| {
                let _ = out_handle.emit(
                    "cli://line",
                    CliLine {
                        run_id: out_id.clone(),
                        stream: "stdout".into(),
                        line,
                    },
                );
            })
        });
        let err_id = run_id.clone();
        let err_thread = std::thread::spawn(move || {
            collect_lines(stderr, |line| {
                let _ = app.emit(
                    "cli://line",
                    CliLine {
                        run_id: err_id.clone(),
                        stream: "stderr".into(),
                        line,
                    },
                );
            })
        });
        let status = runs.wait(&run_id);
        // Join both readers on every exit, including wait failures.
        let stdout = out_thread
            .join()
            .map_err(|_| "stdout reader panicked".to_string());
        let stderr = err_thread
            .join()
            .map_err(|_| "stderr reader panicked".to_string());
        let (status, cancelled) = status?;
        let stdout = stdout??;
        let mut stderr = stderr??;
        if cancelled {
            if !stderr.is_empty() && !stderr.ends_with('\n') {
                stderr.push('\n');
            }
            stderr.push_str("CLI run cancelled\n");
        }
        Ok(CliOutput {
            ok: !cancelled && status.success(),
            code: if cancelled { None } else { status.code() },
            stdout,
            stderr,
        })
    })
    .await
    .map_err(|err| err.to_string())?
}

#[tauri::command]
fn open_config_file(path: String) -> Result<(), String> {
    let target = std::path::PathBuf::from(&path);
    if target.is_file() {
        open_in_editor(&target)
    } else if let Some(parent) = target.parent() {
        open_in_editor(parent)
    } else {
        Err(format!("config file not found: {path}"))
    }
}

/// Reveal a folder in the OS file manager. Used by the Series "Open folder"
/// action, so the button does what it says instead of showing a hint.
#[tauri::command]
fn open_folder(path: String) -> Result<(), String> {
    let (root, cwd) = library_context()?;
    let target = confined_path(&root, &cwd, &path)
        .filter(|target| target.is_dir())
        .ok_or("folder is not inside the configured library")?;
    let mut command = if cfg!(target_os = "macos") {
        let mut command = Command::new("open");
        command.arg(&target);
        command
    } else if cfg!(target_os = "windows") {
        let mut command = Command::new("explorer");
        command.arg(&target);
        command
    } else {
        let mut command = Command::new("xdg-open");
        command.arg(&target);
        command
    };
    let status = command
        .status()
        .map_err(|err| format!("cannot open {}: {err}", target.display()))?;
    if status.success() {
        Ok(())
    } else {
        Err(format!(
            "cannot open {}: exited with {status}",
            target.display()
        ))
    }
}

fn open_in_editor(target: &std::path::Path) -> Result<(), String> {
    let mut command = if cfg!(target_os = "macos") {
        let mut command = Command::new("open");
        if target.is_file() {
            command.arg("-t");
        }
        command.arg(target);
        command
    } else if cfg!(target_os = "windows") {
        let mut command = Command::new("cmd");
        command.args(["/C", "start", ""]).arg(target);
        command
    } else {
        let mut command = Command::new("xdg-open");
        command.arg(target);
        command
    };

    let status = command
        .status()
        .map_err(|err| format!("cannot open {}: {err}", target.display()))?;
    if status.success() {
        Ok(())
    } else {
        Err(format!(
            "cannot open {}: exited with {status}",
            target.display()
        ))
    }
}

const MAX_PAGE_BYTES: u64 = 24 * 1024 * 1024;
const MAX_COVER_BYTES: u64 = 8 * 1024 * 1024;

#[derive(Deserialize)]
struct LibraryConfig {
    output_root: String,
    config_path: String,
}

struct CachedLibrary {
    binary: String,
    cwd: PathBuf,
    root: PathBuf,
    config_path: PathBuf,
    stamp: Option<(SystemTime, u64)>,
}

fn config_stamp(path: &Path) -> Option<(SystemTime, u64)> {
    let meta = std::fs::metadata(path).ok()?;
    Some((meta.modified().ok()?, meta.len()))
}

/// Ask the actual CLI, with exactly the same binary/cwd/env as ordinary runs.
/// Watch its config file rather than spawning a config process for every page.
/// Never fall back to cwd (or accept a root supplied by a web request).
fn library_context() -> Result<(PathBuf, PathBuf), String> {
    static CACHE: OnceLock<Mutex<Option<CachedLibrary>>> = OnceLock::new();
    let binary = resolve_binary();
    let cwd = resolve_cwd(&binary)
        .canonicalize()
        .map_err(|err| err.to_string())?;
    let mut cache = CACHE
        .get_or_init(|| Mutex::new(None))
        .lock()
        .map_err(|err| err.to_string())?;
    let fresh = cache.as_ref().is_some_and(|entry| {
        entry.binary == binary
            && entry.cwd == cwd
            && entry.stamp == config_stamp(&entry.config_path)
    });
    if !fresh {
        let output = Command::new(&binary)
            .args(["config", "--json"])
            .current_dir(&cwd)
            .output()
            .map_err(|err| format!("cannot read CLI config: {err}"))?;
        if !output.status.success() {
            return Err("cannot resolve configured library".into());
        }
        let config: LibraryConfig = serde_json::from_slice(&output.stdout)
            .map_err(|err| format!("invalid CLI config: {err}"))?;
        if config.output_root.trim().is_empty() || config.config_path.is_empty() {
            return Err("CLI config has no library root/config path".into());
        }
        let config_path = cwd.join(&config.config_path);
        *cache = Some(CachedLibrary {
            binary,
            cwd: cwd.clone(),
            root: cwd.join(&config.output_root),
            stamp: config_stamp(&config_path),
            config_path,
        });
    }
    let root = cache
        .as_ref()
        .ok_or("missing library config")?
        .root
        .canonicalize()
        .map_err(|err| format!("cannot resolve library: {err}"))?;
    if !root.is_dir() {
        return Err("configured library is not a directory".into());
    }
    Ok((root, cwd))
}

fn confined_path(root: &Path, cwd: &Path, path: &str) -> Option<PathBuf> {
    if path.is_empty() || path.len() > 4096 || path.contains('\0') {
        return None;
    }
    let target = cwd.join(path).canonicalize().ok()?;
    target.starts_with(root).then_some(target)
}

fn digits(value: &str) -> bool {
    !value.is_empty() && value.bytes().all(|c| c.is_ascii_digit())
}

fn chapter_dir(name: &str) -> bool {
    let Some(number) = name.strip_prefix("chapter-") else {
        return false;
    };
    let number = if let Some((number, raw)) = number.split_once("-raw-") {
        if raw.is_empty()
            || !raw
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || b"._-".contains(&c))
        {
            return false;
        }
        number
    } else {
        number
    };
    let number = if let Some((number, hash)) = number.split_once("-url-") {
        if !name.contains("-raw-") || !hex_hash(hash) {
            return false;
        }
        number
    } else {
        number
    };
    let mut parts = number.split(['.', '-']);
    digits(parts.next().unwrap_or_default())
        && parts.next().is_none_or(digits)
        && parts.next().is_none()
}

fn hex_hash(value: &str) -> bool {
    (8..=64).contains(&value.len()) && value.bytes().all(|c| c.is_ascii_hexdigit())
}

fn page_stem(stem: &str) -> bool {
    let index = if let Some((index, hash)) = stem.split_once('-') {
        if !hex_hash(hash) {
            return false;
        }
        index
    } else {
        stem
    };
    digits(index) && index.bytes().any(|c| c != b'0')
}

#[derive(Clone, Copy)]
enum ImageKind {
    Page,
    Cover,
}

fn allowed_image_path(root: &Path, target: &Path, kind: ImageKind) -> bool {
    let Ok(relative) = target.strip_prefix(root) else {
        return false;
    };
    let Some(parts) = relative
        .iter()
        .map(|s| s.to_str())
        .collect::<Option<Vec<_>>>()
    else {
        return false;
    };
    let Some(series) = parts.first() else {
        return false;
    };
    if series.starts_with('.') {
        return false;
    }
    let extension = target
        .extension()
        .and_then(|s| s.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();
    if !matches!(extension.as_str(), "jpg" | "jpeg" | "png" | "webp") {
        return false;
    }
    let stem = target
        .file_stem()
        .and_then(|s| s.to_str())
        .unwrap_or_default();
    match kind {
        ImageKind::Cover => parts.len() == 2 && stem.eq_ignore_ascii_case("cover"),
        ImageKind::Page => {
            if !page_stem(stem) {
                return false;
            }
            match parts.as_slice() {
                [_, chapter, _] => chapter_dir(chapter),
                [_, container, chapter, _] => {
                    (*container == ".reader" || container.strip_prefix("vol-").is_some_and(digits))
                        && chapter_dir(chapter)
                }
                _ => false,
            }
        }
    }
}

fn image_mime(bytes: &[u8]) -> Option<&'static str> {
    if bytes.starts_with(b"\xff\xd8\xff") {
        Some("image/jpeg")
    } else if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        Some("image/png")
    } else if bytes.len() >= 12 && &bytes[..4] == b"RIFF" && &bytes[8..12] == b"WEBP" {
        Some("image/webp")
    } else {
        None
    }
}

fn read_library_image(
    root: &Path,
    cwd: &Path,
    path: &str,
    kind: ImageKind,
) -> Result<Option<String>, String> {
    let Some(target) = confined_path(root, cwd, path) else {
        return Ok(None);
    };
    if !allowed_image_path(root, &target, kind) {
        return Ok(None);
    }
    let limit = match kind {
        ImageKind::Page => MAX_PAGE_BYTES,
        ImageKind::Cover => MAX_COVER_BYTES,
    };
    let meta = match std::fs::metadata(&target) {
        Ok(meta) => meta,
        Err(_) => return Ok(None),
    };
    if !meta.is_file() || meta.len() == 0 || meta.len() > limit {
        return Ok(None);
    }
    let file = std::fs::File::open(&target).map_err(|err| format!("read image: {err}"))?;
    let meta = file.metadata().map_err(|err| err.to_string())?;
    if !meta.is_file()
        || meta.len() == 0
        || meta.len() > limit
        || target.canonicalize().ok().as_ref() != Some(&target)
    {
        return Ok(None);
    }
    // A file may grow after stat: bound the actual read too.
    let mut bytes = Vec::with_capacity(meta.len() as usize);
    file.take(limit + 1)
        .read_to_end(&mut bytes)
        .map_err(|err| format!("read image: {err}"))?;
    if bytes.len() as u64 > limit {
        return Ok(None);
    }
    let Some(mime) = image_mime(&bytes) else {
        return Ok(None);
    };
    Ok(Some(format!(
        "data:{mime};base64,{}",
        base64_encode(&bytes)
    )))
}

/// Only numeric pages in the configured library's flat, volume or reader-cache
/// chapter layout are readable; both the root and target are canonicalized.
#[tauri::command]
fn read_page(path: String) -> Result<Option<String>, String> {
    let (root, cwd) = library_context()?;
    read_library_image(&root, &cwd, &path, ImageKind::Page)
}

#[tauri::command]
fn read_cover(path: String) -> Result<Option<String>, String> {
    let (root, cwd) = library_context()?;
    read_library_image(&root, &cwd, &path, ImageKind::Cover)
}

/// Minimal standard base64 encoder; avoids pulling a crate in for one use.
fn base64_encode(input: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
    for chunk in input.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = *chunk.get(1).unwrap_or(&0) as u32;
        let b2 = *chunk.get(2).unwrap_or(&0) as u32;
        let triple = (b0 << 16) | (b1 << 8) | b2;
        out.push(ALPHABET[(triple >> 18) as usize & 0x3f] as char);
        out.push(ALPHABET[(triple >> 12) as usize & 0x3f] as char);
        out.push(if chunk.len() > 1 {
            ALPHABET[(triple >> 6) as usize & 0x3f] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            ALPHABET[triple as usize & 0x3f] as char
        } else {
            '='
        });
    }
    out
}

#[tauri::command]
fn cancel_cli(state: State<'_, RunState>, run_id: String) -> Result<bool, String> {
    state.cancel(&run_id)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .manage(RunState::default())
        .invoke_handler(tauri::generate_handler![
            cli_context,
            run_cli,
            run_cli_stream,
            open_config_file,
            read_cover,
            read_page,
            open_folder,
            cancel_cli
        ])
        .setup(|app| {
            if cfg!(debug_assertions) {
                app.handle().plugin(
                    tauri_plugin_log::Builder::default()
                        .level(log::LevelFilter::Info)
                        .build(),
                )?;
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while building tauri application");
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct Fixture(PathBuf);
    impl Fixture {
        fn new() -> Self {
            static NEXT: AtomicUsize = AtomicUsize::new(0);
            let dir = std::env::temp_dir().join(format!(
                "komiku-bridge-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            std::fs::create_dir_all(&dir).unwrap();
            Self(dir.canonicalize().unwrap())
        }
        fn write(&self, relative: &str, bytes: &[u8]) -> PathBuf {
            let target = self.0.join(relative);
            std::fs::create_dir_all(target.parent().unwrap()).unwrap();
            std::fs::write(&target, bytes).unwrap();
            target
        }
        fn read(&self, relative: &str, kind: ImageKind) -> Option<String> {
            read_library_image(&self.0, &self.0, relative, kind).unwrap()
        }
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    const PNG: &[u8] = b"\x89PNG\r\n\x1a\n";

    #[test]
    fn base64_matches_rfc4648_vectors() {
        for (bytes, expected) in [
            ("", ""),
            ("f", "Zg=="),
            ("fo", "Zm8="),
            ("foo", "Zm9v"),
            ("foob", "Zm9vYg=="),
            ("fooba", "Zm9vYmE="),
            ("foobar", "Zm9vYmFy"),
        ] {
            assert_eq!(base64_encode(bytes.as_bytes()), expected);
        }
    }

    #[test]
    fn reads_flat_volume_and_cache_pages_and_sniffs_misleading_extension() {
        let fixture = Fixture::new();
        for relative in [
            "series/chapter-001/001.jpg",
            "series/vol-01/chapter-000.5/002.png",
            "series/.reader/chapter-012/003.webp",
            "series/chapter-002-raw-02/004.jpeg",
            "series/.reader/chapter-003-raw-003/001-8f18a8e9fdc699e4cd76ec20.png",
            "series/.reader/chapter-003-url-8f18a8e9fdc699e4cd76ec20-raw-003/001-8F18A8E9FDC699E4CD76EC20.jpg",
        ] {
            fixture.write(relative, PNG);
            assert!(
                fixture
                    .read(relative, ImageKind::Page)
                    .unwrap()
                    .starts_with("data:image/png;base64,")
            );
        }
        fixture.write("series/cover.jpg", b"RIFF\0\0\0\0WEBP");
        assert!(
            fixture
                .read("series/cover.jpg", ImageKind::Cover)
                .unwrap()
                .starts_with("data:image/webp;base64,")
        );
        assert_eq!(image_mime(b"\xff\xd8\xff\xe0"), Some("image/jpeg"));
        assert_eq!(image_mime(b"RIFFinvalid!"), None);
    }

    #[test]
    fn denies_non_numeric_wrong_layout_non_images_and_large_files() {
        let fixture = Fixture::new();
        for relative in [
            "series/chapter-001/secret.png",
            "series/chapter-001/000.png",
            "chapter-001/001.png",
            "series/.reader/001.png",
            "series/other/chapter-001/001.png",
            "series/chapter-not-a-number/001.png",
            "series/chapter-001/001.txt",
            "series/chapter-001/001.backup.png",
            "series/chapter-001/001-not-a-hash.png",
            "series/chapter-001/000-8f18a8e9.png",
            "series/.reader/chapter-003-url-invalid-raw-003/001.png",
            "series/.reader/chapter-003-url-8f18a8e9/001.png",
            ".hidden/chapter-001/001.png",
        ] {
            fixture.write(relative, PNG);
            assert!(
                fixture.read(relative, ImageKind::Page).is_none(),
                "{relative}"
            );
        }
        fixture.write("series/chapter-001/001.png", b"<html>not an image</html>");
        assert!(
            fixture
                .read("series/chapter-001/001.png", ImageKind::Page)
                .is_none()
        );
        fixture.write("series/chapter-001/002.png", b"");
        assert!(
            fixture
                .read("series/chapter-001/002.png", ImageKind::Page)
                .is_none()
        );
        let large = fixture.write("series/chapter-001/003.png", PNG);
        std::fs::OpenOptions::new()
            .write(true)
            .open(large)
            .unwrap()
            .set_len(MAX_PAGE_BYTES + 1)
            .unwrap();
        assert!(
            fixture
                .read("series/chapter-001/003.png", ImageKind::Page)
                .is_none()
        );
        let cover = fixture.write("series/cover.png", PNG);
        std::fs::OpenOptions::new()
            .write(true)
            .open(cover)
            .unwrap()
            .set_len(MAX_COVER_BYTES + 1)
            .unwrap();
        assert!(fixture.read("series/cover.png", ImageKind::Cover).is_none());
    }

    #[test]
    fn denies_outside_root_and_traversal_even_with_valid_names() {
        let fixture = Fixture::new();
        let outside = Fixture::new();
        let target = outside.write("series/chapter-001/001.png", PNG);
        assert!(
            fixture
                .read(target.to_str().unwrap(), ImageKind::Page)
                .is_none()
        );
        let traversal = format!(
            "../{}/series/chapter-001/001.png",
            outside.0.file_name().unwrap().to_str().unwrap()
        );
        assert!(fixture.read(&traversal, ImageKind::Page).is_none());
        let target = outside.write("series/cover.png", PNG);
        assert!(
            fixture
                .read(target.to_str().unwrap(), ImageKind::Cover)
                .is_none()
        );
    }

    #[cfg(unix)]
    #[test]
    fn denies_file_directory_and_cache_symlink_escapes() {
        use std::os::unix::fs::symlink;
        let fixture = Fixture::new();
        let outside = Fixture::new();
        let page = outside.write("series/chapter-001/001.png", PNG);
        fixture.write("series/chapter-001/002.png", PNG);
        symlink(&page, fixture.0.join("series/chapter-001/001.png")).unwrap();
        assert!(
            fixture
                .read("series/chapter-001/001.png", ImageKind::Page)
                .is_none()
        );
        symlink(
            outside.0.join("series/chapter-001"),
            fixture.0.join("series/chapter-002"),
        )
        .unwrap();
        assert!(
            fixture
                .read("series/chapter-002/001.png", ImageKind::Page)
                .is_none()
        );
        symlink(outside.0.join("series"), fixture.0.join("series/.reader")).unwrap();
        assert!(
            fixture
                .read("series/.reader/chapter-001/001.png", ImageKind::Page)
                .is_none()
        );
    }

    #[test]
    fn line_collector_preserves_full_output_and_final_remainder() {
        let input = "{\"page\":1}\r\n\n日本語 without newline";
        let mut lines = Vec::new();
        assert_eq!(
            collect_lines(input.as_bytes(), |line| lines.push(line)).unwrap(),
            input
        );
        assert_eq!(lines, ["{\"page\":1}", "", "日本語 without newline"]);
    }

    #[test]
    fn validates_args_and_ids_before_spawn() {
        assert!(validate_args(&["read".into(), "series-url".into()]).is_ok());
        assert!(validate_args(&["bad\0arg".into()]).is_err());
        assert!(validate_args(&["x".repeat(8193)]).is_err());
        assert!(validate_args(&vec!["x".into(); 129]).is_err());
        assert!(validate_run_id("run-reader_1").is_ok());
        assert!(validate_run_id("").is_err());
        assert!(validate_run_id("../download").is_err());
        let runs = RunState::default();
        assert!(
            runs.start("spawn-error", &mut Command::new("/no/such/komiku-binary"))
                .is_err()
        );
        assert!(runs.children.lock().unwrap().is_empty());
    }

    #[cfg(unix)]
    #[test]
    fn concurrent_runs_cancel_only_matching_id_without_wait_lock() {
        let runs = RunState::default();
        let (out_a, err_a) = runs
            .start(
                "download",
                Command::new("/bin/sh").args(["-c", "exec sleep 30"]),
            )
            .unwrap();
        let (out_b, err_b) = runs
            .start(
                "reader",
                Command::new("/bin/sh").args(["-c", "printf 'ready\\n'; exec sleep 30"]),
            )
            .unwrap();
        assert!(
            runs.start("download", &mut Command::new("/bin/true"))
                .is_err()
        );
        let waiter = runs.clone();
        let (tx, rx) = std::sync::mpsc::channel();
        let thread = std::thread::spawn(move || {
            tx.send(waiter.wait("download")).unwrap();
        });
        std::thread::sleep(Duration::from_millis(30));
        assert!(!runs.cancel("unknown").unwrap());
        assert!(runs.cancel("download").unwrap());
        let (status, cancelled) = rx.recv_timeout(Duration::from_secs(3)).unwrap().unwrap();
        assert!(cancelled && !status.success());
        assert!(
            runs.children
                .lock()
                .unwrap()
                .get_mut("reader")
                .unwrap()
                .child
                .try_wait()
                .unwrap()
                .is_none()
        );
        assert!(runs.cancel("reader").unwrap());
        assert!(runs.wait("reader").unwrap().1);
        assert!(runs.children.lock().unwrap().is_empty());
        assert!(!runs.cancel("download").unwrap());
        thread.join().unwrap();
        drop((out_a, err_a, out_b, err_b));
    }

    #[cfg(unix)]
    #[test]
    fn normal_exit_is_reaped_and_reports_status() {
        let runs = RunState::default();
        let (out, err) = runs
            .start(
                "exit",
                Command::new("/bin/sh").args(["-c", "printf report; exit 7"]),
            )
            .unwrap();
        let (status, cancelled) = runs.wait("exit").unwrap();
        assert_eq!(status.code(), Some(7));
        assert!(!cancelled);
        assert_eq!(collect_lines(out, |_| {}).unwrap(), "report");
        assert!(collect_lines(err, |_| {}).unwrap().is_empty());
        assert!(runs.children.lock().unwrap().is_empty());
    }
}
