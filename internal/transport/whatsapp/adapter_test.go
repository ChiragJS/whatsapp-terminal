package whatsapp

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestResolveChatTitleDoesNotReturnUnknownGroupJID(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	jid := types.NewJID("120363405662701156", types.GroupServer)
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "m1",
		ChatJID:    jid.String(),
		SenderJID:  "alice@s.whatsapp.net",
		SenderName: "Alice",
		Text:       "hello group",
		Timestamp:  time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
		Receipt:    domain.ReceiptStateReceived,
		IsGroup:    true,
	}, true); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	adapter := &Adapter{repo: repo}
	if got := adapter.resolveChatTitle(ctx, jid, ""); got != "" {
		t.Fatalf("resolveChatTitle() = %q, want empty for unknown group", got)
	}
}

func TestRecordGroupTitleUpdatesCacheTitle(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	jid := types.NewJID("120363405662701156", types.GroupServer)
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "m1",
		ChatJID:    jid.String(),
		SenderJID:  "alice@s.whatsapp.net",
		SenderName: "Alice",
		Text:       "hello group",
		Timestamp:  time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
		Receipt:    domain.ReceiptStateReceived,
		IsGroup:    true,
	}, true); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}
	adapter := &Adapter{repo: repo}
	if err := adapter.recordGroupTitle(ctx, jid, "Project Alpha"); err != nil {
		t.Fatalf("recordGroupTitle() error = %v", err)
	}

	chat, err := repo.GetChat(ctx, jid.String())
	if err != nil {
		t.Fatalf("GetChat() error = %v", err)
	}
	if chat == nil || chat.Title != "Project Alpha" || !chat.IsGroup {
		t.Fatalf("chat = %#v, want titled group", chat)
	}
}

func newFakeSessionDB(t *testing.T, lidToPN map[string]string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("open fake session db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT NOT NULL)`); err != nil {
		t.Fatalf("create lid map: %v", err)
	}
	for lid, pn := range lidToPN {
		if _, err := db.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`, lid, pn); err != nil {
			t.Fatalf("insert lid map row: %v", err)
		}
	}
	return db
}

func TestMirrorLIDAliasesCopiesSavedNamesBothWays(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	// Saved name lives on the phone-number JID; only a push name on the LID.
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "911111@s.whatsapp.net", DisplayName: "Mom"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "222lid@lid", PushName: "Sunita"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	// Reverse case: name known only under the LID alias.
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "444lid@lid", DisplayName: "Dad"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}

	adapter := &Adapter{repo: repo, sessionDB: newFakeSessionDB(t, map[string]string{
		"222lid": "911111",
		"444lid": "933333",
	})}
	if err := adapter.mirrorLIDAliases(ctx); err != nil {
		t.Fatalf("mirrorLIDAliases() error = %v", err)
	}

	if name, _ := repo.ContactName(ctx, "222lid@lid"); name != "Mom" {
		t.Fatalf("ContactName(lid) = %q, want Mom", name)
	}
	if name, _ := repo.ContactName(ctx, "933333@s.whatsapp.net"); name != "Dad" {
		t.Fatalf("ContactName(pn) = %q, want Dad", name)
	}
}

func TestCachedLIDSenderResolvesToSavedNameAfterMirror(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	// An old cached group message whose sender arrived under the LID alias
	// with only a push name snapshot.
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "m1",
		ChatJID:    "1203634@g.us",
		SenderJID:  "222lid@lid",
		SenderName: "Sunita",
		Text:       "khana kha liya?",
		Timestamp:  time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Receipt:    domain.ReceiptStateReceived,
		IsGroup:    true,
	}, true); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "911111@s.whatsapp.net", DisplayName: "Mom"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}

	adapter := &Adapter{repo: repo, sessionDB: newFakeSessionDB(t, map[string]string{"222lid": "911111"})}
	if err := adapter.mirrorLIDAliases(ctx); err != nil {
		t.Fatalf("mirrorLIDAliases() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, "1203634@g.us", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].SenderName != "Mom" {
		t.Fatalf("messages = %#v, want sender resolved to Mom", messages)
	}
}

func TestHandleReceiptResolvesLIDChatAlias(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	// Sent message stored under the canonical phone-number chat.
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:        "m1",
		ChatJID:   "911111@s.whatsapp.net",
		SenderJID: "self@s.whatsapp.net",
		Text:      "hi",
		Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		FromMe:    true,
		Receipt:   domain.ReceiptStateSent,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	adapter := &Adapter{
		repo:      repo,
		events:    make(chan domain.Event, 8),
		sessionDB: newFakeSessionDB(t, map[string]string{"222lid": "911111"}),
	}
	// The read receipt arrives under the chat's LID alias.
	evt := &waevents.Receipt{
		MessageSource: types.MessageSource{Chat: types.NewJID("222lid", types.HiddenUserServer)},
		MessageIDs:    []types.MessageID{"m1"},
		Type:          types.ReceiptTypeRead,
	}
	if err := adapter.handleReceipt(ctx, evt); err != nil {
		t.Fatalf("handleReceipt() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, "911111@s.whatsapp.net", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Receipt != domain.ReceiptStateRead {
		t.Fatalf("messages = %#v, want receipt marked read via LID alias", messages)
	}
}

func TestHandleMessageRecordsReactionWithoutMessageRow(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	if err := repo.RecordMessage(ctx, domain.Message{
		ID: "target-1", ChatJID: "1203634@g.us", SenderJID: "bob@s.whatsapp.net", Text: "sticker",
		Timestamp: time.Date(2026, 7, 8, 14, 42, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived, IsGroup: true,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	adapter := &Adapter{repo: repo, events: make(chan domain.Event, 8)}
	evt := &waevents.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    types.NewJID("1203634", types.GroupServer),
				Sender:  types.NewJID("911234", types.DefaultUserServer),
				IsGroup: true,
			},
			ID:        "reaction-1",
			Timestamp: time.Date(2026, 7, 8, 14, 43, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{
			ReactionMessage: &waE2E.ReactionMessage{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String("1203634@g.us"),
					ID:        proto.String("target-1"),
				},
				Text:              proto.String("😂"),
				SenderTimestampMS: proto.Int64(1700000000000),
			},
		},
	}
	if err := adapter.handleMessage(ctx, evt); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, "1203634@g.us", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d rows, want 1 (reaction must not create a message row)", len(messages))
	}
	if len(messages[0].Reactions) != 1 || messages[0].Reactions[0].Emoji != "😂" {
		t.Fatalf("reactions = %#v, want 😂 attached to target-1", messages[0].Reactions)
	}

	// Removal: same sender, empty text, later timestamp.
	evt.Message.ReactionMessage.Text = proto.String("")
	evt.Message.ReactionMessage.SenderTimestampMS = proto.Int64(1700000001000)
	if err := adapter.handleMessage(ctx, evt); err != nil {
		t.Fatalf("handleMessage(removal) error = %v", err)
	}
	messages, err = repo.ListMessages(ctx, "1203634@g.us", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages[0].Reactions) != 0 {
		t.Fatalf("reactions = %#v, want removal to clear the reaction", messages[0].Reactions)
	}
}
