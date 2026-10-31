mod launch;
use ghenv::provider::{self, Context, Request, Result, Values};
use std::{collections::BTreeMap, io::Write, time::Duration};
const USAGE: &str = include_str!("usage.txt");
fn duration(value: &str) -> Result<Duration> {
    if value == "0" {
        return Ok(Duration::ZERO);
    }
    let value = value.strip_prefix('+').unwrap_or(value);
    let mut rest = value;
    let mut seconds = 0_f64;
    let mut parts = 0;
    while !rest.is_empty() {
        let end = rest
            .find(|c: char| !c.is_ascii_digit() && c != '.')
            .ok_or("invalid duration")?;
        let number = rest[..end].parse::<f64>().map_err(|_| "invalid duration")?;
        rest = &rest[end..];
        let (unit, scale) = [
            ("ns", 1e-9),
            ("us", 1e-6),
            ("µs", 1e-6),
            ("μs", 1e-6),
            ("ms", 0.001),
            ("s", 1.),
            ("m", 60.),
            ("h", 3600.),
        ]
        .into_iter()
        .find(|(unit, _)| rest.starts_with(unit))
        .ok_or("invalid duration")?;
        seconds += number * scale;
        rest = &rest[unit.len()..];
        parts += 1;
    }
    if parts == 0 || !seconds.is_finite() || seconds > i64::MAX as f64 / 1e9 {
        return Err("invalid duration".into());
    }
    Ok(Duration::from_secs_f64(seconds))
}
fn flags(args: &[String], spec: &[(&str, &str)]) -> Result<BTreeMap<String, String>> {
    let mut result: BTreeMap<String, String> = spec
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    let mut index = 0;
    while index < args.len() {
        let argument = &args[index];
        if argument == "--" {
            if index + 1 < args.len() {
                return Err("unexpected argument before --".into());
            }
            break;
        }
        let flag = argument
            .strip_prefix("--")
            .or_else(|| argument.strip_prefix('-'))
            .filter(|s| !s.is_empty() && !s.starts_with('-'))
            .ok_or("unexpected argument before --")?;
        let (name, value) = flag
            .split_once('=')
            .map_or((flag, None), |(k, v)| (k, Some(v)));
        if !result.contains_key(name) {
            return Err(format!("flag provided but not defined: -{name}").into());
        }
        let value = if name == "clean-env" {
            value.unwrap_or("true").to_owned()
        } else if let Some(value) = value {
            value.to_owned()
        } else {
            index += 1;
            args.get(index)
                .ok_or_else(|| format!("flag needs an argument: -{name}"))?
                .clone()
        };
        result.insert(name.into(), value);
        index += 1;
    }
    Ok(result)
}
fn boolean(value: &str) -> Result<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err("invalid boolean value for --clean-env".into()),
    }
}
struct Interrupts(Vec<signal_hook::SigId>);
impl Interrupts {
    fn new(context: &Context) -> Result<Self> {
        let mut guards = Self(Vec::new());
        for signal in [signal_hook::consts::SIGINT, signal_hook::consts::SIGTERM] {
            guards.0.push(signal_hook::flag::register(
                signal,
                context.cancellation_flag(),
            )?);
        }
        Ok(guards)
    }
}
impl Drop for Interrupts {
    fn drop(&mut self) {
        for id in self.0.drain(..) {
            signal_hook::low_level::unregister(id);
        }
    }
}
fn handoff<F, T>(timeout: Duration, request: F) -> Result<T>
where
    F: FnOnce(&Context) -> Result<T>,
{
    let context = Context::new(timeout)?;
    let _signals = Interrupts::new(&context)?;
    request(&context)
}
fn command<'a>(args: &'a [String], message: &str) -> Result<(usize, &'a [String])> {
    let separator = args
        .iter()
        .position(|arg| arg == "--")
        .filter(|i| *i + 1 < args.len())
        .ok_or_else(|| message.to_owned())?;
    Ok((separator, &args[separator + 1..]))
}
fn environment() -> Values {
    std::env::vars_os()
        .filter_map(|(name, value)| Some((name.into_string().ok()?, value.into_string().ok()?)))
        .collect()
}
fn execute(args: &[String], raw_args: &[std::ffi::OsString]) -> Result<i32> {
    let Some(first) = args.first() else {
        print!("{USAGE}");
        return Ok(0);
    };
    match first.as_str() {
        "help" | "--help" | "-h" => print!("{USAGE}"),
        "version" | "--version" | "-v" => println!(
            "ghenv {}\nsource: {}",
            env!("GHENV_VERSION"),
            env!("GHENV_COMMIT")
        ),
        "seal" => {
            let get = |name| std::env::var(name).unwrap_or_default();
            let data = provider::seal_bundle(
                get("GHENV_PREVIEW_BUNDLE").as_bytes(),
                &get("GITHUB_REPOSITORY"),
                &get("GHENV_SECRET_NAMES"),
                &get("GHENV_VAR_NAMES"),
                &get("GHENV_PUBLIC_KEY"),
                &get("GHENV_REQUEST_ID"),
            )?;
            std::io::stdout().write_all(&data)?;
        }
        "doctor" => {
            if args[1..]
                .iter()
                .any(|a| matches!(a.as_str(), "--help" | "-h" | "-help"))
            {
                println!("Usage of doctor:\n  --repo OWNER/REPO");
                return Ok(0);
            }
            let flags = flags(&args[1..], &[("repo", "")])?;
            Request {
                repo: flags["repo"].clone(),
                environment: "Preview".into(),
                reference: "main".into(),
                secrets: String::new(),
                variables: "GHENV_PREFLIGHT".into(),
            }
            .validate()?;
            let context = Context::new(Duration::from_secs(30))?;
            if !provider::github_auth_status(&context)? {
                return Err("github_auth_required: run gh auth login --hostname github.com".into());
            }
            provider::check_workflow(&context, &flags["repo"])?;
            println!(
                "GitHub authentication: ready\nghenv.yml workflow: active\nDispatch permissions, Preview approval and requested bundle names are checked when you run a command.\nUse ghenv local -- COMMAND for an offline session."
            );
        }
        "local" => {
            if args.len() < 3 || args[1] != "--" {
                return Err("usage: ghenv local -- COMMAND [ARG...]".into());
            }
            return launch::launch(
                &raw_args[2..],
                &provider::development_environment(&environment(), &Values::new()),
                false,
            );
        }
        "dev" => {
            let message = "usage: ghenv dev [--mode auto|preview|local] [--config ghenv.json] -- COMMAND [ARG...]";
            let (separator, command) = command(&args[1..], message)?;
            let flags = flags(
                &args[1..separator + 1],
                &[
                    ("mode", "auto"),
                    ("config", "ghenv.json"),
                    ("timeout", "5m"),
                ],
            )?;
            let timeout = duration(&flags["timeout"])?;
            if timeout.is_zero() || !matches!(flags["mode"].as_str(), "auto" | "preview" | "local")
            {
                return Err("development mode must be auto, preview or local, with a positive timeout and no positional options".into());
            }
            let environment = handoff(timeout, |context| {
                provider::prepare(context, &flags["config"], &flags["mode"], &environment())
            })?;
            return launch::launch(&raw_args[args.len() - command.len()..], &environment, false);
        }
        "run" => {
            let (separator, command) = command(&args[1..], "specify a command after --")?;
            let flags = flags(
                &args[1..separator + 1],
                &[
                    ("repo", ""),
                    ("env", "Preview"),
                    ("ref", "main"),
                    ("secrets", ""),
                    ("vars", ""),
                    ("timeout", "5m"),
                    ("clean-env", "true"),
                ],
            )?;
            let timeout = duration(&flags["timeout"])?;
            if timeout.is_zero() {
                return Err("a positive timeout is required".into());
            }
            let clean = boolean(&flags["clean-env"])?;
            let request = Request {
                repo: flags["repo"].clone(),
                environment: flags["env"].clone(),
                reference: flags["ref"].clone(),
                secrets: flags["secrets"].clone(),
                variables: flags["vars"].clone(),
            };
            request.validate()?;
            let selected = handoff(timeout, |context| provider::fetch(context, &request))?;
            let environment = if clean {
                provider::development_environment(&environment(), &selected)
            } else {
                selected
            };
            return launch::launch(
                &raw_args[args.len() - command.len()..],
                &environment,
                !clean,
            );
        }
        _ => return Err(format!("unknown command {first:?}; use ghenv --help").into()),
    }
    Ok(0)
}
fn main() {
    let raw: Vec<_> = std::env::args_os().skip(1).collect();
    let prefix = raw.iter().position(|arg| arg == "--").unwrap_or(raw.len());
    if raw[..prefix].iter().any(|arg| arg.to_str().is_none()) {
        eprintln!("ghenv: options must be valid UTF-8");
        std::process::exit(1);
    }
    let args: Vec<_> = raw
        .iter()
        .map(|arg| arg.to_string_lossy().into_owned())
        .collect();
    match execute(&args, &raw) {
        Ok(code) => std::process::exit(code),
        Err(error) => {
            eprintln!("ghenv: {error}");
            std::process::exit(1);
        }
    }
}
