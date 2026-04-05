package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open app db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init(ctx context.Context) error {
	schema := `
CREATE TABLE IF NOT EXISTS contacts (
    jid TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    push_name TEXT NOT NULL DEFAULT '',
    business_name TEXT NOT NULL DEFAULT '',
    normalized_name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS chats (
    jid TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    normalized_title TEXT NOT NULL DEFAULT '',
    is_group INTEGER NOT NULL DEFAULT 0,
    last_message_id TEXT NOT NULL DEFAULT '',
    last_message_preview TEXT NOT NULL DEFAULT '',
    last_sender_name TEXT NOT NULL DEFAULT '',
    last_message_at TEXT NOT NULL DEFAULT '',
    unread_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    chat_jid TEXT NOT NULL,
    id TEXT NOT NULL,
    sender_jid TEXT NOT NULL,
    sender_name TEXT NOT NULL DEFAULT '',
    text_body TEXT NOT NULL DEFAULT '',
    ts TEXT NOT NULL,
    from_me INTEGER NOT NULL DEFAULT 0,
    receipt TEXT NOT NULL DEFAULT 'unknown',
    is_group INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_jid, id)
);

CREATE INDEX IF NOT EXISTS idx_contacts_normalized_name ON contacts(normalized_name);
CREATE INDEX IF NOT EXISTS idx_chats_normalized_title ON chats(normalized_title);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, ts DESC);
`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

func NormalizeSearch(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	return strings.Join(strings.Fields(input), " ")
}

func (s *Store) UpsertContact(ctx context.Context, contact domain.Contact) error {
	normalized := NormalizeSearch(strings.Join([]string{
		contact.DisplayName,
		contact.PushName,
		contact.BusinessName,
		contact.JID,
	}, " "))
	_, err := s.db.ExecContext(ctx, `
INSERT INTO contacts (jid, display_name, push_name, business_name, normalized_name)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    display_name = excluded.display_name,
    push_name = excluded.push_name,
    business_name = excluded.business_name,
    normalized_name = excluded.normalized_name
`, contact.JID, contact.DisplayName, contact.PushName, contact.BusinessName, normalized)
	if err != nil {
		return fmt.Errorf("upsert contact %s: %w", contact.JID, err)
	}
	return nil
}

func (s *Store) ContactName(ctx context.Context, jid string) (string, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT display_name, push_name, business_name FROM contacts WHERE jid = ?
`, jid)
	var displayName, pushName, businessName string
	if err := row.Scan(&displayName, &pushName, &businessName); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("lookup contact %s: %w", jid, err)
	}
	for _, candidate := range []string{displayName, pushName, businessName} {
		if strings.TrimSpace(candidate) != "" {
			return candidate, nil
		}
	}
	return "", nil
}

func (s *Store) UpsertChat(ctx context.Context, chat domain.ChatSummary) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chats (jid, title, normalized_title, is_group, last_message_id, last_message_preview, last_sender_name, last_message_at, unread_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE chats.title END,
    normalized_title = CASE WHEN excluded.normalized_title <> '' THEN excluded.normalized_title ELSE chats.normalized_title END,
    is_group = excluded.is_group,
    unread_count = excluded.unread_count,
    last_message_id = CASE
        WHEN excluded.last_message_at > chats.last_message_at THEN excluded.last_message_id
        ELSE chats.last_message_id
    END,
    last_message_preview = CASE
        WHEN excluded.last_message_at > chats.last_message_at THEN excluded.last_message_preview
        ELSE chats.last_message_preview
    END,
    last_sender_name = CASE
        WHEN excluded.last_message_at > chats.last_message_at THEN excluded.last_sender_name
        ELSE chats.last_sender_name
    END,
    last_message_at = CASE
        WHEN excluded.last_message_at > chats.last_message_at THEN excluded.last_message_at
        ELSE chats.last_message_at
    END
`, chat.JID, chat.Title, NormalizeSearch(strings.Join([]string{chat.Title, chat.JID}, " ")), boolToInt(chat.IsGroup), chat.LastMessageID, chat.LastMessagePreview, chat.LastSenderName, timeString(chat.LastMessageAt), chat.UnreadCount)
	if err != nil {
		return fmt.Errorf("upsert chat %s: %w", chat.JID, err)
	}
	return nil
}

func (s *Store) GetChat(ctx context.Context, jid string) (*domain.ChatSummary, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
    chats.jid,
    CASE
        WHEN chats.is_group = 0 AND contacts.display_name <> '' THEN contacts.display_name
        WHEN chats.is_group = 0 AND contacts.push_name <> '' THEN contacts.push_name
        WHEN chats.is_group = 0 AND contacts.business_name <> '' THEN contacts.business_name
        WHEN chats.title <> '' THEN chats.title
        WHEN contacts.display_name <> '' THEN contacts.display_name
        WHEN contacts.push_name <> '' THEN contacts.push_name
        WHEN contacts.business_name <> '' THEN contacts.business_name
        ELSE chats.jid
    END AS title,
    chats.last_message_id,
    chats.last_message_preview,
    chats.last_sender_name,
    chats.last_message_at,
    chats.unread_count,
    chats.is_group
FROM chats
LEFT JOIN contacts ON contacts.jid = chats.jid
WHERE chats.jid = ?
`, jid)
	var chat domain.ChatSummary
	var lastMessageAt string
	var isGroup int
	if err := row.Scan(&chat.JID, &chat.Title, &chat.LastMessageID, &chat.LastMessagePreview, &chat.LastSenderName, &lastMessageAt, &chat.UnreadCount, &isGroup); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get chat %s: %w", jid, err)
	}
	chat.IsGroup = isGroup == 1
	chat.LastMessageAt = parseTime(lastMessageAt)
	return &chat, nil
}

func (s *Store) RecordMessage(ctx context.Context, msg domain.Message, incrementUnread bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO messages (chat_jid, id, sender_jid, sender_name, text_body, ts, from_me, receipt, is_group)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, msg.ChatJID, msg.ID, msg.SenderJID, msg.SenderName, msg.Text, timeString(msg.Timestamp), boolToInt(msg.FromMe), string(msg.Receipt), boolToInt(msg.IsGroup))
	if err != nil {
		return fmt.Errorf("insert message %s: %w", msg.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read message rows affected: %w", err)
	}

	currentTitle := msg.ChatJID
	row := tx.QueryRowContext(ctx, `SELECT title FROM chats WHERE jid = ?`, msg.ChatJID)
	var existingTitle string
	switch scanErr := row.Scan(&existingTitle); scanErr {
	case nil:
		if existingTitle != "" {
			currentTitle = existingTitle
		}
	case sql.ErrNoRows:
	default:
		return fmt.Errorf("load existing chat title %s: %w", msg.ChatJID, scanErr)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO chats (jid, title, normalized_title, is_group, last_message_id, last_message_preview, last_sender_name, last_message_at, unread_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    title = CASE WHEN chats.title = '' THEN excluded.title ELSE chats.title END,
    normalized_title = CASE WHEN chats.normalized_title = '' THEN excluded.normalized_title ELSE chats.normalized_title END,
    is_group = excluded.is_group,
    last_message_id = CASE
        WHEN excluded.last_message_at >= chats.last_message_at THEN excluded.last_message_id
        ELSE chats.last_message_id
    END,
    last_message_preview = CASE
        WHEN excluded.last_message_at >= chats.last_message_at THEN excluded.last_message_preview
        ELSE chats.last_message_preview
    END,
    last_sender_name = CASE
        WHEN excluded.last_message_at >= chats.last_message_at THEN excluded.last_sender_name
        ELSE chats.last_sender_name
    END,
    last_message_at = CASE
        WHEN excluded.last_message_at >= chats.last_message_at THEN excluded.last_message_at
        ELSE chats.last_message_at
    END,
    unread_count = chats.unread_count + ?
`, msg.ChatJID, currentTitle, NormalizeSearch(strings.Join([]string{currentTitle, msg.ChatJID}, " ")), boolToInt(msg.IsGroup), msg.ID, previewText(msg.Text), msg.SenderName, timeString(msg.Timestamp), 0, unreadIncrement(rows, incrementUnread))
	if err != nil {
		return fmt.Errorf("upsert message chat %s: %w", msg.ChatJID, err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit message tx: %w", err)
	}
	return nil
}

func (s *Store) UpdateReceipts(ctx context.Context, chatJID string, messageIDs []string, receipt domain.ReceiptState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin receipt tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, messageID := range messageIDs {
		_, err = tx.ExecContext(ctx, `
UPDATE messages SET receipt = ? WHERE chat_jid = ? AND id = ?
`, string(receipt), chatJID, messageID)
		if err != nil {
			return fmt.Errorf("update receipt %s: %w", messageID, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit receipt tx: %w", err)
	}
	return nil
}

func (s *Store) ResetUnread(ctx context.Context, chatJID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE chats SET unread_count = 0 WHERE jid = ?
`, chatJID)
	if err != nil {
		return fmt.Errorf("reset unread %s: %w", chatJID, err)
	}
	return nil
}

func (s *Store) ListChats(ctx context.Context, query string, limit int) ([]domain.ChatSummary, error) {
	query = NormalizeSearch(query)
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `
SELECT
    chats.jid,
    CASE
        WHEN chats.is_group = 0 AND contacts.display_name <> '' THEN contacts.display_name
        WHEN chats.is_group = 0 AND contacts.push_name <> '' THEN contacts.push_name
        WHEN chats.is_group = 0 AND contacts.business_name <> '' THEN contacts.business_name
        WHEN chats.title <> '' THEN chats.title
        WHEN contacts.display_name <> '' THEN contacts.display_name
        WHEN contacts.push_name <> '' THEN contacts.push_name
        WHEN contacts.business_name <> '' THEN contacts.business_name
        ELSE chats.jid
    END AS title,
    chats.last_message_id,
    chats.last_message_preview,
    chats.last_sender_name,
    chats.last_message_at,
    chats.unread_count,
    chats.is_group
FROM chats
LEFT JOIN contacts ON contacts.jid = chats.jid
WHERE ? = '' OR chats.normalized_title LIKE ? OR chats.jid LIKE ? OR contacts.normalized_name LIKE ?
ORDER BY last_message_at DESC, title ASC
LIMIT ?
`, query, like, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()

	var chats []domain.ChatSummary
	seen := make(map[string]struct{}, limit)
	for rows.Next() {
		var chat domain.ChatSummary
		var lastMessageAt string
		var isGroup int
		if err := rows.Scan(&chat.JID, &chat.Title, &chat.LastMessageID, &chat.LastMessagePreview, &chat.LastSenderName, &lastMessageAt, &chat.UnreadCount, &isGroup); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chat.IsGroup = isGroup == 1
		chat.LastMessageAt = parseTime(lastMessageAt)
		chats = append(chats, chat)
		seen[chat.JID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if query == "" || len(chats) >= limit {
		return chats, nil
	}

	remaining := limit - len(chats)
	contactRows, err := s.db.QueryContext(ctx, `
SELECT jid, display_name, push_name, business_name
FROM contacts
WHERE normalized_name LIKE ?
ORDER BY
    CASE
        WHEN display_name <> '' THEN display_name
        WHEN push_name <> '' THEN push_name
        WHEN business_name <> '' THEN business_name
        ELSE jid
    END ASC
LIMIT ?
`, like, remaining)
	if err != nil {
		return nil, fmt.Errorf("list searchable contacts: %w", err)
	}
	defer contactRows.Close()

	for contactRows.Next() {
		var jid, displayName, pushName, businessName string
		if err := contactRows.Scan(&jid, &displayName, &pushName, &businessName); err != nil {
			return nil, fmt.Errorf("scan searchable contact: %w", err)
		}
		if _, ok := seen[jid]; ok {
			continue
		}
		title := firstNonEmpty(displayName, pushName, businessName, jid)
		chats = append(chats, domain.ChatSummary{
			JID:   jid,
			Title: title,
		})
		seen[jid] = struct{}{}
		if len(chats) >= limit {
			break
		}
	}
	if err := contactRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate searchable contacts: %w", err)
	}
	return chats, nil
}

func (s *Store) ListMessages(ctx context.Context, chatJID string, limit int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, chat_jid, sender_jid,
       CASE
           WHEN from_me = 1 AND sender_name <> '' THEN sender_name
           WHEN contacts.display_name <> '' THEN contacts.display_name
           WHEN contacts.push_name <> '' THEN contacts.push_name
           WHEN contacts.business_name <> '' THEN contacts.business_name
           WHEN sender_name <> '' THEN sender_name
           ELSE sender_jid
       END AS sender_name,
       text_body, ts, from_me, receipt, is_group
FROM (
    SELECT id, chat_jid, sender_jid, sender_name, text_body, ts, from_me, receipt, is_group
    FROM messages
    WHERE chat_jid = ?
    ORDER BY ts DESC
    LIMIT ?
 ) recent_messages
LEFT JOIN contacts ON contacts.jid = recent_messages.sender_jid
ORDER BY ts ASC
`, chatJID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages %s: %w", chatJID, err)
	}
	defer rows.Close()

	var messages []domain.Message
	for rows.Next() {
		var msg domain.Message
		var ts string
		var fromMe, isGroup int
		var receipt string
		if err := rows.Scan(&msg.ID, &msg.ChatJID, &msg.SenderJID, &msg.SenderName, &msg.Text, &ts, &fromMe, &receipt, &isGroup); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msg.Timestamp = parseTime(ts)
		msg.FromMe = fromMe == 1
		msg.Receipt = domain.ReceiptState(receipt)
		msg.IsGroup = isGroup == 1
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func (s *Store) OldestMessage(ctx context.Context, chatJID string) (*domain.Message, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, chat_jid, sender_jid, sender_name, text_body, ts, from_me, receipt, is_group
FROM messages
WHERE chat_jid = ?
ORDER BY ts ASC
LIMIT 1
`, chatJID)

	var msg domain.Message
	var ts, receipt string
	var fromMe, isGroup int
	if err := row.Scan(&msg.ID, &msg.ChatJID, &msg.SenderJID, &msg.SenderName, &msg.Text, &ts, &fromMe, &receipt, &isGroup); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("oldest message %s: %w", chatJID, err)
	}
	msg.Timestamp = parseTime(ts)
	msg.FromMe = fromMe == 1
	msg.Receipt = domain.ReceiptState(receipt)
	msg.IsGroup = isGroup == 1
	return &msg, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unreadIncrement(rowsAffected int64, incrementUnread bool) int {
	if incrementUnread && rowsAffected > 0 {
		return 1
	}
	return 0
}

func timeString(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func previewText(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	const maxLen = 80
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-1] + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
