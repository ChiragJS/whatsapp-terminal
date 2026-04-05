# WhatsApp Terminal

`whatsapp-terminal` is a Go TUI client for personal WhatsApp accounts. It uses `whatsmeow` for the multi-device transport layer, stores local state in SQLite, and keeps the UI, transport, and persistence layers separated so the project can evolve without rewriting the whole stack.

## Current MVP

- QR pairing from inside the terminal
- chat list with unread counts
- person/chat search by name or JID
- conversation view with cached history
- send and receive text messages
- on-demand older-history requests from the primary device

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
- `--demo`: launch with seeded offline data for TUI testing
- `--no-alt-screen`: keep rendering in the main terminal buffer

## Development

```bash
go test ./...
go build ./...
go vet ./...
```

The app uses pure-Go SQLite via `modernc.org/sqlite`, so local builds do not need CGO.

## Security And Risk Notes

This project is not an official WhatsApp client. The dependency graph was checked with `govulncheck` during development, but unofficial-client/account risk still exists and is documented in [SECURITY.md](SECURITY.md).
