# Domain Context

## Terms

- **Local Cache** — the SQLite-backed record of contacts, chats, messages, media metadata, receipts, unread counts, and derived chat summary state used by the TUI.
- **Chat Summary** — the inbox row for a chat: title, latest message metadata, preview text, sender name, last activity time, unread count, and group flag.
- **Message Record** — one cached WhatsApp message, including text, sender identity, receipt state, timestamp, group flag, and optional media metadata.
- **History Batch** — older messages received from WhatsApp history sync or demo history loading. History batches populate the Local Cache without incrementing unread counts.

## Invariants

- The Local Cache owns derived Chat Summary state when a Message Record is recorded.
- History Batch messages never increment unread counts.
- Newer Message Records may update latest-message fields on the Chat Summary.
- Older Message Records must not overwrite a better Chat Summary title.
- Duplicate Message Records are no-ops for unread counts.
