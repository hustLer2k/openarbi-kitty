#!/bin/sh
set -eu
# Resolve the managed launcher symlink without depending on GNU readlink -f.
launcher=$0
while [ -L "$launcher" ]; do
  target=$(readlink "$launcher")
  case $target in /*) launcher=$target ;; *) launcher=$(dirname "$launcher")/$target ;; esac
done
bundle=$(CDPATH= cd -- "$(dirname "$launcher")/.." && pwd -P)
export OPENARBI_BUNDLE_DIR=$bundle
export KITTY_CONFIG_DIRECTORY="$bundle/share/openarbi/theme"
case $(uname -s) in
  Linux)
    export FONTCONFIG_FILE="$bundle/share/openarbi/fonts.conf"
    desktop_dir=${OPENARBI_DESKTOP_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/OpenArbi/desktop}
    ;;
  Darwin) desktop_dir=${OPENARBI_DESKTOP_DIR:-$HOME/Library/Application Support/OpenArbi/desktop} ;;
  *) printf '%s\n' 'OpenArbi supports Linux and macOS.' >&2; exit 1 ;;
esac
# Keep window/font caches separate from an independently installed Kitty.
export KITTY_CACHE_DIRECTORY="$desktop_dir/kitty-cache"
# Preserve operational subcommands used by the shareholder hooks.
case ${1:-} in
  shareholder|login|logout|rooms|create|attach|queue|stop|resume|members)
    "$bundle/bin/openarbi-desktop" --directory "$desktop_dir" ensure
    exec "$bundle/bin/openarbi-client" --desktop-socket "$desktop_dir/broker.sock" "$@" ;;
  --server|--help|-h) exec "$bundle/bin/openarbi-client" "$@" ;;
  update|uninstall) exec "$bundle/bin/openarbi-installer" "$@" ;;
  '') exec "$bundle/bin/kitty" +launch "$bundle/share/openarbi/desktop.py" ;;
  *) printf '%s\n' 'Usage: openarbi [update|uninstall|rooms|attach|queue|shareholder|login|logout]' >&2; exit 1 ;;
esac
