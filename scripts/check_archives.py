"""Validate native archives, checksums and embedded source identity."""
import argparse
import hashlib
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import zipfile
import os

MATRIX = [(system, arch, 'zip' if system == 'Windows' else 'tar.gz')
          for system in ('Linux', 'Darwin') for arch in ('x86_64', 'arm64')]


def host_archive():
    arch = {'amd64': 'x86_64', 'aarch64': 'arm64'}.get(platform.machine().lower(), platform.machine().lower())
    system = platform.system()
    return Path('dist') / f'ghenv_{system}_{arch}.{ "zip" if system == "Windows" else "tar.gz" }'


def extract(archive, directory):
    binary = 'ghenv.exe' if archive.suffix == '.zip' else 'ghenv'
    expected = {binary, 'README.md'}
    if archive.suffix == '.zip':
        with zipfile.ZipFile(archive) as package:
            if set(package.namelist()) != expected or len(package.infolist()) != 2:
                raise ValueError('unexpected ZIP contents')
            package.extractall(directory)
    else:
        with tarfile.open(archive) as package:
            members = package.getmembers()
            if {item.name for item in members} != expected or len(members) != 2 or not all(item.isfile() for item in members):
                raise ValueError('unexpected TAR contents')
            package.extractall(directory, filter='data')
    return Path(directory) / binary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--record', action='store_true')
    args = parser.parse_args()
    expected = {f'ghenv_{system}_{arch}.{extension}' for system, arch, extension in MATRIX}
    if not os.environ.get('NATIVE_CLI_ARCHIVES'):
        expected = {host_archive().name}
    actual = {p.name for p in Path('dist').glob('*') if p.name.endswith(('.tar.gz', '.zip'))}
    if actual != expected:
        raise ValueError('release must contain exactly the expected native archives')
    checksums = ''.join(hashlib.sha256((Path('dist') / name).read_bytes()).hexdigest()+'  '+name+'\n' for name in sorted(expected))
    manifest = Path('dist/checksums.txt')
    if args.record:
        manifest.write_text(checksums)
        return
    if manifest.read_text() != checksums:
        raise ValueError('release checksums differ from archive bytes')
    for name in sorted(expected):
        with tempfile.TemporaryDirectory() as directory:
            extract(Path('dist') / name, directory)
    with tempfile.TemporaryDirectory() as directory:
        binary = extract(host_archive(), directory)
        version = subprocess.check_output([binary, '--version'], text=True, timeout=10).strip()
        wanted = os.environ.get('VERSION', 'dev')
        revision = os.environ.get('COMMIT') or subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
        if version != f'ghenv {wanted}\nsource: {revision}':
            raise ValueError('archived binary version differs from the requested release')
        print(version)
    print(f'{len(expected)} native archives, checksums and source identity verified')
    with tempfile.TemporaryDirectory() as directory:
        binary = extract(host_archive(), directory).resolve()
        subprocess.run(['python3','tests/cli_test.py'],env=dict(os.environ,GHENV_TEST_BINARY=str(binary)),check=True)


if __name__ == '__main__':
    main()
