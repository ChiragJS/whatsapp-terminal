# Architecture

The project is split into four layers:

- `internal/config`: CLI flags and filesystem defaults.
- `internal/transport/whatsapp`: connection lifecycle, QR pairing, `whatsmeow` event handling, and translation into domain events.
- `internal/store`: local SQLite cache for contacts, chats, and messages.
- `internal/ui`: Bubble Tea state machine and terminal rendering.

## Event Flow

1. The app starts the transport adapter.
2. The adapter connects to WhatsApp and emits domain events.
3. Incoming history sync, message, and receipt events update the local cache.
4. The UI listens to transport events and reloads affected chat/message views from the cache.

This keeps the TUI independent from upstream transport types and makes offline cache behavior straightforward to test.
