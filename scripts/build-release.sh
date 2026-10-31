#!/usr/bin/env bash
set -euo pipefail
test ! -L .artifacts && test ! -L .artifacts/build && test ! -L dist
export VERSION="${VERSION:-dev}"
export COMMIT="${COMMIT:-$(git rev-parse HEAD)}"
export DATE="${DATE:-unknown}"
[[ "$VERSION" == dev || "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release version' >&2; exit 1; }
test "$COMMIT" = "$(git rev-parse HEAD)"
if [ -z "${NATIVE_CLI_ARCHIVES:-}" ]; then cargo build --release --locked; fi
python3 scripts/build-native.py
python3 scripts/check_archives.py --record
mkdir -p .artifacts/build
python3 - <<'PY_BUILD'
import sys
sys.path.insert(0,'scripts')
from check_archives import extract,host_archive
from pathlib import Path
import tempfile,shutil,os
with tempfile.TemporaryDirectory() as directory:
    binary=extract(host_archive(),directory)
    shutil.copyfile(binary,Path('.artifacts/build/app'))
    Path('.artifacts/build/app').chmod(0o755)
PY_BUILD
