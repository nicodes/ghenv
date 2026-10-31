"""Collect the complete CI matrix, or package this host for a source build."""
import os
from pathlib import Path
import platform
import shutil
import subprocess
from check_archives import MATRIX

Path('dist').mkdir(exist_ok=True)
if os.environ.get('NATIVE_CLI_ARCHIVES'):
    root=Path(os.environ['NATIVE_CLI_ARCHIVES'])
    expected={f'ghenv_{system}_{arch}.{extension}' for system,arch,extension in MATRIX}
    assert {path.name for path in root.iterdir()}==expected, 'Incomplete or unexpected native artifacts'
    for name in sorted(expected): shutil.copyfile(root/name,Path('dist')/name)
else:
    system=platform.system()
    arch={'amd64':'x86_64','aarch64':'arm64'}.get(platform.machine().lower(),platform.machine().lower())
    assert system in ('Linux','Darwin','Windows') and arch in ('x86_64','arm64')
    env=dict(os.environ,RELEASE_PLATFORM=system,RELEASE_ARCH=arch,RELEASE_EXTENSION='zip' if system=='Windows' else 'tar.gz')
    subprocess.run(['python3','scripts/package-cli.py'],env=env,check=True)
