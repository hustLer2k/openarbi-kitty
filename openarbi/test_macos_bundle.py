"""Native launcher/signature regression; requires macOS build tools only."""
from pathlib import Path
import plistlib
import subprocess
import sys
import tarfile
import tempfile
import unittest


@unittest.skipUnless(sys.platform == 'darwin', 'requires macOS codesign and clang')
class MacLauncher(unittest.TestCase):
    def test_signature_and_arguments_survive_archive_and_application_link(self):
        with tempfile.TemporaryDirectory(prefix='oa mac ') as tmp:
            root = Path(tmp)
            payload = root / 'payload'
            app = payload / 'OpenArbi.app'
            executable = app / 'Contents/MacOS/openarbi'
            executable.parent.mkdir(parents=True)
            (app / 'Contents/Info.plist').write_bytes(plistlib.dumps({
                'CFBundleIdentifier': 'com.openarbi.launcher-test',
                'CFBundleExecutable': 'openarbi', 'CFBundlePackageType': 'APPL'}))
            subprocess.run(['xcrun', 'clang', '-Wall', '-Wextra', '-Werror',
                            str(Path(__file__).with_name('macos-launcher.c')),
                            '-o', str(executable)], check=True)
            subprocess.run(['codesign', '--force', '--sign', '-', str(app)], check=True)
            (payload / 'bin').mkdir()
            launcher = payload / 'bin/openarbi'
            launcher.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            launcher.chmod(0o755)
            archive = root / 'payload.tar'
            with tarfile.open(archive, 'w') as out:
                out.add(payload, arcname='payload')
            extracted = root / 'extracted'
            with tarfile.open(archive) as source:
                # This test archive is created above from owned fixture files.
                source.extractall(extracted)
            restored = extracted / 'payload/OpenArbi.app'
            subprocess.run(['codesign', '--verify', '--deep', '--strict', str(restored)], check=True)
            link = root / 'Applications/OpenArbi.app'
            link.parent.mkdir()
            link.symlink_to(restored)
            result = subprocess.run([str(link / 'Contents/MacOS/openarbi'), 'two words', '--help'],
                                    check=True, capture_output=True, text=True)
            self.assertEqual(result.stdout, 'two words\n--help\n')


if __name__ == '__main__':
    unittest.main()
