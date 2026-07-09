package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

// actionSpecIndex returns the position of action in actionSpecs, used to drive
// the editor selection in tests.
func actionSpecIndex(t *testing.T, action Action) int {
	t.Helper()
	for i, spec := range actionSpecs {
		if spec.action == action {
			return i
		}
	}
	t.Fatalf("action %q not found in actionSpecs", action)
	return -1
}

func withKeymap(m Model, km Keymap) Model {
	m.keymap = km
	m.keyIndex = buildKeymapIndex(km)
	return m
}

func TestKeymapSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	km := DefaultKeymap()
	km[ActionChatOpen] = []string{"o"}
	km[ActionThreadPlay] = []string{"space", "p"}

	if err := SaveKeymap(dir, km); err != nil {
		t.Fatalf("SaveKeymap() error = %v", err)
	}
	loaded, problems := LoadKeymap(dir)
	if len(problems) != 0 {
		t.Fatalf("LoadKeymap() problems = %v, want none", problems)
	}
	if !reflect.DeepEqual(loaded, km) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", loaded, km)
	}
}

func TestValidateKeymap(t *testing.T) {
	t.Parallel()

	assertComplete := func(t *testing.T, km Keymap) {
		t.Helper()
		if len(km) != len(actionSpecs) {
			t.Fatalf("keymap size = %d, want %d (every action present)", len(km), len(actionSpecs))
		}
		for _, spec := range actionSpecs {
			if len(km[spec.action]) == 0 {
				t.Fatalf("action %q has no keys after validation", spec.action)
			}
		}
	}

	t.Run("unknown action dropped and reported", func(t *testing.T) {
		t.Parallel()
		clean, problems := ValidateKeymap(Keymap{"bogus.action": {"z"}})
		if _, ok := clean["bogus.action"]; ok {
			t.Fatal("unknown action leaked into clean keymap")
		}
		if !containsSubstr(problems, "unknown action") {
			t.Fatalf("problems = %v, want an unknown-action report", problems)
		}
		assertComplete(t, clean)
	})

	t.Run("reserved key reverted to default", func(t *testing.T) {
		t.Parallel()
		clean, problems := ValidateKeymap(Keymap{ActionChatOpen: {"esc"}})
		if !reflect.DeepEqual(clean[ActionChatOpen], []string{"enter"}) {
			t.Fatalf("chat.open = %v, want default [enter]", clean[ActionChatOpen])
		}
		if !containsSubstr(problems, "reserved") {
			t.Fatalf("problems = %v, want a reserved-key report", problems)
		}
		assertComplete(t, clean)
	})

	t.Run("conflict within context reverted", func(t *testing.T) {
		t.Parallel()
		clean, problems := ValidateKeymap(Keymap{
			ActionChatDown: {"j"},
			ActionChatUp:   {"j"},
		})
		if !reflect.DeepEqual(clean[ActionChatDown], []string{"j"}) {
			t.Fatalf("chat.down = %v, want [j]", clean[ActionChatDown])
		}
		if !reflect.DeepEqual(clean[ActionChatUp], []string{"k", "up"}) {
			t.Fatalf("chat.up = %v, want default [k up] after conflict", clean[ActionChatUp])
		}
		if !containsSubstr(problems, "already bound") {
			t.Fatalf("problems = %v, want a conflict report", problems)
		}
		assertComplete(t, clean)
	})

	t.Run("conflict with global reverted", func(t *testing.T) {
		t.Parallel()
		clean, problems := ValidateKeymap(Keymap{ActionChatRefresh: {"?"}})
		if !reflect.DeepEqual(clean[ActionChatRefresh], []string{"r"}) {
			t.Fatalf("chat.refresh = %v, want default [r] after global conflict", clean[ActionChatRefresh])
		}
		if !containsSubstr(problems, "already bound") {
			t.Fatalf("problems = %v, want a conflict report", problems)
		}
		assertComplete(t, clean)
	})

	t.Run("valid custom passes", func(t *testing.T) {
		t.Parallel()
		clean, problems := ValidateKeymap(Keymap{ActionChatRefresh: {"R"}})
		if len(problems) != 0 {
			t.Fatalf("problems = %v, want none", problems)
		}
		if !reflect.DeepEqual(clean[ActionChatRefresh], []string{"R"}) {
			t.Fatalf("chat.refresh = %v, want [R]", clean[ActionChatRefresh])
		}
		assertComplete(t, clean)
	})

	// This scenario does not use assertComplete: it deliberately produces an
	// action with zero keys, which is the correct outcome here (see below),
	// not a case the "every action has keys" invariant applies to.
	t.Run("valid rebind onto another action's untouched default is caught, not silently ghosted", func(t *testing.T) {
		t.Parallel()
		// chat.down is validly rebound onto "r"; chat.refresh is absent from
		// raw entirely, so it falls back to its own default, which is also
		// "r". Both requests are individually valid — the collision only
		// exists between them — so this must be caught by the SAME
		// conflict check the explicit-entry path uses, not skipped because
		// one side arrived via the default-fallback path.
		clean, problems := ValidateKeymap(Keymap{ActionChatDown: {"r"}})
		if !reflect.DeepEqual(clean[ActionChatDown], []string{"r"}) {
			t.Fatalf("chat.down = %v, want [r] (the valid rebind should win)", clean[ActionChatDown])
		}
		if len(clean[ActionChatRefresh]) != 0 {
			t.Fatalf("chat.refresh = %v, want unbound — its default collided and must not silently share \"r\"", clean[ActionChatRefresh])
		}
		if !containsSubstr(problems, "already bound") {
			t.Fatalf("problems = %v, want a conflict report naming the contested default", problems)
		}

		// The keymap must stay internally consistent: whichever action
		// clean{} says holds "r" must be the one dispatch actually resolves
		// to. Before this fix, both actions ended up listing "r" in clean{}
		// with no problem reported, while the index (built by iterating in
		// the same fixed action order) silently gave the key to whichever
		// action happened to be declared later — a ghost binding on the
		// loser that never fired and no diagnostic explaining why.
		idx := buildKeymapIndex(clean)
		action, ok := idx.actionFor(contextChatList, "r")
		if !ok || action != ActionChatDown {
			t.Fatalf("dispatch for \"r\" = %v (ok=%v), want chat.down, matching what clean{} reports", action, ok)
		}
	})
}

func TestLoadKeymapBadJSONKeepsFileAndReturnsDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	garbage := []byte("{ this is not valid json ]")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	km, problems := LoadKeymap(dir)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly one", problems)
	}
	if !reflect.DeepEqual(km, DefaultKeymap()) {
		t.Fatal("bad JSON should yield the default keymap")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !reflect.DeepEqual(after, garbage) {
		t.Fatalf("bad keybindings file was rewritten:\n%s", after)
	}
}

func TestDispatchHonorsCustomBinding(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	km := DefaultKeymap()
	km[ActionChatOpen] = []string{"o"}
	m = withKeymap(m, km)

	updated, _ := m.Update(chatsLoadedMsg{chats: []domain.ChatSummary{{
		JID:   "111@s.whatsapp.net",
		Title: "Alice",
	}}})
	model := updated.(Model)

	// enter is no longer bound to chat.open: nothing happens.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.mode != viewChats {
		t.Fatalf("enter opened a thread despite rebind: mode = %v", model.mode)
	}

	// the custom "o" key opens the thread.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	model = updated.(Model)
	if model.mode != viewThread {
		t.Fatalf("custom binding did not open thread: mode = %v", model.mode)
	}
	if model.currentChatID != "111@s.whatsapp.net" {
		t.Fatalf("currentChatID = %q", model.currentChatID)
	}
}

func TestKeymapEditorCaptureBindsAndPersists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	m = m.WithDataDir(dir)
	m = withKeymap(m, DefaultKeymap())

	// Open the editor with ctrl+k from the chat list.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model := updated.(Model)
	if !model.keymapEditorOpen {
		t.Fatal("ctrl+k did not open the keybinding editor")
	}

	// Navigation moves the selection.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if model.keymapEditorIndex != 1 {
		t.Fatalf("editor index after j = %d, want 1", model.keymapEditorIndex)
	}

	// Select chat.refresh and capture "g".
	model.keymapEditorIndex = actionSpecIndex(t, ActionChatRefresh)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.keymapCapture {
		t.Fatal("enter did not enter capture mode")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	model = updated.(Model)
	if model.keymapCapture {
		t.Fatal("capture mode did not end after a key press")
	}
	if !reflect.DeepEqual(model.keymap[ActionChatRefresh], []string{"g"}) {
		t.Fatalf("chat.refresh = %v, want [g]", model.keymap[ActionChatRefresh])
	}
	if action, ok := model.keyIndex.actionFor(contextChatList, "g"); !ok || action != ActionChatRefresh {
		t.Fatalf("index not rebuilt: actionFor(chat, g) = %q, %v", action, ok)
	}

	// The change is persisted to disk.
	onDisk := readKeymapFile(t, dir)
	if !reflect.DeepEqual(onDisk[ActionChatRefresh], []string{"g"}) {
		t.Fatalf("persisted chat.refresh = %v, want [g]", onDisk[ActionChatRefresh])
	}

	// Capturing a key already bound in the same context is rejected.
	model.keymapEditorIndex = actionSpecIndex(t, ActionChatOpen)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	model = updated.(Model)
	if !strings.Contains(model.lastErr, "already bound") {
		t.Fatalf("lastErr = %q, want an already-bound conflict", model.lastErr)
	}
	if !reflect.DeepEqual(model.keymap[ActionChatOpen], []string{"enter"}) {
		t.Fatalf("chat.open changed despite conflict: %v", model.keymap[ActionChatOpen])
	}
}

func TestKeymapEditorResetRestoresDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	m = m.WithDataDir(dir)
	km := DefaultKeymap()
	km[ActionChatRefresh] = []string{"g"}
	m = withKeymap(m, km)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model := updated.(Model)
	model.keymapEditorIndex = actionSpecIndex(t, ActionChatRefresh)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	model = updated.(Model)
	if !reflect.DeepEqual(model.keymap[ActionChatRefresh], []string{"r"}) {
		t.Fatalf("chat.refresh = %v, want default [r] after reset", model.keymap[ActionChatRefresh])
	}
	onDisk := readKeymapFile(t, dir)
	if !reflect.DeepEqual(onDisk[ActionChatRefresh], []string{"r"}) {
		t.Fatalf("persisted chat.refresh = %v, want default [r]", onDisk[ActionChatRefresh])
	}
}

func TestKeymapEditorResetRejectsCollisionWithRebindElsewhere(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	m = m.WithDataDir(dir)

	// chat.refresh has moved off its default "r", freeing it up for
	// chat.down to claim via an explicit rebind.
	km := DefaultKeymap()
	km[ActionChatRefresh] = []string{"g"}
	km[ActionChatDown] = []string{"r"}
	m = withKeymap(m, km)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model := updated.(Model)
	model.keymapEditorIndex = actionSpecIndex(t, ActionChatRefresh)

	// Resetting chat.refresh to its default ("r") must not silently clobber
	// chat.down's live binding of the same key.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	model = updated.(Model)

	if len(model.keymap[ActionChatRefresh]) != 0 {
		t.Fatalf("chat.refresh = %v, want unbound — its default collided and must not silently share \"r\"", model.keymap[ActionChatRefresh])
	}
	if !reflect.DeepEqual(model.keymap[ActionChatDown], []string{"r"}) {
		t.Fatalf("chat.down = %v, want unchanged [r]", model.keymap[ActionChatDown])
	}
	if !strings.Contains(model.lastErr, "already bound") {
		t.Fatalf("lastErr = %q, want an already-bound conflict", model.lastErr)
	}
	if action, ok := model.keyIndex.actionFor(contextChatList, "r"); !ok || action != ActionChatDown {
		t.Fatalf("dispatch for \"r\" = %v (ok=%v), want chat.down, matching what clean{} reports", action, ok)
	}

	onDisk := readKeymapFile(t, dir)
	if len(onDisk[ActionChatRefresh]) != 0 {
		t.Fatalf("persisted chat.refresh = %v, want unbound", onDisk[ActionChatRefresh])
	}
}

func TestKeymapLiveReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	m = m.WithDataDir(dir)
	m = withKeymap(m, DefaultKeymap())

	// External edit: rebind chat.open to "o".
	edited := DefaultKeymap()
	edited[ActionChatOpen] = []string{"o"}
	writeKeymapFile(t, dir, edited)
	m.keymapModTime = time.Time{}

	updated, _ := m.Update(keymapReloadTickMsg{})
	model := updated.(Model)
	if action, ok := model.keyIndex.actionFor(contextChatList, "o"); !ok || action != ActionChatOpen {
		t.Fatalf("live reload did not apply new binding: actionFor(chat, o) = %q, %v", action, ok)
	}
	if model.status != "Keybindings reloaded" {
		t.Fatalf("status = %q, want reloaded message", model.status)
	}

	// External edit turns the file into garbage: fall back to defaults + error.
	if err := os.WriteFile(filepath.Join(dir, "keybindings.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	model.keymapModTime = time.Time{}
	updated, _ = model.Update(keymapReloadTickMsg{})
	model = updated.(Model)
	if !reflect.DeepEqual(model.keymap, DefaultKeymap()) {
		t.Fatal("garbage reload should fall back to defaults")
	}
	if model.lastErr == "" {
		t.Fatal("garbage reload should set lastErr")
	}
}

func TestKeymapEditorRejectsReservedCapture(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true
	m = m.WithDataDir(dir)
	m = withKeymap(m, DefaultKeymap())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model := updated.(Model)
	model.keymapEditorIndex = actionSpecIndex(t, ActionChatRefresh)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if model.keymapCapture {
		t.Fatal("capture should end after rejecting a reserved key")
	}
	if !strings.Contains(model.lastErr, "reserved") {
		t.Fatalf("lastErr = %q, want a reserved-key rejection", model.lastErr)
	}
	if !reflect.DeepEqual(model.keymap[ActionChatRefresh], []string{"r"}) {
		t.Fatalf("chat.refresh changed after reserved rejection: %v", model.keymap[ActionChatRefresh])
	}
}

func containsSubstr(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}

func readKeymapFile(t *testing.T, dir string) Keymap {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "keybindings.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var km Keymap
	if err := json.Unmarshal(data, &km); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return km
}

func writeKeymapFile(t *testing.T, dir string, km Keymap) {
	t.Helper()
	data, err := json.MarshalIndent(km, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keybindings.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
