# Desktop installation and releases

No desktop release is currently pinned by `install.sh`. It intentionally fails
until an explicitly published version is supplied; it never selects `latest`.
After publication, install from a desktop terminal:

```sh
curl -fsSL https://raw.githubusercontent.com/hustLer2k/openarbi-kitty/main/install.sh |
  OPENARBI_VERSION=vX.Y.Z sh
```

The POSIX bootstrap needs curl and sha256sum (Linux) or shasum (macOS). It selects
Linux amd64 or macOS arm64, verifies the native installer, and passes the exact
payload URL/checksum. No user Go, Python, Git or Homebrew is needed. Checksums
come from the same HTTPS release: they detect corruption but are not an
independent release signature.

Configure and test `ssh revenge` first, including its known host key. The broker
uses system OpenSSH with host checking. Login reads a hidden token from `/dev/tty`,
validates it through the broker, and saves it only in the OS credential store.
Linux requires a working Secret Service such as GNOME Keyring. Missing graphical
libraries produce actionable package names; Debian/Ubuntu package installation
requires an explicit terminal answer. `--no-connect` skips login/launch;
`--no-sound` skips the chime. Unavailable sound does not fail installation.

Linux software lives in `~/.local/share/openarbi`; macOS software lives in
`~/Library/Application Support/OpenArbi/install`, linked from
`~/Applications/OpenArbi.app`. Both get a managed `~/.local/bin/openarbi` link.
The installer adds a removable, delimited PATH block to Bash rc and its selected
login profile, plus Zsh rc/login files (respecting ZDOTDIR). Existing configuration
bytes remain intact; symlink or edited managed blocks produce explicit errors.
Open a new terminal to use the updated PATH. Linux gets a user desktop entry. Unrelated paths,
are preserved and reported. Previous CLI installations migrate only after their
receipt checksums, modes and links verify. `--legacy-prefix` selects a nondefault
previous prefix. The old executable link then points at the new graphical launcher. Same-root
migration uses compatible paths and the single current pointer switch. Migration
from another prefix keeps its old CLI usable until activation, then repoints it
with a durable journal; rerunning the installer or uninstall completes recovery
if the process is interrupted. Unrecognized manually installed binaries are
preserved rather than adopted by filename alone.

Releases stage into separate directories and are checksum-verified, safely
extracted and smoke-tested before an atomic `current` symlink switch. Corruption,
cancellation, failed validation and unsafe tar entries preserve the previous
release. Archive links, duplicate files and special files are rejected. An OS
lock excludes concurrent installations; previous versions remain available.

Quit OpenArbi before updating or uninstalling. Rerun the bootstrap or use:

```sh
openarbi update                 # resolve the published stable GitHub release
openarbi update --version vX.Y.Z # resolve this version
# Explicit offline/custom artifact flags --archive/--url and --sha256 remain available.
openarbi uninstall
```

Update resolves the versioned HTTPS release.json manifest, validates its requested
version/platform and SHA-256, then uses the same verified staging transaction.
Missing releases, mismatched versions, bad checksums and unsupported platforms
fail explicitly. Update skips login and launch by default. Under the GUI launch
lock it verifies all windows closed, reconciles a proven stale window snapshot,
shuts down the old broker, switches the payload and restarts the new broker when
previously running. Startup failure restores the old payload and broker.

Known old user tunnel services are retired only after exact file and loaded
process ownership verification: Linux openarbi-revenge-tunnel.service and macOS
io.openarbi.revenge-tunnel. Definitions are backed up under legacy-backups and
restored on activation failure. Modified services and unidentified processes are
left untouched with an explicit error. VPS forwarding services are never touched. Uninstall removes only managed software
and entrypoints; credentials, local workspace history and remote rooms remain.
A small management marker/lock directory remains to preserve lock safety.
Uninstall uses the same GUI lock and window checks, then stops the broker before
removing payloads. It preserves workspace history and removes only unchanged
managed PATH blocks; edited blocks are reported for manual repair.

## Build unpublished artifacts

Build hosts need the Go version in `client/go.mod` and Python 3.12+. macOS builds
require a native Mac with hdiutil/codesign. Use a reviewed public checkout:

```sh
python3 openarbi/build-release.py --version vX.Y.Z --platform linux-amd64 --output /tmp/openarbi-release
# On macOS:
python3 openarbi/build-release.py --version vX.Y.Z --platform darwin-arm64 --output /tmp/openarbi-release
```

The builder reads only public files. `upstream.lock.json` and `codex.lock.json`
pin downloaded artifacts; all are verified. Bundles contain native client,
broker, installer, Kitty, Codex, Starship, fonts, theme, artwork, chime, licenses
and corresponding public Kitty source. Approved upstream links are flattened to
ordinary files. Mac app bundles are ad-hoc signed and verified; no notarization
or Gatekeeper bypass is claimed. Outputs are a versioned platform tarball, native
platform installer, versioned `release.json` and `SHA256SUMS`. Building does not publish.

Real Ubuntu and Apple Silicon acceptance remains mandatory before publication:
installation, rendering/font discovery, clipboard, shortcuts, quit/restore,
reconnect, update and uninstall. Then pin the tested exact version in bootstrap
and verify its actual public download. Current fixture tests do not install on
the operator's computer. Atomic activation protects against process interruption,
not sudden disk/machine failure. macOS build/runtime acceptance needs Mac access.
