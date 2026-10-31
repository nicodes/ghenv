use super::policy::names;
use super::{MAX_PAYLOAD, PrivateKey, Request, Result, Values, unseal};
use aws_lc_rs::rand::{SecureRandom, SystemRandom};
use serde_json::{Value, json};
use std::{
    io::{Cursor, Read, Write},
    process::{Command, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

#[derive(Clone)]
pub struct Context {
    deadline: Instant,
    cancelled: Arc<AtomicBool>,
}
impl Context {
    pub fn new(timeout: Duration) -> Result<Self> {
        Ok(Self {
            deadline: Instant::now()
                .checked_add(timeout)
                .ok_or("timeout is too large")?,
            cancelled: Arc::new(AtomicBool::new(false)),
        })
    }
    pub fn cancel(&self) {
        self.cancelled.store(true, Ordering::SeqCst);
    }
    pub fn cancellation_flag(&self) -> Arc<AtomicBool> {
        self.cancelled.clone()
    }
    pub fn check(&self) -> Result<()> {
        if self.cancelled.load(Ordering::SeqCst) {
            return Err("context canceled".into());
        }
        if Instant::now() >= self.deadline {
            return Err("context deadline exceeded".into());
        }
        Ok(())
    }
    fn pause(&self, duration: Duration) -> Result<()> {
        let until = Instant::now()
            .checked_add(duration)
            .ok_or("timeout is too large")?
            .min(self.deadline);
        loop {
            self.check()?;
            let remaining = until.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return Ok(());
            }
            thread::sleep(remaining.min(Duration::from_millis(20)));
        }
    }
}
pub(super) fn command(
    context: &Context,
    args: &[&str],
    input: Option<Vec<u8>>,
) -> Result<(bool, Vec<u8>, Vec<u8>)> {
    context.check()?;
    let mut command = Command::new("gh");
    command
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    let mut child = command.spawn().map_err(|error| {
        if error.kind() == std::io::ErrorKind::NotFound {
            "github_cli_missing: install GitHub CLI (gh), then run gh auth login"
        } else {
            "github_request_failed: could not start GitHub CLI"
        }
    })?;
    #[cfg(unix)]
    let group = child.id();
    let overflow = Arc::new(AtomicBool::new(false));
    let read = |reader: Box<dyn Read + Send>, overflow: Arc<AtomicBool>| {
        thread::spawn(move || {
            let mut bytes = Vec::new();
            let result = reader
                .take((MAX_PAYLOAD * 2 + 1) as u64)
                .read_to_end(&mut bytes);
            if bytes.len() > MAX_PAYLOAD * 2 {
                overflow.store(true, Ordering::SeqCst);
            }
            result.map(|_| bytes)
        })
    };
    let stdout = read(
        Box::new(child.stdout.take().expect("child stdout")),
        overflow.clone(),
    );
    let stderr = read(
        Box::new(child.stderr.take().expect("child stderr")),
        overflow.clone(),
    );
    let mut stdin = child.stdin.take().expect("child stdin");
    let writer = thread::spawn(move || {
        if let Some(input) = input {
            stdin.write_all(&input)
        } else {
            Ok(())
        }
    });
    let status = loop {
        if let Err(error) = context.check() {
            break Err(error);
        }
        if overflow.load(Ordering::SeqCst) {
            break Err("GitHub response exceeds size limit".into());
        }
        match child.try_wait() {
            Ok(Some(status)) => break Ok(status),
            Ok(None) => thread::sleep(Duration::from_millis(10)),
            Err(_) => break Err("github_request_failed: could not wait for GitHub CLI".into()),
        }
    };
    // Readers must not remain blocked on a descendant's inherited pipe after
    // cancellation or exit. This group belongs only to this invocation of gh.
    #[cfg(unix)]
    unsafe {
        libc::kill(-(group as i32), libc::SIGKILL);
    }
    if status.is_err() {
        let _ = child.kill();
        let _ = child.wait();
    }
    let output = stdout
        .join()
        .map_err(|_| "GitHub output reader failed")?
        .map_err(|_| "GitHub output read failed")?;
    let diagnostic = stderr
        .join()
        .map_err(|_| "GitHub diagnostic reader failed")?
        .map_err(|_| "GitHub diagnostic read failed")?;
    let _ = writer.join();
    let status = status?;
    if output.len() > MAX_PAYLOAD * 2 || diagnostic.len() > MAX_PAYLOAD * 2 {
        return Err("GitHub response exceeds size limit".into());
    }
    Ok((status.success(), output, diagnostic))
}
fn api(context: &Context, method: &str, path: &str, body: Option<Value>) -> Result<Vec<u8>> {
    let mut args = vec![
        "api",
        "--hostname",
        "github.com",
        "--method",
        method,
        path,
        "-H",
        "Accept: application/vnd.github+json",
        "-H",
        "X-GitHub-Api-Version: 2022-11-28",
    ];
    let input = if let Some(body) = body {
        args.extend(["--input", "-"]);
        Some(serde_json::to_vec(&body)?)
    } else {
        None
    };
    let (success, output, diagnostic) = command(context, &args, input)?;
    if !success {
        let text = String::from_utf8_lossy(&diagnostic);
        let (code, message) = if text.contains("HTTP 401") || text.contains("gh auth login") {
            (
                "github_auth_required",
                "run gh auth login --hostname github.com",
            )
        } else if text.contains("HTTP 403") || text.contains("HTTP 429") {
            (
                "github_access_denied",
                "check Actions read/write permission, repository access and GitHub rate limits",
            )
        } else if text.contains("HTTP 404") {
            (
                "github_not_found",
                "check repository access and install .github/workflows/ghenv.yml on the default branch",
            )
        } else if text.contains("HTTP 422") {
            (
                "github_invalid_dispatch",
                "check the trusted workflow ref and ghenv workflow inputs",
            )
        } else {
            (
                "github_request_failed",
                "run ghenv doctor --repo OWNER/REPO to check setup",
            )
        };
        return Err(format!(
            "{code}: GitHub {method} {} failed; {message}",
            path.split('?').next().unwrap_or(path)
        )
        .into());
    }
    Ok(output)
}
pub fn check_workflow(context: &Context, repo: &str) -> Result<()> {
    Request {
        repo: repo.into(),
        environment: "Preview".into(),
        reference: "main".into(),
        secrets: String::new(),
        variables: "GHENV_PREFLIGHT".into(),
    }
    .validate()?;
    let output = api(
        context,
        "GET",
        &format!("repos/{repo}/actions/workflows/ghenv.yml"),
        None,
    )?;
    let value: Value = serde_json::from_slice(&output)
        .map_err(|_| "invalid_workflow_response: GitHub returned invalid workflow metadata")?;
    if value["state"] != "active" {
        return Err(
            "workflow_disabled: enable .github/workflows/ghenv.yml in GitHub Actions".into(),
        );
    }
    Ok(())
}
struct Artifact {
    path: String,
}
impl Drop for Artifact {
    fn drop(&mut self) {
        let deleted = Context::new(Duration::from_secs(10))
            .and_then(|context| api(&context, "DELETE", &self.path, None));
        if deleted.is_err() {
            eprintln!("ghenv: could not delete encrypted artifact; it expires after one day");
        }
    }
}
pub fn fetch(context: &Context, request: &Request) -> Result<Values> {
    request.validate()?;
    context.check()?;
    let selected = names(&request.secrets, &request.variables)?;
    let private = PrivateKey::generate()?;
    let mut random = [0_u8; 16];
    SystemRandom::new()
        .fill(&mut random)
        .map_err(|_| "cannot generate request ID")?;
    let id: String = random.iter().map(|byte| format!("{byte:02x}")).collect();
    let prefix = format!("repos/{}/actions", request.repo);
    let output = api(
        context,
        "POST",
        &format!("{prefix}/workflows/ghenv.yml/dispatches"),
        Some(
            json!({"ref":request.reference,"return_run_details":true,"inputs":{"environment":request.environment,"request_id":id,"public_key":private.public_key()?,"secret_names":request.secrets,"variable_names":request.variables}}),
        ),
    )?;
    let dispatch:Value=serde_json::from_slice(&output).map_err(|_|"GitHub did not return a workflow run ID; github.com workflow dispatch run details are required")?;
    let run=dispatch["workflow_run_id"].as_i64().filter(|id|*id!=0).ok_or("GitHub did not return a workflow run ID; github.com workflow dispatch run details are required")?;
    let run_path = format!("{prefix}/runs/{run}");
    eprintln!(
        "ghenv: waiting for https://github.com/{}/actions/runs/{run}",
        request.repo
    );
    let mut previous = String::new();
    loop {
        let output = api(context, "GET", &run_path, None)?;
        let value: Value =
            serde_json::from_slice(&output).map_err(|_| "invalid workflow run response")?;
        let status = value["status"]
            .as_str()
            .ok_or("invalid workflow run response")?;
        if status == "waiting" && previous != status {
            eprintln!(
                "ghenv: waiting for Preview environment approval; approve the linked workflow run"
            );
        }
        previous = status.into();
        if status == "completed" {
            let conclusion = value["conclusion"]
                .as_str()
                .ok_or("invalid workflow run response")?;
            if conclusion != "success" {
                return Err(format!("workflow_failed: workflow finished with {conclusion}; inspect the linked run and check the approved bundle contains every requested name").into());
            }
            break;
        }
        context.pause(Duration::from_secs(2)).map_err(|e|format!("waiting for Actions: {e} (the remote run may continue; artifacts expire after one day)"))?;
    }
    let output = api(
        context,
        "GET",
        &format!("{run_path}/artifacts?per_page=100"),
        None,
    )?;
    let listing: Value =
        serde_json::from_slice(&output).map_err(|_| "invalid artifact response")?;
    let artifact = listing["artifacts"]
        .as_array()
        .ok_or("invalid artifact response")?
        .iter()
        .find(|a| a["name"] == format!("ghenv-{id}") && !a["expired"].as_bool().unwrap_or(false))
        .and_then(|a| a["id"].as_i64())
        .filter(|id| *id != 0)
        .ok_or("encrypted artifact is missing or expired")?;
    let artifact = Artifact {
        path: format!("{prefix}/artifacts/{artifact}"),
    };
    let archive = api(context, "GET", &format!("{}/zip", artifact.path), None)?;
    let payload = read_artifact(&archive)?;
    let values = unseal(&private, &payload, &id)?;
    if values.len() != selected.len() {
        return Err("workflow returned an unexpected set of names".into());
    }
    if selected
        .iter()
        .any(|name| values.get(name).is_none_or(|value| value.contains('\0')))
    {
        return Err("workflow returned an invalid environment".into());
    }
    Ok(values)
}
pub fn read_artifact(data: &[u8]) -> Result<Vec<u8>> {
    if data.len() > MAX_PAYLOAD * 2 {
        return Err("artifact archive is too large".into());
    }
    let mut archive =
        zip::ZipArchive::new(Cursor::new(data)).map_err(|_| "invalid artifact archive")?;
    if archive.len() != 1 || archive.has_duplicate_names() {
        return Err("artifact must contain only payload.json".into());
    }
    let mut entry = archive
        .by_index(0)
        .map_err(|_| "invalid artifact archive")?;
    if entry.name().map_err(|_| "invalid artifact archive")? != "payload.json"
        || entry.is_dir()
        || entry.is_symlink()
    {
        return Err("artifact must contain only payload.json".into());
    }
    if entry.size() > (MAX_PAYLOAD * 2) as u64 {
        return Err("artifact payload is too large".into());
    }
    let mut payload = Vec::new();
    (&mut entry)
        .take((MAX_PAYLOAD * 2 + 1) as u64)
        .read_to_end(&mut payload)
        .map_err(|_| "invalid artifact payload")?;
    if payload.len() > MAX_PAYLOAD * 2 {
        return Err("artifact payload is too large".into());
    }
    Ok(payload)
}

pub fn github_auth_status(context: &Context) -> Result<bool> {
    Ok(command(
        context,
        &["auth", "status", "--hostname", "github.com"],
        None,
    )?
    .0)
}

#[cfg(test)]
mod tests {
    use super::read_artifact;
    #[test]
    fn artifact_requires_one_regular_payload_without_duplicate_names() {
        let expected = include_bytes!("../../tests/fixtures/go-payload.json");
        assert_eq!(
            read_artifact(include_bytes!("../../tests/fixtures/valid.zip")).unwrap(),
            expected
        );
        for data in [
            include_bytes!("../../tests/fixtures/duplicate.zip").as_slice(),
            include_bytes!("../../tests/fixtures/traversal.zip").as_slice(),
            include_bytes!("../../tests/fixtures/extra.zip").as_slice(),
            include_bytes!("../../tests/fixtures/symlink.zip").as_slice(),
            b"not a ZIP".as_slice(),
        ] {
            assert!(read_artifact(data).is_err());
        }
    }
}
