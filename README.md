# ghenv

Run a local command with named GitHub Actions secrets and variables, without
creating a local `.env` file. Go CLI, GitHub authentication, no vault service.

```sh
ghenv run --repo nicodes/ghenv --secrets DEMO_SECRET --vars DEMO_MESSAGE -- npm run dev
```

## First demo

Requires Go 1.24+, [GitHub CLI](https://cli.github.com/), a GitHub.com repository
with Actions enabled, and permission to dispatch workflows and delete artifacts
(Actions read/write for a fine-grained token). The workflow must exist on the
repository's default branch before GitHub accepts manual dispatches. For this
first demo, use this repository after the PR is merged.

```sh
gh auth login
go install github.com/nicodes/ghenv@latest

# Enter a development-only secret at the prompt; do not put it in shell history.
gh secret set DEMO_SECRET --repo nicodes/ghenv
gh variable set DEMO_MESSAGE --body 'Hello from GitHub' --repo nicodes/ghenv

# Show the variable and verify the secret arrived without printing its value.
ghenv run --repo nicodes/ghenv --secrets DEMO_SECRET --vars DEMO_MESSAGE -- \
  sh -c 'printf "%s\n" "$DEMO_MESSAGE"; test -n "$DEMO_SECRET" && echo "Secret injected"'
```

The default GitHub environment is `development`; create it in repository Settings
→ Environments and put environment-specific values there if desired. Repository
and permitted organization values are also available through the workflow's
`secrets` and `vars` contexts. Environment values take precedence according to
GitHub's context rules.

```sh
ghenv run --repo OWNER/REPO --env development \
  --secrets DATABASE_URL,API_KEY --vars API_BASE_URL \
  --timeout 10m -- npm run dev
```

Flags go before `--`; the command and its arguments go after it. Values replace
matching variables inherited by the child process. Empty values are supported;
missing names, duplicate names, NUL bytes and `GITHUB_TOKEN` are rejected. The
command inherits the terminal streams, receives interrupt/termination signals,
and its ordinary exit code is returned by `ghenv`.

## How it works

1. The CLI generates a temporary RSA-3072 key pair in memory and dispatches
   `.github/workflows/ghenv.yml` with its public key and requested names.
2. The workflow builds the same Go program and selects only those names from
   its GitHub secrets and variables. A random AES-256-GCM key encrypts the JSON;
   RSA-OAEP-SHA256 encrypts that key. The request ID authenticates the payload.
3. GitHub stores an artifact containing only ciphertext, with one-day retention.
4. The CLI downloads it, decrypts in memory, attempts to delete the artifact,
   and launches your command with the values in its environment.

No private key or decrypted payload is written to disk by the CLI. The workflow
does not write plaintext secret files. There is no local cache, so each invocation
waits for an Actions runner and any environment approval. An interrupted or failed
request can leave ciphertext until retention expires; the remote job may continue.

## Scope and trust

This is a first demo for GitHub.com and POSIX development machines. It is not a
GitHub-supported secret retrieval feature. Actions jobs can access secrets, while
the secrets REST API cannot read their values. This workflow deliberately makes
requested values available to developers who can dispatch it and obtain approval
for its environment. Use development credentials and environment protection rules.
Anyone trusted to modify the workflow or the code it builds can change what it
exports. Review those changes as you would any other workflow that handles secrets.

The initial handoff workflow builds Go code from its own repository. To use a
different repository, install the workflow there and adjust its checkout/build
steps to build a reviewed, immutable commit of `nicodes/ghenv`; do not build
untrusted application code in a job that exports secrets. The CLI's `--repo`
always identifies the repository holding the secrets and workflow. `--ref`
defaults to `main` and must identify a trusted workflow revision.

The launched application and its descendants can read, print or persist their
environment. Values still exist in process memory; Go does not guarantee their
complete erasure. Do not use shell tracing or commands that print secrets.

## Development

```sh
go test -race ./...
go vet ./...
go build -o ghenv .
```

Tests exercise encrypted transport through a simulated GitHub API, child-process
injection, payload tampering, wrong keys, request binding, missing values and
artifact cleanup. They do not require credentials or access real secrets.
