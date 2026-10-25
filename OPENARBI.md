# OpenArbi desktop

OpenArbi is a Kitty-based desktop for authenticated remote Codex workspaces.
The standalone Go client lives in `client/`; the backend and worker images are
private and are not part of this repository. Kitty's upstream history and GPL
license are preserved, pinned at v0.43.1.

Desktop packaging is under development. The `v0.1.0-client1` prerelease is the
standalone command-line client, **not an installable desktop release**. Do not
use the bootstrap until a desktop version has been published and validated for
your platform. See [installation and release instructions](openarbi/INSTALLER.md).

The desktop uses your system OpenSSH `revenge` alias and OS credential store.
Tabs hold independent native Codex attachments. Quitting preserves the layout;
closing an individual tab removes it from restoration while retaining its room.
The room picker opens existing rooms in new tabs and provides explicit stop and
recovery actions. A deliberately idle-suspended room wakes automatically; a lost
worker requires explicit recovery.

| Action | Linux and macOS | Additional macOS |
| --- | --- | --- |
| New room tab/window | Ctrl+Shift+T / N | Cmd+T / N |
| Previous/next tab | Ctrl+Shift+Q / E | Cmd+Shift+[ / ] |
| Select tab | Ctrl+Shift+1–9 | Cmd+1–9 |
| Close/reopen tab | Ctrl+Shift+W / R | Cmd+W / Cmd+Shift+T |
| Local Bash/Zsh tab | Ctrl+Shift+S | |
| Room picker | Ctrl+Shift+P | |

All shortcuts belong to the application. Ordinary letters, Ctrl+C, Ctrl+D and
Tab remain available to the foreground program. Local shells retain your shell
configuration and initialize the bundled Starship prompt in OpenArbi only.

Build and check the client with `GOWORK=off go test -race ./...` and
`GOWORK=off go vet ./...` from `client/`. Run GUI regression checks with
`python3 -m unittest openarbi/test_desktop.py` from the repository root. Release
construction uses locked upstream artifacts and a clean public checkout;
macOS builds require a Mac for application signing and acceptance testing.

The bundled Shadow Fiend artwork and exact palette were supplied for this
application. JetBrains Mono includes its OFL license. Distribution retains
Kitty, Codex and Starship licenses and corresponding Kitty source.
