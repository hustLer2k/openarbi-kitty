#!/bin/sh
# Public bootstrap. A release operator pins OPENARBI_VERSION here only after
# uploading and verifying that exact release; no unpublished/latest default.
set -eu
version=${OPENARBI_VERSION:-}
base=${OPENARBI_RELEASE_BASE:-https://github.com/hustLer2k/openarbi-kitty/releases/download}
if [ -z "$version" ]; then
    echo 'No desktop release is pinned yet. Set OPENARBI_VERSION to an explicitly published release.' >&2
    exit 1
fi
case "$version" in *[!a-zA-Z0-9.-]*|.*|-*) echo 'Invalid release version' >&2; exit 1;; esac
case "$base" in https://*) ;; *) echo 'Release base must use HTTPS' >&2; exit 1;; esac
case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) platform=linux-amd64 ;;
    Darwin/arm64) platform=darwin-arm64 ;;
    *) echo 'OpenArbi supports Linux amd64 and macOS Apple Silicon.' >&2; exit 1 ;;
esac
command -v curl >/dev/null 2>&1 || { echo 'Install curl first.' >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then checksum=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum=shasum
else echo 'Install sha256sum or shasum first.' >&2; exit 1
fi
umask 077
tmp=$(mktemp -d "${TMPDIR:-/tmp}/openarbi-bootstrap.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
release="$base/$version"
installer="openarbi-installer-$platform"
payload="openarbi-$version-$platform.tar.gz"
printf '%s\n' 'checking     Selecting native installer' 'downloading  Fetching release checksums'
curl --proto '=https' --proto-redir '=https' -fsSL "$release/SHA256SUMS" -o "$tmp/SHA256SUMS"
expected() {
    awk -v name="$1" '$2 == name { n++; value=$1 } END { if(n!=1 || length(value)!=64 || value ~ /[^a-fA-F0-9]/) exit 1; print value }' "$tmp/SHA256SUMS"
}
installer_sum=$(expected "$installer") || { echo 'Installer checksum missing or invalid' >&2; exit 1; }
payload_sum=$(expected "$payload") || { echo 'Payload checksum missing or invalid' >&2; exit 1; }
curl --proto '=https' --proto-redir '=https' -fsSL "$release/$installer" -o "$tmp/installer"
if [ "$checksum" = sha256sum ]; then actual=$(sha256sum "$tmp/installer" | awk '{print $1}')
else actual=$(shasum -a 256 "$tmp/installer" | awk '{print $1}')
fi
[ "$actual" = "$installer_sum" ] || { echo 'Installer checksum mismatch' >&2; exit 1; }
printf '%s\n' 'verifying    Native installer checksum verified'
chmod 700 "$tmp/installer"
"$tmp/installer" --version "$version" --url "$release/$payload" --sha256 "$payload_sum" "$@"
