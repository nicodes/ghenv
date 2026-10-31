"""Package and smoke-test the native release executable."""
import os
from native_linkage import verify_linkage
from pathlib import Path
import platform
import subprocess
import tarfile
import zipfile

system, arch, extension = (os.environ[name] for name in ('RELEASE_PLATFORM', 'RELEASE_ARCH', 'RELEASE_EXTENSION'))
expected_system = {'Linux': 'Linux', 'Darwin': 'Darwin', 'Windows': 'Windows'}[system]
assert platform.system() == expected_system
actual_arch = platform.machine().lower()
assert actual_arch in ({'x86_64', 'amd64'} if arch == 'x86_64' else {'arm64', 'aarch64'}), actual_arch
assert extension == ('zip' if system == 'Windows' else 'tar.gz')
executable = 'ghenv.exe' if system == 'Windows' else 'ghenv'
binary = Path(os.environ.get('CARGO_TARGET_DIR','target'))/os.environ.get('RUST_TARGET','')/'release'/executable
identity = f"ghenv {os.environ['VERSION']}\nsource: {os.environ['COMMIT']}"
assert subprocess.check_output([str(binary), '--version'], text=True).strip() == identity
Path('dist').mkdir(exist_ok=True)
archive = Path('dist')/f'ghenv_{system}_{arch}.{extension}'
if extension == 'zip':
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as bundle:
        bundle.write(binary, executable)
        bundle.write('README.md', 'README.md')
else:
    with tarfile.open(archive, 'w:gz') as bundle:
        bundle.add(binary, arcname=executable)
        bundle.add('README.md', arcname='README.md')

# Native CI artifacts preserve the portable runtime of published Go releases.
# Local source builds may use the host C runtime.
if os.environ.get('RUST_TARGET'):
    verify_linkage(binary, system)
