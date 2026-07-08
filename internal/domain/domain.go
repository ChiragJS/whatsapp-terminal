package domain

import (
	"context"
	"time"
)

type ReceiptState string

const (
	ReceiptStateUnknown   ReceiptState = "unknown"
	ReceiptStateSent      ReceiptState = "sent"
	ReceiptStateDelivered ReceiptState = "delivered"
	ReceiptStateRead      ReceiptState = "read"
	ReceiptStateReceived  ReceiptState = "received"
)

type MediaKind string

const (
	MediaKindNone     MediaKind = ""
	MediaKindImage    MediaKind = "image"
	MediaKindVideo    MediaKind = "video"
	MediaKindAudio    MediaKind = "audio"
	MediaKindVoice    MediaKind = "voice"
	MediaKindDocument MediaKind = "document"
	MediaKindSticker  MediaKind = "sticker"
)

type EventType string

const (
	EventStatus         EventType = "status"
	EventQRCode         EventType = "qr_code"
	EventChatListUpdate EventType = "chat_list_update"
	EventChatUpdate     EventType = "chat_update"
	EventError          EventType = "error"
)

type Contact struct {
	JID          string
	DisplayName  string
	PushName     string
	BusinessName string
}

type ChatSummary struct {
	JID                string
	Title              string
	LastMessageID      string
	LastMessagePreview string
	LastSenderName     string
	LastMessageAt      time.Time
	UnreadCount        int
	IsGroup            bool
}

type Message struct {
	ID                 string
	ChatJID            string
	SenderJID          string
	SenderName         string
	Text               string
	Timestamp          time.Time
	FromMe             bool
	Receipt            ReceiptState
	IsGroup            bool
	MediaKind          MediaKind
	MediaMIME          string
	MediaFileName      string
	MediaDirectPath    string
	MediaFileLength    uint64
	MediaSeconds       uint32
	MediaKey           []byte
	MediaFileSHA256    []byte
	MediaFileEncSHA256 []byte
	DownloadedPath     string
	Reactions          []Reaction
}

// Reaction is one sender's current emoji reaction on a message. Reactions
// are last-write-wins state keyed by (chat, target message, sender); an
// empty emoji means the sender removed their reaction.
type Reaction struct {
	ChatJID    string
	TargetID   string
	SenderJID  string
	SenderName string
	Emoji      string
	// SenderTS is the sender's millisecond timestamp, used to reject
	// out-of-order updates (e.g. a stale reaction arriving after its removal).
	SenderTS int64
}

type Event struct {
	Type    EventType
	ChatJID string
	Status  string
	QRCode  string
	Err     error
	Notify  bool
}

// Transport is the seam between the TUI and a chat network adapter.
//
// A successful send method must record the local echo in the Local Cache before
// returning and emit an EventChatUpdate for the affected chat. RequestHistory
// only requests remote history; the resulting Message Records arrive later via
// adapter-owned Local Cache mutation and transport events. DownloadMedia writes
// the requested file and marks the Message Record downloaded in the Local Cache.
type Transport interface {
	Start(context.Context) error
	Stop() error
	Events() <-chan Event

	SendText(context.Context, string, string) error
	SendImage(context.Context, string, string, string) error
	SendMedia(context.Context, string, string, string) error
	SendVoiceNote(context.Context, string, string, time.Duration) error
	// SendReaction reacts to the message identified by (chatJID,
	// targetSenderJID, targetMessageID); an empty emoji removes the reaction.
	SendReaction(ctx context.Context, chatJID, targetSenderJID, targetMessageID, emoji string) error

	DownloadMedia(context.Context, Message, string) (string, error)
	RequestHistory(context.Context, string, int) error
}
