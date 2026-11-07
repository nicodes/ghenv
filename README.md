<!-- Generated from private documentation source. Do not edit directly. Source SHA256: 6609318195a3e57339d45eefcfdc7c66a42edbba34b4a60a991602f2b37fb5c5 -->

# ghenv

Run a local command with an explicitly approved set of GitHub-hosted Preview
credentials. Standalone CLI, GitHub authentication, no local `.env` file or vault service.

```sh
ghenv run --repo OWNER/REPO --secrets API_KEY --vars API_BASE_URL -- npm run dev
```

## Install

Linux and macOS (x86_64 or arm64), using prebuilt binaries:

With curl:

```sh
curl -fsSL https://ghenv.dev/install | sh
```

Or with mise:

```sh
mise use --global github:nicodes/ghenv@latest
```

The shell installer verifies the release checksum and installs to `~/.local/bin`; add that directory to your PATH if necessary. Mise manages its own installation directory and PATH through its shell activation.

## Setup

Requires [GitHub CLI](https://cli.github.com/), Linux or macOS (x86_64 or arm64), and a
GitHub.com repository with Actions enabled. Authenticate with `gh auth login`.
A fine-grained token needs Actions read/write for dispatch and artifact cleanup.
Environment approval and branch restrictions still apply.

For a project repository, use the small dispatch workflow from
[`ghenv-actions`](https://github.com/nicodes/ghenv-actions), pinned to a reviewed
full action commit. It builds the reviewed exporter, handles encrypted artifact
upload, and cleans temporary files without checking out application code.
The workflow in this CLI repository is the standalone implementation for CLI
development. For the normal setup, install the workflow on the default branch. `--ref`
defaults to `main` and must identify a trusted workflow revision.

Create a GitHub environment named **`Preview`**. Its owner must curate a single
secret named **`GHENV_PREVIEW_BUNDLE`** containing JSON like this:

```json
{
  "version": 1,
  "environment": "Preview",
  "repository": "OWNER/REPO",
  "secrets": {
    "API_KEY": "replace-with-a-development-only-credential"
  },
  "variables": {
    "API_BASE_URL": "http://127.0.0.1:3000"
  }
}
```

Use the actual repository name reported by GitHub, including its casing. Paste
this JSON into the GitHub secret form or the interactive prompt below; no
plaintext local file is needed. The entire bundle must fit GitHub's 48 KiB
per-secret limit.

```sh
gh secret set GHENV_PREVIEW_BUNDLE --repo OWNER/REPO --env Preview
ghenv run --repo OWNER/REPO --secrets API_KEY --vars API_BASE_URL -- npm run dev
```

The bundle is the owner-maintained export allowlist. Every permitted dispatcher
who obtains any required environment approval can request any entry in it. Only
put development credentials there. Existing standalone Actions secrets and
variables are not automatically imported; migrate approved development values
into this bundle before using the hardened workflow.

## Export boundary

The workflow passes **only `GHENV_PREVIEW_BUNDLE`** to the exporter. It never
serializes the surrounding GitHub secrets or variables contexts. The exporter
validates the bundle's version, `Preview` scope, and repository against trusted
`GITHUB_REPOSITORY` metadata before selecting requested entries. Direct API
callers receive the same checks as the CLI. Missing names, malformed or ambiguous
JSON, unsupported scope/version, foreign repositories, duplicate names, NUL
bytes, and `GITHUB_TOKEN` exports fail before an artifact is produced.

`--env` defaults to `Preview`; all other environment names are refused. The
workflow job is also fixed to `Preview`. Production-marked bundles are rejected,
including one returned through GitHub's same-name secret fallback.

GitHub's merged secret context does not attest where an individual value came
from. **Reserve `GHENV_PREVIEW_BUNDLE` exclusively for development credentials at
all GitHub scopes, and never reuse that name for production.** A valid
Preview-marked bundle at repository or organization scope can still be selected
by GitHub's fallback. Its contents must remain development-only. The scope
marker prevents mixing correctly labelled bundles; it cannot detect a
production credential that an owner incorrectly labels as Preview. Restrict who
can change the bundle, workflow, and code it builds.

## Check installation and setup

```sh
ghenv --version
ghenv doctor --repo OWNER/REPO
```

`doctor` checks GitHub authentication and the installed workflow without exporting credentials. Dispatch permissions, environment approval and selected bundle names are checked during the actual handoff. Errors identify missing authentication, unavailable workflows and denied API requests without echoing credential-bearing diagnostics.

## Running commands

```sh
ghenv run --repo OWNER/REPO --env Preview \
  --secrets DATABASE_URL,API_KEY --vars API_BASE_URL \
  --timeout 10m -- npm run dev
```

Flags go before `--`; the command and its arguments go after it. From v0.4.0, `run` starts the child with an isolated environment of terminal and tool paths plus the requested values. Other inherited credentials and loader hooks are removed, and Expo dotenv loading is disabled. Use `--clean-env=false` only when you deliberately need the parent environment; this also permits inherited credentials and application dotenv overrides. The command inherits terminal
streams and its ordinary exit code is returned by `ghenv`. Interactive children
receive the foreground terminal.

The child runs in its own process group. Interrupt, termination, and hangup
signals received by `ghenv` are forwarded to that group and start a five-second
shutdown deadline. When the command exits, remaining group members are
terminated too, with the same bounded grace period. Remaining members then
receive SIGKILL; a second signal received by `ghenv` accelerates cleanup.
Terminal Ctrl-C goes directly to the foreground command. If that command
handles or ignores it without exiting, `ghenv` keeps waiting; send SIGTERM to
`ghenv` to initiate bounded group cleanup. This is process
supervision, not a sandbox: a program deliberately creating another session or
process group can escape that cleanup. SIGKILL of `ghenv` itself cannot run cleanup.

## Encrypted handoff

1. The CLI generates a temporary RSA-3072 key pair in memory and dispatches
   `ghenv.yml` with its public key and requested names.
2. The workflow validates the fixed Preview bundle and selects permitted entries.
   AES-256-GCM encrypts the JSON, and RSA-OAEP-SHA256 wraps the random AES key.
   The unique request ID authenticates the payload.
3. GitHub stores only ciphertext in an artifact with one-day retention.
4. The CLI downloads and decrypts in memory, attempts to delete the artifact,
   then launches the command with the selected values in its environment.

No private key or decrypted payload is written to disk by the CLI. The workflow
does not write plaintext credential files. Each invocation waits for a runner
and any environment approval. A failed or interrupted request can leave
ciphertext until retention expires, and its remote job may continue.

**Local users and administrators can inspect injected secrets.** An application
can print or persist its environment. Keys developers must never read need to
stay behind a remote API. Browser-facing settings such as `EXPO_PUBLIC_*` remain
public. Rust does not guarantee complete erasure of all values from memory.

## Replacing dotenv files

Injection can replace scalar dotenv settings when the app reads its process
environment. Map bundle entry names to those the app expects. Public client
settings may require a framework prefix such as `EXPO_PUBLIC_`; never add that
prefix to a private key.

Check the launcher before removing files: dotenv overrides, shell scripts that
source `.env`, Docker Compose `env_file`, and tools requiring credential files
need an adapter. For Expo, set `EXPO_NO_DOTENV=1` to prevent loading old files.
Locally generated database credentials and ports also need a local configuration
source. `ghenv` does not remove inherited production settings or rewrite files.

## Project development runner

A project can commit a public `ghenv.toml` containing credential **names** and
launch policy. Keep credential values in GitHub Preview. For example:

```toml
version = 1
repository = "OWNER/PROJECT"
default_mode = "preview"
secrets = "API_KEY"
variables = "API_BASE_URL"

[prefixes]
API_KEY = "test_"

[aliases]
APP_PUBLIC_API_URL = "API_BASE_URL"

[environment]
BUN_OPTIONS = "--no-env-file"
```

```sh
ghenv dev -- npm run dev
ghenv dev --mode local -- npm run dev
```

`auto`, the default mode, follows the profile's `default_mode` (`preview` or
`local`). Use `preview` where the application consumes Preview credentials;
use `local` for a fixture stack that does not. `--mode local` always skips the
GitHub handoff. A profile with no selected secrets or variables starts the same
clean child in either mode without contacting GitHub.

The runner reads `ghenv.toml` from the current directory; `--config PATH`
selects another TOML profile. Current-source builds refuse legacy JSON profiles; convert them explicitly before starting a session. Published binaries retain their released interface. The encrypted Preview bundle remains JSON. `--timeout` bounds the handoff and defaults to five
minutes. Invalid profiles or failed Preview requests stop startup; they do not
fall back to another mode.

`prefixes` checks selected values before starting the child and reports only
entry names on failure. `aliases` maps a new environment name to a selected
entry. An alias must be appropriate for its destination: browser-facing values
must come from public entries. `environment` holds fixed **nonsecret** settings.
Aliases and fixed entries cannot overwrite selected names. Unknown fields,
duplicate TOML keys, invalid identifiers and unsupported modes are refused.
The existing clean environment, dotenv boundary, interactive terminal handling,
exit status and process-group cleanup also apply to `dev`.

Projects own their development commands. Use `ghenv dev --config ghenv.toml --mode local -- COMMAND` for an explicitly selected offline session, or wrap the native supervisor with `ghenv local -- COMMAND`. Deployment configuration is independent of development credentials.

Current-source migration is manual: create the named TOML source with the same effective settings and archive the legacy JSON v1 source outside the active configuration directory. No automatic import command is provided. Preserve explicit `false`, zero and empty arrays; omit null overrides only where they meant inheritance or defaults. Do not copy credentials into tracked source files. Existing installed settings are not migrated by a source update.

## Local development launchers

Pin a released CLI in your tool manifest, for example:

```toml
[tools]
"github:nicodes/ghenv" = "latest"
```

Wrap the existing development supervisor once per session:

```sh
ghenv run --clean-env --repo OWNER/PROJECT --secrets API_KEY --vars PUBLIC_KEY -- make product-dev
```

`run` isolates the child by default; `--clean-env` explicitly selects the same behavior. It keeps terminal settings, executable/tool/cache paths and the SSH
agent socket. It drops other inherited variables, including GitHub tokens,
remote database URLs, provider keys, shell startup hooks and Make overrides.
GitHub authentication still uses the original parent environment during the
handoff. Only the selected Preview values reach the application. Expo dotenv
loading is disabled with `EXPO_NO_DOTENV=1`. Other dotenv readers must be removed
from the application's local launch commands so they cannot override the handoff.
Pass local ports and other nonsecret options as command arguments. Do not request
production database URLs or deploy keys. Credential-free workflows can use `ghenv local -- make product-dev` to apply
the same environment isolation without GitHub authentication or an Actions round trip.

To reuse existing individual Preview secrets and variables, see the explicit
allowlist example in [ghenv-actions](https://github.com/nicodes/ghenv-actions).
The caller constructs the bundle in memory; no duplicate secret needs provisioning.

## Rust provider

The root Rust crate exposes `ghenv::provider` for project runners that need the credential handoff without launching a process.

```rust
use ghenv::provider::{Context, Values, prepare};
use std::time::Duration;

let context = Context::new(Duration::from_secs(300))?;
let parent: Values = std::env::vars().collect();
let environment = prepare(&context, "profile.json", "auto", &parent)?;
```

The profile uses the version 1 project fields above. `prepare` returns an isolated child environment. `fetch` accepts an explicit `Request`; `seal_bundle` implements the workflow export boundary. `Context` supports a deadline and explicit cancellation. No plaintext credential file is created. The encrypted `ghenv-v1` protocol remains compatible with existing workflow exporters. Earlier immutable Go module tags remain available for older consumers.

## Build from source

With Rust 1.94.0 installed:

```sh
cargo build --release --locked
./target/release/ghenv --version
```

## Contributor verification

`mise.toml` pins contributor SDKs. The generated root Makefile selects an immutable shared cookbook revision and binds it to `cookbook.toml`.

```sh
make help
make setup
make build
make test
```

`make help` works offline before setup. `make setup` explicitly prepares pinned SDKs, locked dependencies and declared fixtures. `make build` compiles and packages the native outputs, then validates their source and artifact identity. `make test` runs the implemented checks against those current-source outputs. Run build again after changing source; tests do not install tools or build missing shipping artifacts.

Use `make fmt` to apply formatting, `make test:lint`, `make test:unit`, `make test:security` or `make test:integration` to select a group. This recipe has no application development service. `make clean` removes declared generated outputs. Build and test evidence stays in `.artifacts/verification`.

The verified executable is `.artifacts/build/ghenv`. CI retains Linux and macOS archives for both x86_64 and arm64. Native provider/archive tests, protocol fixtures, CLI cancellation and terminal behavior, environment isolation and installer refusal checks remain product-owned. End users install the published distribution; contributor checks do not request a secret bundle.

## Machine diagnostics

Use `ghenv --json version`, `ghenv --json doctor --repo OWNER/REPO`, or `ghenv --json help` for one structured result on stdout. Diagnostics use stderr and failures exit nonzero. `doctor` verifies the existing GitHub authentication and workflow without requesting a secret bundle.

`dev --config FILE` selects an explicit project configuration; the default is `ghenv.toml`. Launch commands preserve their child process streams and exit status. `seal` retains its encrypted binary protocol. These commands reject `--json` before launching a child or acquiring secrets.
