"""Exercise the shipped installer with valid and hostile release archives."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


class Installer(unittest.TestCase):
    def test_release_archives(self):
        script = Path('install.sh').resolve()
        old = b'previous executable\n'
        payload = b"#!/bin/sh\nprintf 'installed fixture\\n'\n"
        for scenario in ('valid', 'checksum', 'symlink', 'duplicate', 'missing'):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binary, fixture, commands = (root/name for name in ('bin', 'fixture', 'commands'))
                for path in (binary, fixture, commands):
                    path.mkdir()
                (binary/'ghenv').write_bytes(old)
                with tarfile.open(fixture/'archive', 'w:gz') as archive:
                    for _ in range(2 if scenario == 'duplicate' else 1):
                        entry = tarfile.TarInfo('another-file' if scenario == 'missing' else 'ghenv')
                        entry.mode = 0o755
                        entry.uid, entry.gid = 1001, 1001
                        entry.size = len(payload)
                        if scenario == 'symlink':
                            entry.type, entry.linkname, entry.size = tarfile.SYMTYPE, '/outside', 0
                        archive.addfile(entry, io.BytesIO(payload) if entry.size else None)
                digest = hashlib.sha256((fixture/'archive').read_bytes()).hexdigest()
                (fixture/'checksums').write_text(('0'*64 if scenario == 'checksum' else digest)+'  ghenv_Linux_x86_64.tar.gz\n')
                scripts = {
                    'uname': '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; *) exit 1;; esac\n',
                    'curl': '''#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
 case "$1" in
 --output) output=$2; shift 2;;
 --write-out|--retry|--connect-timeout|--max-time|--proto|--proto-redir) shift 2;;
 https://*) url=$1; shift;;
 *) shift;;
 esac
done
case "$url" in
 https://github.com/nicodes/ghenv/releases/latest) printf 'https://github.com/nicodes/ghenv/releases/tag/v1.2.3';;
 https://github.com/nicodes/ghenv/releases/download/v1.2.3/checksums.txt) cp "$INSTALL_FIXTURE/checksums" "$output";;
 https://github.com/nicodes/ghenv/releases/download/v1.2.3/ghenv_Linux_x86_64.tar.gz) cp "$INSTALL_FIXTURE/archive" "$output";;
 *) echo "unexpected download: $url" >&2; exit 1;;
esac
''',
                }
                for name, body in scripts.items():
                    (commands/name).write_text(body)
                    (commands/name).chmod(0o700)
                result = subprocess.run(['sh', str(script), '--install-dir', str(binary)],
                                        env=dict(os.environ, PATH=str(commands)+os.pathsep+os.environ['PATH'], INSTALL_FIXTURE=str(fixture)),
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, scenario == 'valid', result.stderr)
                self.assertEqual((binary/'ghenv').read_bytes(), payload if scenario == 'valid' else old)


if __name__ == '__main__':
    unittest.main(verbosity=2)
