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
}

type Event struct {
	Type    EventType
	ChatJID string
	Status  string
	QRCode  string
	Err     error
	Notify  bool
}

type Transport interface {
	Start(context.Context) error
	Stop() error
	Events() <-chan Event
	SendText(context.Context, string, string) error
	SendImage(context.Context, string, string, string) error
	SendMedia(context.Context, string, string, string) error
	SendVoiceNote(context.Context, string, string, time.Duration) error
	DownloadMedia(context.Context, Message, string) (string, error)
	RequestHistory(context.Context, string, int) error
}
