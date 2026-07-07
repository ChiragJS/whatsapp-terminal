# TODO

Deferred items captured during the Phosphor Press redesign.

## UI feedback

- **Refresh confirmation.** `r` in the chat list re-queries SQLite via `loadChatsCmd` but renders no visible status, so it looks like a no-op when the local cache is unchanged. Add a transient status hint (e.g. `m.status = "Refreshed inbox"`) so the action is observable.
