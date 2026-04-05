# WhatsApp Terminal

`whatsapp-terminal` is a Go TUI client for personal WhatsApp accounts. It uses `whatsmeow` for the multi-device transport, keeps local app state in SQLite, and isolates transport, store, and UI code so the app can evolve without rewriting the stack.

## Supported Today

- QR pairing and reconnect from the terminal
- recent chat list with unread counts
- chat and contact search by name or JID
- direct and group thread views backed by the local cache
- send and receive text messages
- send images, documents, generic media, and voice notes
- stage clipboard screenshots before sending
- in-app file picker and path suggestions for attachments
- download media from the current thread
- recent-history bootstrap plus per-thread history requests
- local notifications through the terminal bell

## Current Limits

- no call support
- no broadcast/status UI
- no desktop-native notifications yet
- media rendering is text-first; attachments are tracked and downloadable, not previewed inline

## What Can Be Added Next

The pinned `whatsmeow` dependency already exposes transport support for more features than this TUI currently surfaces. Reasonable next additions include:

- group management and group event UI
- typing indicators and presence surfaces
- invite-link flows
- richer message-type rendering and retry/decryption diagnostics
- app-state controls such as pin, mute, and related chat metadata

The same dependency explicitly does **not** support calls, and broadcast list messaging is not supported by WhatsApp Web either.

## Quick Start

```bash
go run ./cmd/whatsapp-terminal
go build ./cmd/whatsapp-terminal
./whatsapp-terminal --debug
./whatsapp-terminal --demo --no-alt-screen
```

Useful flags:

- `--data-dir`: override the app data directory
- `--session-name`: keep multiple local profiles
- `--debug`: write structured debug logs to `debug.log`
- `--demo`: launch with seeded offline data for TUI work
- `--no-alt-screen`: render in the main terminal buffer instead of the alternate screen
- `--version`: print the embedded release version and exit

## Releases And Binaries

Tagged releases publish cross-platform binaries to GitHub Releases through GoReleaser.

- Linux: `amd64`, `arm64`
- macOS: `amd64`, `arm64`
- Windows: `amd64`, `arm64`

One-command installer for Linux and macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/ChiragJS/whatsapp-terminal/main/scripts/install.sh | sh
```

The installer detects OS and CPU, downloads the latest release archive plus `checksums.txt`, verifies the SHA-256 checksum, installs the binary to `/usr/local/bin` when writable or `~/.local/bin` otherwise, and prints the final path.

To pin a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/ChiragJS/whatsapp-terminal/main/scripts/install.sh | RELEASE_TAG=v0.1.0 sh
```

Release flow:

```bash
git tag v0.1.0
git push origin v0.1.0
```

That tag triggers the release workflow, which builds archives, publishes a GitHub Release, and attaches `checksums.txt`.

Users can download the right archive from the Releases page and run:

```bash
./whatsapp-terminal --version
```

## Development

```bash
go test ./...
go build ./...
go vet ./...
go run ./cmd/whatsapp-terminal --version
```

The app uses pure-Go SQLite via `modernc.org/sqlite`, so local builds do not need CGO.

## Licensing

This repository is licensed under MIT. It depends on `go.mau.fi/whatsmeow`, which is licensed under MPL-2.0. That dependency keeps its own license; this repository does not relicense upstream code.

If future changes copy or modify MPL-covered upstream files, those files must keep the MPL-2.0 notices and source-availability obligations. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the current dependency notice.

## Security And Risk Notes

This is not an official WhatsApp client. The dependency graph was checked with `govulncheck` during development, but unofficial-client and account-policy risk still exist. See [SECURITY.md](SECURITY.md).
