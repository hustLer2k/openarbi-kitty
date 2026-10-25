#!/bin/sh
set -eu
: "${OPENARBI_BUNDLE_DIR:?OpenArbi installation directory is required}"
case ${SHELL:-} in
  */bash) exec "$SHELL" --rcfile "$OPENARBI_BUNDLE_DIR/share/openarbi/shell/bashrc" -i ;;
  */zsh)
    export OPENARBI_ORIGINAL_ZDOTDIR=${ZDOTDIR:-$HOME}
    export ZDOTDIR="$OPENARBI_BUNDLE_DIR/share/openarbi/shell/zsh"
    exec "$SHELL" -i ;;
  *) printf '%s\n' 'Choose Bash or Zsh as your shell before opening a local OpenArbi tab.' >&2; exit 1 ;;
esac
