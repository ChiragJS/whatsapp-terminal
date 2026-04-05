# Repository Guidelines

## Project Structure & Module Organization

`cmd/whatsapp-terminal/` contains the CLI entrypoint. `internal/app/` wires config, storage, transport, and the Bubble Tea program together. `internal/transport/whatsapp/` is the only package allowed to import `whatsmeow`; keep upstream types contained there. `internal/store/` owns the app SQLite schema and chat cache. `internal/ui/` holds the TUI model and view logic. Long-form design notes live in `docs/`.

## Build, Test, and Development Commands

- `go run ./cmd/whatsapp-terminal`: run the client directly.
- `go run ./cmd/whatsapp-terminal --demo --no-alt-screen`: run the offline seeded TUI for local UI work.
- `go build ./...`: compile all packages and catch integration issues.
- `go test ./...`: run unit tests for storage and UI behavior.
- `go vet ./...`: catch common Go mistakes.
- `go test ./... -run TestStore`: narrow execution while iterating on persistence.

## Coding Style & Naming Conventions

Use standard Go formatting with tabs and `gofmt -w`. Keep exported APIs small and package-local helpers private unless they are used across package boundaries. Prefer explicit names such as `RequestHistory`, `RecordMessage`, and `resolveChatTitle` over short abstractions. Do not let `whatsmeow` structs escape `internal/transport/whatsapp/`; translate them into types from `internal/domain/`.

## Testing Guidelines

Add table-driven unit tests next to the package they cover. Storage tests should use `t.TempDir()` and a fresh SQLite database. UI tests should drive the model with Bubble Tea messages instead of relying on manual inspection. Live WhatsApp validation is opt-in only and must not be required for `go test ./...`.

## Commit & Pull Request Guidelines

Use Conventional Commits such as `feat: add history sync command` or `fix: clamp chat selection`. Keep pull requests focused, include a short behavior summary, list the commands you ran, and add terminal screenshots or recordings when the UI changes. Call out any protocol, session, or security-impacting changes explicitly.

## Security & Configuration Tips

Keep local data under the configured app data directory with restrictive permissions. Do not log message bodies at info level. Treat session databases and debug logs as sensitive files, and document any new network-facing behavior in `SECURITY.md`.
