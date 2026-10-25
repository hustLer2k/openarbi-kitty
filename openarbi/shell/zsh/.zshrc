# Preserve the user's interactive configuration without modifying it.
if [[ -f "${OPENARBI_ORIGINAL_ZDOTDIR:-$HOME}/.zshrc" ]]; then
  source "${OPENARBI_ORIGINAL_ZDOTDIR:-$HOME}/.zshrc"
fi
: "${OPENARBI_BUNDLE_DIR:?OpenArbi installation directory is required}"
export STARSHIP_CONFIG="$OPENARBI_BUNDLE_DIR/share/openarbi/shell/starship.toml"
if openarbi_prompt_init=$("$OPENARBI_BUNDLE_DIR/bin/starship" init zsh); then
  eval "$openarbi_prompt_init"
else
  printf '%s\n' 'OpenArbi could not initialize its bundled prompt; repair or update the installation.' >&2
fi
unset openarbi_prompt_init
