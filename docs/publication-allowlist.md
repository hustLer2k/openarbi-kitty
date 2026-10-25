# Client publication allowlist

The public repository starts from Kitty v0.43.1, commit
`7f24dd68c98d8f4712457560f25e8056cce52ec8`. No private Git history,
configuration, credentials, deployment manifests, database content, or workspace
images belong in the public repository.

The standalone module is `github.com/hustLer2k/openarbi-kitty/client`, Go 1.27.
The API client owns its wire representations. The terminal process owns its
native attachment and subprocess; credential storage remains in the OS store.
The publication API preserves uncertain outcomes and performs no automatic
mutation retry. Desktop allocation, broker, GUI and release work are separate.

## Exact source files

These are the only complete source files read for the extraction. Source paths
are relative to the private checkout; destinations have the same suffix after
`openarbi/`, under public `client/`. Import paths are rewritten to the public
module. Hashes identify the reviewed source snapshot, not the transformed output.

| Source path | Source SHA-256 |
| --- | --- |
| `openarbi/cmd/openarbi/main.go` | `32ee4bf7dd6bb47faca84d0443700641f144c4f9c6e72822eda234d7e1e4b4f5` |
| `openarbi/cmd/openarbi/main_test.go` | `328f908bd2f8b7042b17a537c24f1baa0e6c5b690d08a6f03b904ce976f29f96` |
| `openarbi/cmd/openarbi/native_rooms.go` | `16e63de6b95bdc4fbf40a7abba8f57144a951faefccaeedd2f545872adc9a019` |
| `openarbi/cmd/openarbi/native_rooms_test.go` | `ae8f0667baae5a70dddd85dbf5822197470b33d2db09889353c54bbc2e6b36cc` |
| `openarbi/cmd/openarbi/shareholder.go` | `75bb0cf459a56dc8f4579c04c4f05382d2535fa60698d19e742b199831f03242` |
| `openarbi/cmd/openarbi/shareholder_test.go` | `cf71bcb4906404001dc688de1baa2aea429f9e8ca32858cdab4cb421eddeab9f` |
| `openarbi/internal/client/client.go` | `bad23dd99e98be3ce345be1cada5690ced19b1278b4c69dd0073236e9c48d86b` |
| `openarbi/internal/client/client_test.go` | `f82ff86aeb6110e63fc885cf5ae3596bf055d6f67e02d4bb50139553eab04da3` |
| `openarbi/internal/client/credentials.go` | `500bd8dff9a6a53877ce107f856c2bd1b4739aa7eb1ff31881183bbb0530945a` |
| `openarbi/internal/client/native_rooms.go` | `b70feed44c096c6fcc2d0c550f6ea77bec16dd615f49ceb89697ed6597db0140` |
| `openarbi/internal/client/preferences.go` | `510246e13efae0d11b8bcb95801c5a10214178a31b6d889e9582b31a9655f182` |
| `openarbi/internal/client/preferences_test.go` | `09dfb64fe3ea15098681fd06d74dae7340f00725d0404a0a8daee9766548c7b7` |
| `openarbi/internal/client/shareholder.go` | `f1d0f373e1d14d65c183f2c3b2c472baadc220c0113769edc9b4b261990d1d82` |
| `openarbi/internal/client/shareholder_test.go` | `658b265175020a62180dbbd3949d2fb0687a76ab58ccf7a58301476c166991d5` |
| `openarbi/internal/roomtransport/client.go` | `6ea1381e6703dd2692198e9206dd6e08e4425b2e3db630153d34a4072a93c1cc` |
| `openarbi/internal/terminaltext/sanitize.go` | `fa78f24f9be332471937982bc49529037717446b2451e3e692059437cb85638f` |
| `openarbi/internal/terminaltext/sanitize_test.go` | `3ed7d994397b57672bb24aa9c4d69bec16eaa8049c6a3ed47f7303a5bd4237bf` |

The extracted `internal/client/client.go` omits unused legacy session creation,
command, snapshot, history, SSE and watcher APIs. Its relevant authentication,
idempotency, redirect, lost-acknowledgement and server-failure tests now exercise
native room requests. Session-only tests are omitted. Native Codex attach/queue,
shareholder publication, preferences and terminal sanitization tests remain.

## Declaration-only sources

Only the following declarations are permitted from these files. Their containing
packages are neither copied nor imported.

| Source path | Declarations | Public destination |
| --- | --- | --- |
| `openarbi/internal/model/model.go` | `ProtocolVersion`, `Profile`, `Capabilities`, `APIError`, `APIError.Error` | `client/internal/client/wire.go` |
| `openarbi/internal/native/types.go` | `CodexVersion`, `Workspace`, `Room`, `CreateRequest`, `Member`, `AttachInfo` | `client/internal/client/wire.go` |
| `openarbi/internal/shareholder/types.go` | `PublishRequest`, `PublishResponse` | `client/internal/client/wire.go` |
| `openarbi/internal/shareholder/types.go` | `identifier`, `roomIdentifier`, `Invalid`, `Validate` | `client/internal/client/shareholder_validation.go` |
| `openarbi/internal/roomtransport/server.go` | `DestinationPort`, `bridge` byte-copy helper only | `client/internal/roomtransport/bridge.go` |

`server.go` itself, gateway authorization, runtime grants, token configuration,
provider configuration, persistence interfaces and service implementations are
excluded. The byte-copy helper contains no authentication or lifecycle policy.

## Newly authored public files

- `client/go.mod` and `client/go.sum`: minimal client dependency graph, regenerated
  with `GOWORK=off go mod tidy`; no private module replacements.
- `client/internal/client/wire_test.go`: independent JSON and native-route checks.
- `client/internal/roomtransport/client_test.go`: ephemeral SSH echo fixture,
  host-key/ticket rejection, cancellation, reauthorization and changed-room checks.
- `client/internal/client/testdata/capabilities.json`
- `client/internal/client/testdata/room.json`
- `client/internal/client/testdata/create.json`
- `client/internal/client/testdata/member.json`
- `client/internal/client/testdata/attach.json`
- `client/internal/client/testdata/publication.json`
- `client/internal/client/testdata/publication_response.json`
- `client/internal/client/testdata/error.json`
- `docs/publication-allowlist.md`: this publication inventory.

Every fixture is synthetic. Attachment strings are explicitly invalid; SSH tests
create ephemeral keys in memory. No live credential store, production API or
provider is required by the tests.

## Verification

From `client/`, run:

```sh
GOWORK=off go test ./internal/client ./internal/terminaltext ./cmd/openarbi
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

Before publication, inspect every added path against this list and scan the
complete public additions for private module imports, private key blocks, live
credential patterns, private configuration, and unreviewed binaries/archives.
A source allowlist and synthetic fixtures supplement independent review; they do
not replace it. Keep the private CLI until worker packaging consumes a pinned
public artifact and its publication/continuation checks pass.

## Desktop integration publication (2026-10-25)

The following newly authored paths are allowlisted after independent review.
They contain no private backend packages, deployment state or credentials.
The existing public `client/cmd/openarbi/main.go` adds private Unix-socket routing
only. Test secrets are synthetic; tests never read the operator's credentials.

- `OPENARBI.md`
- `client/cmd/openarbi-desktop/main.go`
- `client/cmd/openarbi-desktop/main_test.go`
- `client/cmd/openarbi-installer/legacy_tunnel.go`
- `client/cmd/openarbi-installer/legacy_tunnel_test.go`
- `client/cmd/openarbi-installer/main.go`
- `client/cmd/openarbi-installer/main_test.go`
- `client/cmd/openarbi-installer/upgrade.go`
- `client/cmd/openarbi-installer/upgrade_test.go`
- `client/internal/client/desktop.go`
- `client/internal/client/desktop_test.go`
- `client/internal/desktop/rooms.go`
- `client/internal/desktop/rooms_test.go`
- `client/internal/desktop/socket.go`
- `client/internal/desktop/socket_test.go`
- `client/internal/desktop/store.go`
- `client/internal/desktop/tunnel.go`
- `client/internal/desktop/tunnel_test.go`
- `client/internal/desktop/workspace.go`
- `client/internal/desktop/workspace_test.go`
- `client/internal/install/install.go`
- `client/internal/install/install_test.go`
- `client/internal/install/legacy.go`
- `client/internal/install/legacy_test.go`
- `client/internal/install/migration_journal.go`
- `client/internal/install/path.go`
- `client/internal/install/path_test.go`
- `client/internal/install/release.go`
- `client/internal/install/release_test.go`
- `install.sh`
- `openarbi/INSTALLER.md`
- `openarbi/assets/fonts/JetBrainsMono-Bold.ttf`
- `openarbi/assets/fonts/JetBrainsMono-BoldItalic.ttf`
- `openarbi/assets/fonts/JetBrainsMono-Italic.ttf`
- `openarbi/assets/fonts/JetBrainsMono-Regular.ttf`
- `openarbi/assets/fonts/OFL.txt`
- `openarbi/assets/ready.wav`
- `openarbi/assets/shadow-fiend.png`
- `openarbi/build-release.py`
- `openarbi/codex.lock.json`
- `openarbi/desktop.py`
- `openarbi/fonts.conf`
- `openarbi/launch.sh`
- `openarbi/picker.py`
- `openarbi/shell/bashrc`
- `openarbi/shell/launch.sh`
- `openarbi/shell/starship.toml`
- `openarbi/shell/zsh/.zshrc`
- `openarbi/test_desktop.py`
- `openarbi/theme/kitty.conf`
- `openarbi/upstream.lock.json`

`openarbi/codex.lock.json` contains public npm artifact URLs and published
integrity hashes only. The original user-supplied PNG is copied byte-for-byte
(SHA-256 `ebdf548cd1f2e736d4ebb2602848f0553835e16b0d1e32d21cba074e11986640`).
JetBrains Mono font files and OFL text are verified against the locked upstream
2.304 archive; the completion WAV was generated for this application. No Git
bundle, private configuration, recovery material, build artifact or provider
credentials is included. Release construction includes an archive of this public
repository only. The desktop release remains unpublished pending live platform
and backend acceptance.

## macOS acceptance fixes (2026-10-25)

New public sources: `openarbi/macos-launcher.c`,
`openarbi/test_macos_bundle.py`, `client/internal/install/mac_links.go`, and
`client/internal/install/mac_links_test.go`. Existing builder/installer tests and
public documentation are updated for native macOS packaging, exact pinned
framework links, path aliases and two historical managed LaunchAgent templates.
Fixtures contain synthetic data only; no private configuration or recovery
material is copied. Acceptance evidence excludes identities and credentials.
