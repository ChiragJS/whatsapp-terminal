# Debugging the TUI

Two complementary tracks: a built-in diagnostic log you toggle with `--debug`,
and Delve for interactive breakpoints. The log usually answers "what data is
the UI seeing?"; Delve answers "what is the UI doing right now?".

## 1. Structured debug log

### Turn it on

```sh
go run ./cmd/whatsapp-terminal --debug
```

This opens `<data-dir>/debug.log` and routes every `slog` call there. The UI
emits its own events under the `component=ui` attribute, including a per-row
breakdown of why `displayChatTitle` did or did not mask a value.

The default data dir is platform-dependent (e.g. `~/.config/whatsapp-terminal`
on Linux); use `--data-dir /tmp/wt-debug` to send everything to a throwaway
directory while iterating.

### `D` — dump current chat-list state

While the chat list is focused, press `D` (uppercase). This writes one log
record per loaded chat with the raw field values (`title_raw`, `title_quoted`,
`sender_raw`, `sender_quoted`, etc.) and the resolved display strings
(`display_title`, `display_sender`). Use it when a specific row is rendering
incorrectly — the dump shows exactly what the renderer received.

```sh
# In one terminal:
go run ./cmd/whatsapp-terminal --debug --data-dir /tmp/wt-debug

# Tail the log in another terminal:
tail -F /tmp/wt-debug/debug.log | grep ui:
```

### Useful filters

```sh
# Only the fallback decisions
grep 'ui:displayChatTitle' debug.log

# Why a specific JID rendered strangely
grep '120363405662701156' debug.log

# Full diagnostic dump triggered by the D key
grep 'ui:dump' debug.log
```

The `pkgLog` package-level logger is installed when `WithLogger` runs at
startup, so helpers without a `Model` receiver (`displayChatTitle`,
`displaySenderLabel`, …) all flow into the same file.

## 2. Delve

A TUI fights with Delve for the terminal, so always run Delve **headless**
and attach a second client.

### Launch headless

```sh
dlv exec --headless --api-version=2 --listen=127.0.0.1:2345 -- \
    ./whatsapp-terminal --demo --stress --data-dir /tmp/wt-stress
```

The `--` separates Delve's flags from the binary's. Build first
(`go build -o whatsapp-terminal ./cmd/whatsapp-terminal`) so Delve's own
output isn't interleaved with `go run`'s build chatter.

### Attach from another terminal

```sh
dlv connect 127.0.0.1:2345
```

In the prompt:

```text
(dlv) break ui.displayChatTitle
(dlv) continue
(dlv) print chat.JID
(dlv) print chat.Title
(dlv) print title
(dlv) stepout
```

The TUI keeps responding in its own terminal while you stop at breakpoints
in Delve. `continue` resumes; the TUI window will only redraw when the model
sees a new message, so use the chat list or compose keys to drive an event.

### Useful breakpoints

| Symptom | Breakpoint | What to inspect |
| --- | --- | --- |
| Wrong title rendered | `ui.displayChatTitle` | `chat.JID`, `chat.Title`, `title` |
| Wrong sender label | `ui.displaySenderLabel` | `name`, `user` |
| Frame layout drift | `ui.renderChatItem` | `width`, `topLine`, `botLine` |
| Panel size mismatch | `ui.renderPanel` | `totalWidth`, `innerHeight`, `content` |
| Wrong chat opened | `ui.(*Model).updateChatList` | `m.selected`, `m.currentChatID` |

### IDE integration

Both VS Code (Go extension) and GoLand can speak the Delve API. Point them
at `127.0.0.1:2345`. Once attached you get clickable breakpoints, watch
expressions, and goroutine views.

## 3. When something looks wrong, capture this

A useful bug report bundle:

1. Run with `--debug --data-dir /tmp/wt-debug`.
2. Reproduce the issue.
3. Press `D` once chats are visible.
4. Quit (`Esc` then `q`).
5. Share the last ~200 lines of `/tmp/wt-debug/debug.log` plus a screenshot.

That's enough to pin down whether the problem is data (something in the
cache is off), display (the helpers misclassify a value), or layout
(panel-height math is drifting). Each layer has its own log prefix
(`store:`, `ui:`, etc.) for fast filtering.
