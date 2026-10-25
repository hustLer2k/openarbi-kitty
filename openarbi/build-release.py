#!/usr/bin/env python3
"""Build an unpublished, checksummed desktop bundle from locked public artifacts.
macOS bundles must be built on macOS (hdiutil and ad-hoc codesign required).
"""
import argparse
import base64
import hashlib
import gzip
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent


def run(*args, cwd=None, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def fetch(spec, destination):
    url = spec["url"]
    if not url.startswith("https://"):
        raise ValueError("artifact URLs must use HTTPS")
    with urllib.request.urlopen(url, timeout=120) as source, destination.open("wb") as target:
        if not source.url.startswith("https://"):
            raise ValueError("non-HTTPS artifact redirect")
        shutil.copyfileobj(source, target)
    data = destination.read_bytes()
    if "sha256" in spec:
        valid = hashlib.sha256(data).hexdigest() == spec["sha256"]
    else:
        algorithm, expected = spec["integrity"].split("-", 1)
        if algorithm != "sha512":
            raise ValueError("unsupported integrity algorithm")
        valid = base64.b64encode(hashlib.sha512(data).digest()).decode() == expected
    if not valid:
        raise ValueError(f"checksum mismatch: {destination.name}")


def unpack(archive, destination):
    destination.mkdir(parents=True, exist_ok=True)
    # data_filter rejects traversal, escaping links and device files. Python 3.12+
    # is a release-build dependency only, never an end-user requirement.
    with tarfile.open(archive) as source:
        source.extractall(destination, filter="data")


def write_executable(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o755)


def copy_license(source, destination):
    paths = sorted(p for p in source.rglob("*") if p.is_file() and p.name.lower().startswith(("license", "copying", "notice")))
    if not paths:
        raise ValueError(f"no upstream license in {source}")
    destination.mkdir(parents=True, exist_ok=True)
    for i, path in enumerate(paths):
        shutil.copy2(path, destination / f"{i}-{path.name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--release-base", default="https://github.com/hustLer2k/openarbi-kitty/releases/download")
    parser.add_argument("--platform", choices=["linux-amd64", "darwin-arm64"], required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--allow-dirty-tests", action="store_true", help="allow unpublished fixture builds from an uncommitted checkout")
    args = parser.parse_args()
    if not re.fullmatch(r"v?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", args.version):
        parser.error("version must be explicit semver")
    if args.platform == "darwin-arm64" and sys.platform != "darwin":
        parser.error("macOS bundles require a macOS build host for DMG extraction and signing")
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT))
    if dirty and not args.allow_dirty_tests:
        parser.error("release builds require a clean committed public checkout; --allow-dirty-tests is for unpublished fixtures only")
    lock = json.loads((ROOT / "openarbi/upstream.lock.json").read_text())
    codex = json.loads((ROOT / "openarbi/codex.lock.json").read_text())
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="openarbi-release-") as temporary:
        work = Path(temporary)
        bundle = work / "payload"
        (bundle / "bin").mkdir(parents=True)
        (bundle / "share/openarbi").mkdir(parents=True)
        licenses = bundle / "share/licenses"
        licenses.mkdir(parents=True)
        for name in ("theme", "assets", "shell"):
            shutil.copytree(ROOT / "openarbi" / name, bundle / "share/openarbi" / name)
        for name in ("desktop.py", "picker.py", "fonts.conf"):
            shutil.copy2(ROOT / "openarbi" / name, bundle / "share/openarbi" / name)
        artwork = bundle / "share/openarbi" / lock["artwork"]["file"]
        if hashlib.sha256(artwork.read_bytes()).hexdigest() != lock["artwork"]["sha256"]:
            raise ValueError("artwork differs from lock")
        shutil.copy2(ROOT / "openarbi/launch.sh", bundle / "bin/openarbi")
        (bundle / "bin/openarbi").chmod(0o755)
        env = dict(os.environ, GOWORK="off", CGO_ENABLED="0", GOOS=args.platform.split("-")[0], GOARCH=args.platform.split("-")[1])
        for binary, package in (("openarbi-client", "openarbi"), ("openarbi-desktop", "openarbi-desktop"), ("openarbi-installer", "openarbi-installer")):
            run("go", "build", "-trimpath", "-ldflags=-s -w -buildid=", "-o", str(bundle / "bin" / binary), "./cmd/" + package, cwd=ROOT / "client", env=env)
        kitty_package = work / "kitty-package"
        fetch(lock["kitty"]["packages"][args.platform], kitty_package)
        kitty_dir = bundle / "lib/kitty"
        if args.platform == "linux-amd64":
            unpack(kitty_package, kitty_dir)
            kitty_binary = "lib/kitty/bin/kitty"
        else:
            mount = work / "mount"
            mount.mkdir()
            run("hdiutil", "attach", "-readonly", "-nobrowse", "-mountpoint", str(mount), str(kitty_package))
            try:
                kitty_dir.mkdir(parents=True)
                shutil.copytree(mount / "kitty.app", kitty_dir / "kitty.app", symlinks=False)
            finally:
                run("hdiutil", "detach", str(mount))
            kitty_binary = "lib/kitty/kitty.app/Contents/MacOS/kitty"
        write_executable(bundle / "bin/kitty", '#!/bin/sh\nset -eu\nbase=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\nexec "$base/' + kitty_binary + '" "$@"\n')
        starship_archive = work / "starship.tar.gz"
        fetch(lock["starship"]["packages"][args.platform], starship_archive)
        starship_dir = work / "starship"
        unpack(starship_archive, starship_dir)
        shutil.copy2(starship_dir / "starship", bundle / "bin/starship")
        codex_key = {"linux-amd64": "Linux/x86_64", "darwin-arm64": "Darwin/arm64"}[args.platform]
        codex_archive = work / "codex.tgz"
        fetch(codex["packages"][codex_key], codex_archive)
        codex_dir = work / "codex"
        unpack(codex_archive, codex_dir)
        binaries = [p for p in codex_dir.rglob("codex") if p.is_file() and p.parent.name == "bin"]
        if len(binaries) != 1:
            raise ValueError("expected exactly one native Codex binary")
        shutil.copytree(binaries[0].parent.parent, bundle / "lib/codex")
        write_executable(bundle / "bin/codex", '#!/bin/sh\nset -eu\nbase=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\nexec "$base/lib/codex/bin/codex" "$@"\n')
        for name, digest in {"LICENSE": "d17f227e4df5da1600391338865ce0f3055211760a36688f816941d58232d8dc", "NOTICE": "9d71575ecfd9a843fc1677b0efb08053c6ba9fd686a0de1a6f5382fd3c220915"}.items():
            fetch({"url": "https://raw.githubusercontent.com/openai/codex/rust-v0.164.0/" + name, "sha256": digest}, licenses / ("codex-" + name))
        copy_license(codex_dir, licenses / "codex")
        shutil.copy2(ROOT / "LICENSE", licenses / "kitty-GPL-3.0.txt")
        font_archive = work / "fonts.zip"
        fetch(lock["fonts"], font_archive)
        with zipfile.ZipFile(font_archive) as fonts:
            for bundled in (bundle / "share/openarbi/assets/fonts").iterdir():
                candidates = [name for name in fonts.namelist() if Path(name).name == bundled.name]
                if len(candidates) != 1 or fonts.read(candidates[0]) != bundled.read_bytes():
                    raise ValueError(f"bundled font/license differs from locked upstream: {bundled.name}")
        shutil.copy2(ROOT / "openarbi/assets/fonts/OFL.txt", licenses / "JetBrainsMono-OFL.txt")
        # Pin Starship's MIT text to its release tag; require its copyright notice.
        fetch({"url": "https://raw.githubusercontent.com/starship/starship/v1.27.0/LICENSE",
               "sha256": "2587580d0ec4ddc43d1534745a19ffeec0b63186497c2ed4bc36832da91b13e2"},
              licenses / "starship-MIT.txt")
        # Corresponding source is a clean archive of this public repository only.
        run("git", "archive", "--format=tar.gz", "-o", str(bundle / "share/kitty-source.tar.gz"), "HEAD", cwd=ROOT)
        for name in ("upstream.lock.json", "codex.lock.json"):
            shutil.copy2(ROOT / "openarbi" / name, bundle / "share/openarbi" / name)
        (bundle / "share/openarbi/release.json").write_text(json.dumps({"version": args.version, "platform": args.platform, "unpublished_dirty_fixture": dirty, "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()}, indent=2) + "\n")
        if args.platform == "darwin-arm64":
            contents = bundle / "OpenArbi.app/Contents"
            write_executable(contents / "MacOS/openarbi", '#!/bin/sh\nset -eu\nbase=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)\nbase=$(CDPATH= cd -- "$base/../../.." && pwd -P)\nexec "$base/bin/openarbi" "$@"\n')
            shutil.copytree(bundle / "share/openarbi/assets/fonts", contents / "Resources/fonts")
            (contents / "Info.plist").write_bytes(plistlib.dumps({"CFBundleName": "OpenArbi", "CFBundleDisplayName": "OpenArbi", "CFBundleIdentifier": "com.openarbi.desktop", "CFBundleVersion": args.version.lstrip("v"), "CFBundleExecutable": "openarbi", "CFBundlePackageType": "APPL", "NSHighResolutionCapable": True, "ATSApplicationFontsPath": "Resources/fonts", "NSRequiresAquaSystemAppearance": False}))
            embedded = kitty_dir / "kitty.app/Contents"
            shutil.copytree(bundle / "share/openarbi/assets/fonts", embedded / "Resources/OpenArbiFonts")
            embedded_info = plistlib.loads((embedded / "Info.plist").read_bytes())
            embedded_info.update({"CFBundleName": "OpenArbi", "CFBundleDisplayName": "OpenArbi", "ATSApplicationFontsPath": "Resources/OpenArbiFonts", "NSRequiresAquaSystemAppearance": False})
            (embedded / "Info.plist").write_bytes(plistlib.dumps(embedded_info))
            run("codesign", "--force", "--deep", "--sign", "-", str(kitty_dir / "kitty.app"))
            run("codesign", "--verify", "--deep", "--strict", str(kitty_dir / "kitty.app"))
            run("codesign", "--force", "--deep", "--sign", "-", str(bundle / "OpenArbi.app"))
            run("codesign", "--verify", "--deep", "--strict", str(bundle / "OpenArbi.app"))
        artifact = output / f"openarbi-{args.version}-{args.platform}.tar.gz"
        # The native installer rejects all archive links. Dereference trusted
        # upstream links during release construction after data-filter validation.
        epoch = int(subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], cwd=ROOT))
        def normalize(info):
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            info.mtime = epoch
            return info
        with artifact.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", dereference=True) as tar:
                for child in sorted(bundle.iterdir()):
                    tar.add(child, arcname=child.name, filter=normalize)
        installer = output / f"openarbi-installer-{args.platform}"
        shutil.copy2(bundle / "bin/openarbi-installer", installer)
        manifest_path = output / "release.json"
        manifest = {"version": args.version, "packages": {}}
        if manifest_path.exists():
            manifest = json.loads(manifest_path.read_text())
            if manifest["version"] != args.version:
                raise ValueError("output directory contains a different release version")
        if not args.release_base.startswith("https://"):
            raise ValueError("release base must use HTTPS")
        manifest["packages"][args.platform] = {"url": args.release_base.rstrip("/") + "/" + args.version + "/" + artifact.name,
                                                   "sha256": hashlib.sha256(artifact.read_bytes()).hexdigest()}
        manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        sums = output / "SHA256SUMS"
        files = sorted(p for p in output.iterdir() if (p.name.startswith("openarbi-") or p.name == "release.json") and p.is_file())
        sums.write_text("".join(hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name + "\n" for p in files))
        print(artifact)
        print("Built locally; publication and platform acceptance are separate steps.")


if __name__ == "__main__":
    main()
