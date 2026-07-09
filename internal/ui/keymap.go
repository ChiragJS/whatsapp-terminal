package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Action identifies a rebindable UI operation. Action IDs are the on-disk
// schema for keybindings.json and must stay stable across releases.
type Action string

// keyContext scopes a binding. A key is resolved against its current context
// first, then the global context, so global bindings work everywhere.
type keyContext string

const (
	contextGlobal   keyContext = "global"
	contextChatList keyContext = "chat"
	contextThread   keyContext = "thread"
	contextCompose  keyContext = "compose"
)

// Rebindable action IDs. Keep the string values stable; they are the config
// file keys.
const (
	ActionGlobalHelp        Action = "global.help"
	ActionGlobalRepaint     Action = "global.repaint"
	ActionGlobalKeybindings Action = "global.keybindings"

	ActionChatDown        Action = "chat.down"
	ActionChatUp          Action = "chat.up"
	ActionChatOpen        Action = "chat.open"
	ActionChatSearch      Action = "chat.search"
	ActionChatRefresh     Action = "chat.refresh"
	ActionChatTheme       Action = "chat.theme"
	ActionChatDiagnostics Action = "chat.diagnostics"

	ActionThreadScrollUp       Action = "thread.scroll_up"
	ActionThreadScrollDown     Action = "thread.scroll_down"
	ActionThreadPageUp         Action = "thread.page_up"
	ActionThreadPageDown       Action = "thread.page_down"
	ActionThreadOldest         Action = "thread.oldest"
	ActionThreadLatest         Action = "thread.latest"
	ActionThreadCompose        Action = "thread.compose"
	ActionThreadHistory        Action = "thread.history"
	ActionThreadDownloadLatest Action = "thread.download_latest"
	ActionThreadPlay           Action = "thread.play"
	ActionThreadSelect         Action = "thread.select"
	ActionThreadMediaPicker    Action = "thread.media_picker"

	ActionComposeSend       Action = "compose.send"
	ActionComposeNewline    Action = "compose.newline"
	ActionComposeFiles      Action = "compose.files"
	ActionComposePasteImage Action = "compose.paste_image"
	ActionComposeVoice      Action = "compose.voice"
)

// actionSpec is one row in the single source of truth for the action registry.
// DefaultKeymap, ValidateKeymap, the help screen, and the editor all iterate
// actionSpecs so ordering and grouping stay consistent.
type actionSpec struct {
	action   Action
	context  keyContext
	label    string
	defaults []string
}

var actionSpecs = []actionSpec{
	{ActionGlobalHelp, contextGlobal, "help", []string{"?"}},
	{ActionGlobalRepaint, contextGlobal, "force repaint", []string{"ctrl+l"}},
	{ActionGlobalKeybindings, contextGlobal, "edit keybindings", []string{"ctrl+k"}},

	{ActionChatDown, contextChatList, "move down", []string{"j", "down"}},
	{ActionChatUp, contextChatList, "move up", []string{"k", "up"}},
	{ActionChatOpen, contextChatList, "open thread", []string{"enter"}},
	{ActionChatSearch, contextChatList, "filter inbox", []string{"/"}},
	{ActionChatRefresh, contextChatList, "reload cache", []string{"r"}},
	{ActionChatTheme, contextChatList, "cycle theme", []string{"t", "T"}},
	{ActionChatDiagnostics, contextChatList, "dump diagnostics", []string{"D"}},

	{ActionThreadScrollUp, contextThread, "scroll up", []string{"k", "up"}},
	{ActionThreadScrollDown, contextThread, "scroll down", []string{"j", "down"}},
	{ActionThreadPageUp, contextThread, "page up", []string{"pgup", "ctrl+u"}},
	{ActionThreadPageDown, contextThread, "page down", []string{"pgdown", "ctrl+d"}},
	{ActionThreadOldest, contextThread, "jump to oldest", []string{"home"}},
	{ActionThreadLatest, contextThread, "jump to latest", []string{"end"}},
	{ActionThreadCompose, contextThread, "compose", []string{"i", "tab"}},
	{ActionThreadHistory, contextThread, "load older history", []string{"u"}},
	{ActionThreadDownloadLatest, contextThread, "download latest media", []string{"d"}},
	{ActionThreadPlay, contextThread, "play · stop voice", []string{"p"}},
	{ActionThreadSelect, contextThread, "select message", []string{"r"}},
	{ActionThreadMediaPicker, contextThread, "browse chat media", []string{"m"}},

	{ActionComposeSend, contextCompose, "send", []string{"enter"}},
	{ActionComposeNewline, contextCompose, "newline", []string{"ctrl+j", "alt+enter", "shift+enter"}},
	{ActionComposeFiles, contextCompose, "file picker", []string{"ctrl+o"}},
	{ActionComposePasteImage, contextCompose, "paste clipboard image", []string{"ctrl+v"}},
	{ActionComposeVoice, contextCompose, "record voice note", []string{"alt+v"}},
}

// contextTitles labels each context in the help and editor overlays.
var contextTitles = map[keyContext]string{
	contextGlobal:   "Global",
	contextChatList: "Inbox",
	contextThread:   "Thread",
	contextCompose:  "Compose",
}

// Keymap maps each action to the list of keys that trigger it.
type Keymap map[Action][]string

// reservedKeys can never be bound to an action: they are wired directly into
// the model (quit, exits) and stealing them would strand the user.
func reservedKeys() map[string]bool {
	return map[string]bool{"esc": true, "ctrl+c": true, "q": true}
}

// DefaultKeymap returns a fresh copy of the built-in bindings.
func DefaultKeymap() Keymap {
	km := make(Keymap, len(actionSpecs))
	for _, spec := range actionSpecs {
		keys := make([]string, len(spec.defaults))
		copy(keys, spec.defaults)
		km[spec.action] = keys
	}
	return km
}

// keymapIndex is the reverse lookup used at dispatch time: context+key → action.
type keymapIndex struct {
	byContext map[keyContext]map[string]Action
}

func buildKeymapIndex(km Keymap) keymapIndex {
	idx := keymapIndex{byContext: map[keyContext]map[string]Action{}}
	for _, spec := range actionSpecs {
		for _, key := range km[spec.action] {
			bucket := idx.byContext[spec.context]
			if bucket == nil {
				bucket = map[string]Action{}
				idx.byContext[spec.context] = bucket
			}
			bucket[key] = spec.action
		}
	}
	return idx
}

// actionFor resolves a key press in the given context, falling back to global
// bindings so that global actions work from every context.
func (idx keymapIndex) actionFor(ctx keyContext, key string) (Action, bool) {
	if bucket := idx.byContext[ctx]; bucket != nil {
		if action, ok := bucket[key]; ok {
			return action, true
		}
	}
	if ctx != contextGlobal {
		if bucket := idx.byContext[contextGlobal]; bucket != nil {
			if action, ok := bucket[key]; ok {
				return action, true
			}
		}
	}
	return "", false
}

// ValidateKeymap sanitizes a raw (possibly user-authored) keymap. Unknown
// actions are dropped and reported; empty/reserved keys and conflicts are
// reported and the offending action falls back to its default. The returned
// keymap always contains every known action.
func ValidateKeymap(raw Keymap) (Keymap, []string) {
	var problems []string

	known := map[Action]bool{}
	for _, spec := range actionSpecs {
		known[spec.action] = true
	}
	var unknown []string
	for action := range raw {
		if !known[action] {
			unknown = append(unknown, string(action))
		}
	}
	sort.Strings(unknown)
	for _, u := range unknown {
		problems = append(problems, fmt.Sprintf("unknown action %q ignored", u))
	}

	clean := make(Keymap, len(actionSpecs))
	globalKeys := map[string]Action{}
	contextKeys := map[keyContext]map[string]Action{}
	reserved := reservedKeys()

	for _, spec := range actionSpecs {
		rawKeys, present := raw[spec.action]
		var keys []string
		if present {
			for _, key := range rawKeys {
				k := strings.TrimSpace(key)
				switch {
				case k == "":
					problems = append(problems, fmt.Sprintf("%s: empty key ignored", spec.action))
				case reserved[k]:
					problems = append(problems, fmt.Sprintf("%s: reserved key %q ignored", spec.action, k))
				case keyConflict(spec.context, k, globalKeys, contextKeys):
					problems = append(problems, fmt.Sprintf("%s: key %q already bound, ignored", spec.action, k))
				default:
					keys = append(keys, k)
					registerKey(spec.context, k, spec.action, globalKeys, contextKeys)
				}
			}
			if len(keys) == 0 {
				problems = append(problems, fmt.Sprintf("%s: no valid keys, using defaults", spec.action))
			}
		}
		if len(keys) == 0 {
			keys = append(keys, spec.defaults...)
			for _, k := range spec.defaults {
				registerKey(spec.context, k, spec.action, globalKeys, contextKeys)
			}
		}
		clean[spec.action] = keys
	}
	return clean, problems
}

// keyConflict reports whether key is already claimed in a way that would clash
// with a binding in ctx: any global binding conflicts with every context, and
// within a non-global context duplicates conflict too.
func keyConflict(ctx keyContext, key string, globalKeys map[string]Action, contextKeys map[keyContext]map[string]Action) bool {
	if _, ok := globalKeys[key]; ok {
		return true
	}
	if ctx == contextGlobal {
		for _, bucket := range contextKeys {
			if _, ok := bucket[key]; ok {
				return true
			}
		}
		return false
	}
	if bucket := contextKeys[ctx]; bucket != nil {
		if _, ok := bucket[key]; ok {
			return true
		}
	}
	return false
}

func registerKey(ctx keyContext, key string, action Action, globalKeys map[string]Action, contextKeys map[keyContext]map[string]Action) {
	if ctx == contextGlobal {
		globalKeys[key] = action
		return
	}
	if contextKeys[ctx] == nil {
		contextKeys[ctx] = map[string]Action{}
	}
	contextKeys[ctx][key] = action
}

func keymapFilePath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "keybindings.json")
}

// LoadKeymap reads keybindings.json from dataDir. A missing file yields the
// defaults with no problems; an unreadable or malformed file yields the
// defaults plus a problem and never overwrites the user's file.
func LoadKeymap(dataDir string) (Keymap, []string) {
	path := keymapFilePath(dataDir)
	if path == "" {
		return DefaultKeymap(), nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- constrained to configured data dir
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultKeymap(), nil
		}
		return DefaultKeymap(), []string{fmt.Sprintf("keybindings: cannot read %s: %v", path, err)}
	}
	var raw Keymap
	if err := json.Unmarshal(data, &raw); err != nil {
		return DefaultKeymap(), []string{fmt.Sprintf("keybindings: invalid JSON in %s: %v", path, err)}
	}
	return ValidateKeymap(raw)
}

// SaveKeymap writes the keymap to keybindings.json atomically (temp + rename)
// with owner-only permissions.
func SaveKeymap(dataDir string, km Keymap) error {
	path := keymapFilePath(dataDir)
	if path == "" {
		return fmt.Errorf("no data directory configured")
	}
	data, err := json.MarshalIndent(km, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// EnsureKeymapFile writes the default keybindings file if none exists, so the
// user has a discoverable starting point. An existing file is left untouched.
func EnsureKeymapFile(dataDir string) {
	path := keymapFilePath(dataDir)
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	_ = SaveKeymap(dataDir, DefaultKeymap())
}

// keymapFileInfo returns the modification time of the keybindings file for the
// live-reload poll.
func keymapFileInfo(dataDir string) (time.Time, bool) {
	path := keymapFilePath(dataDir)
	if path == "" {
		return time.Time{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// keyLabel returns the first (primary) key of a binding for compact chip help.
func keyLabel(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// isDefaultBinding reports whether keys exactly match defaults.
func isDefaultBinding(keys, defaults []string) bool {
	if len(keys) != len(defaults) {
		return false
	}
	for i := range keys {
		if keys[i] != defaults[i] {
			return false
		}
	}
	return true
}
