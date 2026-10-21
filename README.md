# ghenv

Run a local command with an explicitly approved set of GitHub-hosted Preview
credentials. Go CLI, GitHub authentication, no local `.env` file or vault service.

```sh
ghenv run --repo OWNER/REPO --secrets API_KEY --vars API_BASE_URL -- npm run dev
```

## Setup

Requires Go 1.24+, [GitHub CLI](https://cli.github.com/), Linux or macOS, and a
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
go install github.com/nicodes/ghenv@latest
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

## Running commands

```sh
ghenv run --repo OWNER/REPO --env Preview \
  --secrets DATABASE_URL,API_KEY --vars API_BASE_URL \
  --timeout 10m -- npm run dev
```

Flags go before `--`; the command and its arguments go after it. Requested values
replace matching variables inherited by the child. The command inherits terminal
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
public. Go does not guarantee complete erasure of values from memory.

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

## Development

```sh
go test -race ./...
go vet ./...
go build -o ghenv .
```

Tests use synthetic values. They cover encrypted transport, direct exporter
bypass attempts, invalid/fallback bundles, archive validation, process injection,
exit status, and cleanup of descendants that ignore SIGTERM. Linux and macOS
process tests also cover a wrapper that exits before its descendant, foreground
terminal input/restoration, and terminal Ctrl-C cleanup. No live credentials
are needed for these checks.

## Local development launchers

Pin a released CLI in your tool manifest, for example:

```toml
[tools]
"go:github.com/nicodes/ghenv" = "0.1.0"
```

Wrap the existing development supervisor once per session:

```sh
ghenv run --clean-env --repo OWNER/PROJECT --secrets API_KEY --vars PUBLIC_KEY -- make product-dev
```

`--clean-env` keeps terminal settings, executable/tool/cache paths and the SSH
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
