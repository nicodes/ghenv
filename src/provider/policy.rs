use super::{Context, MAX_BUNDLE, Result, Values, fetch, seal};
use serde::{
    Deserialize,
    de::{DeserializeSeed, MapAccess, Visitor},
};
use serde_json::Value;
use std::{
    collections::{BTreeMap, BTreeSet},
    fmt,
    fs::File,
    io::Read,
    path::Path,
};

#[derive(Clone, Debug)]
pub struct Request {
    pub repo: String,
    pub environment: String,
    pub reference: String,
    pub secrets: String,
    pub variables: String,
}
impl Request {
    pub fn validate(&self) -> Result<()> {
        if !repository(&self.repo) {
            return Err("repository must be OWNER/REPO".into());
        }
        if self.environment != "Preview" {
            return Err(
                "local runs only support the Preview environment; production is refused".into(),
            );
        }
        if self.reference.is_empty() {
            return Err("a trusted workflow ref is required".into());
        }
        names(&self.secrets, &self.variables)?;
        Ok(())
    }
}
pub(super) fn repository(repo: &str) -> bool {
    let Some((owner, repo)) = repo.split_once('/') else {
        return false;
    };
    !owner.is_empty()
        && owner.as_bytes()[0].is_ascii_alphanumeric()
        && owner
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
        && !repo.is_empty()
        && repo != "."
        && repo != ".."
        && repo
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_.-".contains(&b))
}
fn identifier(name: &str) -> bool {
    !name.is_empty()
        && (name.as_bytes()[0].is_ascii_alphabetic() || name.starts_with('_'))
        && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_')
}
fn valid(name: &str, value: &str) -> bool {
    identifier(name) && !name.eq_ignore_ascii_case("GITHUB_TOKEN") && !value.contains('\0')
}
pub(super) fn names(secrets: &str, variables: &str) -> Result<Vec<String>> {
    let mut result = Vec::new();
    for group in [secrets, variables].into_iter().filter(|g| !g.is_empty()) {
        for name in group.split(',') {
            if !valid(name, "") {
                return Err("names must be environment variable identifiers; GITHUB_TOKEN cannot be exported".into());
            }
            if result.iter().any(|n| n == name) {
                return Err(format!("duplicate name {name}").into());
            }
            result.push(name.into());
        }
    }
    if result.is_empty() {
        return Err("request at least one name with --secrets or --vars".into());
    }
    Ok(result)
}
struct Depth(usize);
impl<'de> DeserializeSeed<'de> for Depth {
    type Value = Value;
    fn deserialize<D: serde::Deserializer<'de>>(
        self,
        decoder: D,
    ) -> std::result::Result<Value, D::Error> {
        if self.0 > 4 {
            return Err(serde::de::Error::custom("JSON is too deeply nested"));
        }
        decoder.deserialize_any(self)
    }
}
impl<'de> Visitor<'de> for Depth {
    type Value = Value;
    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("unambiguous JSON objects and scalar values")
    }
    fn visit_bool<E: serde::de::Error>(self, v: bool) -> std::result::Result<Value, E> {
        Ok(Value::Bool(v))
    }
    fn visit_i64<E: serde::de::Error>(self, v: i64) -> std::result::Result<Value, E> {
        Ok(v.into())
    }
    fn visit_u64<E: serde::de::Error>(self, v: u64) -> std::result::Result<Value, E> {
        Ok(v.into())
    }
    fn visit_f64<E: serde::de::Error>(self, v: f64) -> std::result::Result<Value, E> {
        Ok(Value::from(v))
    }
    fn visit_str<E: serde::de::Error>(self, v: &str) -> std::result::Result<Value, E> {
        Ok(v.into())
    }
    fn visit_string<E: serde::de::Error>(self, v: String) -> std::result::Result<Value, E> {
        Ok(v.into())
    }
    fn visit_unit<E: serde::de::Error>(self) -> std::result::Result<Value, E> {
        Ok(Value::Null)
    }
    fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> std::result::Result<Value, M::Error> {
        let mut seen = BTreeSet::new();
        let mut values = serde_json::Map::new();
        while let Some(key) = map.next_key::<String>()? {
            if !seen.insert(key.to_lowercase()) {
                return Err(serde::de::Error::custom("duplicate JSON key"));
            }
            values.insert(key, map.next_value_seed(Depth(self.0 + 1))?);
        }
        Ok(Value::Object(values))
    }
}
fn unique(data: &[u8]) -> Result<Value> {
    if data.len() > MAX_BUNDLE {
        return Err("JSON exceeds size limit".into());
    }
    let mut decoder = serde_json::Deserializer::from_slice(data);
    let value = Depth(0)
        .deserialize(&mut decoder)
        .map_err(|_| "invalid or ambiguous JSON")?;
    decoder.end().map_err(|_| "trailing JSON")?;
    Ok(value)
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Bundle {
    version: u32,
    environment: String,
    repository: String,
    secrets: Values,
    variables: Values,
}
pub fn seal_bundle(
    data: &[u8],
    repository_name: &str,
    secrets: &str,
    variables: &str,
    public: &str,
    request_id: &str,
) -> Result<Vec<u8>> {
    let invalid = "missing or invalid GHENV_PREVIEW_BUNDLE; expected a version 1 Preview bundle for this repository";
    let value = unique(data).map_err(|_| invalid)?;
    let bundle: Bundle = serde_json::from_value(value).map_err(|_| invalid)?;
    if !repository(repository_name)
        || bundle.version != 1
        || bundle.environment != "Preview"
        || bundle.repository != repository_name
        || bundle
            .secrets
            .iter()
            .any(|(k, v)| !valid(k, v) || bundle.variables.contains_key(k))
        || bundle.variables.iter().any(|(k, v)| !valid(k, v))
    {
        return Err(invalid.into());
    }
    names(secrets, variables)?;
    let mut selected = Values::new();
    for (group, source) in [(secrets, &bundle.secrets), (variables, &bundle.variables)] {
        if group.is_empty() {
            continue;
        }
        for name in group.split(',') {
            selected.insert(
                name.into(),
                source
                    .get(name)
                    .ok_or_else(|| {
                        format!("requested name {name} is unavailable in this workflow environment")
                    })?
                    .clone(),
            );
        }
    }
    if request_id.len() != 32
        || !request_id
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err("invalid request ID".into());
    }
    seal(public, &selected, request_id)
}
fn string_or_null<'de, D: serde::Deserializer<'de>>(
    decoder: D,
) -> std::result::Result<String, D::Error> {
    Ok(Option::<String>::deserialize(decoder)?.unwrap_or_default())
}
fn map_or_null<'de, D: serde::Deserializer<'de>>(
    decoder: D,
) -> std::result::Result<Values, D::Error> {
    Ok(Option::<Values>::deserialize(decoder)?.unwrap_or_default())
}
#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Project {
    pub version: u32,
    #[serde(deserialize_with = "string_or_null")]
    pub repository: String,
    #[serde(deserialize_with = "string_or_null")]
    pub default_mode: String,
    #[serde(deserialize_with = "string_or_null")]
    pub secrets: String,
    #[serde(deserialize_with = "string_or_null")]
    pub variables: String,
    #[serde(deserialize_with = "map_or_null")]
    pub prefixes: Values,
    #[serde(deserialize_with = "map_or_null")]
    pub aliases: Values,
    #[serde(deserialize_with = "map_or_null")]
    pub environment: Values,
}
impl Project {
    pub fn read(path: impl AsRef<Path>) -> Result<Self> {
        let mut bytes = Vec::new();
        File::open(path)?
            .take((MAX_BUNDLE + 1) as u64)
            .read_to_end(&mut bytes)?;
        if bytes.len() > MAX_BUNDLE {
            return Err("invalid or oversized project configuration".into());
        }
        let value = unique(&bytes).map_err(|_| "invalid project configuration JSON")?;
        let object = value
            .as_object()
            .ok_or("invalid project configuration JSON")?;
        for field in object.keys() {
            if ![
                "version",
                "repository",
                "default_mode",
                "secrets",
                "variables",
                "prefixes",
                "aliases",
                "environment",
            ]
            .contains(&field.as_str())
            {
                return Err(format!("unknown project field {field:?}").into());
            }
        }
        let project: Self =
            serde_json::from_value(value).map_err(|_| "invalid project configuration")?;
        project.validate()?;
        Ok(project)
    }
    pub fn validate(&self) -> Result<()> {
        if self.version != 1 || !repository(&self.repository) {
            return Err("project requires version 1 and an OWNER/REPO repository".into());
        }
        if !matches!(self.default_mode.as_str(), "preview" | "local") {
            return Err("project default_mode must be preview or local".into());
        }
        let names = if self.secrets.is_empty() && self.variables.is_empty() {
            Vec::new()
        } else {
            names(&self.secrets, &self.variables)?
        };
        if self.prefixes.iter().any(|(name, prefix)| {
            !names.contains(name) || prefix.is_empty() || prefix.contains('\0')
        }) {
            return Err("prefix checks must name selected entries and a nonempty prefix".into());
        }
        if self.aliases.iter().any(|(dest, source)| {
            !valid(dest, "") || !names.contains(source) || names.contains(dest)
        }) {
            return Err("aliases must map new environment names to selected entries".into());
        }
        if self.environment.iter().any(|(name, value)| {
            !valid(name, value) || self.aliases.contains_key(name) || names.contains(name)
        }) {
            return Err("fixed environment entries must be valid and distinct from selected entries and aliases".into());
        }
        Ok(())
    }
    pub fn environment_with<F>(
        &self,
        context: &Context,
        mode: &str,
        base: &Values,
        mut fetch: F,
    ) -> Result<Values>
    where
        F: FnMut(&Context, &Request) -> Result<Values>,
    {
        self.validate()?;
        let mode = if mode == "auto" {
            self.default_mode.as_str()
        } else {
            mode
        };
        if !matches!(mode, "local" | "preview") {
            return Err("development mode must be preview or local".into());
        }
        let mut values = Values::new();
        if mode == "preview" && (!self.secrets.is_empty() || !self.variables.is_empty()) {
            let request = Request {
                repo: self.repository.clone(),
                environment: "Preview".into(),
                reference: "main".into(),
                secrets: self.secrets.clone(),
                variables: self.variables.clone(),
            };
            let selected = fetch(context, &request)?;
            let names = names(&self.secrets, &self.variables)?;
            if selected.len() != names.len() {
                return Err("development handoff returned an unexpected selection".into());
            }
            for name in names {
                let value = selected
                    .get(&name)
                    .filter(|v| valid(&name, v))
                    .ok_or("development handoff returned an invalid selection")?;
                if self
                    .prefixes
                    .get(&name)
                    .is_some_and(|prefix| !value.starts_with(prefix))
                {
                    return Err(format!(
                        "Preview entry {name} does not match its required development prefix"
                    )
                    .into());
                }
                values.insert(name, value.clone());
            }
            for (destination, source) in &self.aliases {
                values.insert(destination.clone(), values[source].clone());
            }
        }
        values.extend(self.environment.clone());
        Ok(development_environment(base, &values))
    }
}
pub fn prepare(
    context: &Context,
    profile: impl AsRef<Path>,
    mode: &str,
    base: &Values,
) -> Result<Values> {
    Project::read(profile)?.environment_with(context, mode, base, fetch)
}
pub fn development_environment(base: &Values, values: &Values) -> Values {
    let allowed = [
        "HOME",
        "USER",
        "LOGNAME",
        "PATH",
        "SHELL",
        "PWD",
        "TMPDIR",
        "TMP",
        "TEMP",
        "TERM",
        "COLORTERM",
        "TERM_PROGRAM",
        "TERM_PROGRAM_VERSION",
        "LANG",
        "LC_ALL",
        "LC_CTYPE",
        "DISPLAY",
        "WAYLAND_DISPLAY",
        "XDG_RUNTIME_DIR",
        "XDG_CACHE_HOME",
        "XDG_CONFIG_HOME",
        "XDG_DATA_HOME",
        "SSH_AUTH_SOCK",
        "GOROOT",
        "GOPATH",
        "GOMODCACHE",
        "GOCACHE",
        "GOTOOLCHAIN",
        "BUN_INSTALL",
        "BUN_INSTALL_CACHE_DIR",
        "MISE_DATA_DIR",
        "MISE_CACHE_DIR",
        "MISE_CONFIG_DIR",
        "MISE_STATE_DIR",
        "MISE_TRUSTED_CONFIG_PATHS",
        "MISE_INSTALLS_DIR",
        "CARGO_HOME",
        "RUSTUP_HOME",
    ];
    let mut result: BTreeMap<_, _> = base
        .iter()
        .filter(|(name, _)| allowed.contains(&name.as_str()))
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect();
    result.extend(values.clone());
    result.insert("EXPO_NO_DOTENV".into(), "1".into());
    result
}
