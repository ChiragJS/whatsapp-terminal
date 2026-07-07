package whatsapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
	"go.mau.fi/whatsmeow/types"
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
