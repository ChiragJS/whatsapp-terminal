package demo

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	"github.com/chirag/whatsapp-terminal/internal/media"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

type Transport struct {
	repo   *appstore.Store
	logger *slog.Logger
	events chan domain.Event
	stress bool

	mu sync.Mutex
}

func New(repo *appstore.Store, logger *slog.Logger) *Transport {
	return &Transport{
		repo:   repo,
		logger: logger,
		events: make(chan domain.Event, 64),
	}
}

// WithStress toggles the oversized stress-test seed. The flag only matters on
// the first launch against an empty cache; subsequent launches reuse what was
// already seeded.
func (t *Transport) WithStress(enabled bool) *Transport {
	t.stress = enabled
	return t
}

func (t *Transport) Start(ctx context.Context) error {
	if err := t.seed(ctx); err != nil {
		return err
	}
	t.emit(domain.Event{Type: domain.EventStatus, Status: "connected (demo mode)"})
	t.emit(domain.Event{Type: domain.EventChatListUpdate})
	return nil
}

func (t *Transport) Stop() error {
	return nil
}

func (t *Transport) Events() <-chan domain.Event {
	return t.events
}

func (t *Transport) SendText(ctx context.Context, chatJID, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now().UTC()
	msg := domain.Message{
		ID:         fmt.Sprintf("demo-%d", now.UnixNano()),
		ChatJID:    chatJID,
		SenderJID:  "self@s.whatsapp.net",
		SenderName: "You",
		Text:       text,
		Timestamp:  now,
		FromMe:     true,
		Receipt:    domain.ReceiptStateSent,
		IsGroup:    chatJID == "project-alpha@g.us",
	}
	if err := t.repo.RecordMessageWithChatTitle(ctx, msg, "Unknown", false); err != nil {
		return err
	}

	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: "Message sent in demo mode"})
	return nil
}

func (t *Transport) SendImage(ctx context.Context, chatJID, path, caption string) error {
	return t.recordOutgoingMedia(ctx, chatJID, path, caption, domain.MediaKindImage, domain.MediaKindImage, 0)
}

func (t *Transport) SendMedia(ctx context.Context, chatJID, path, caption string) error {
	kind := media.KindForMIME(detectMIME(path))
	return t.recordOutgoingMedia(ctx, chatJID, path, caption, kind, kind, 0)
}

func (t *Transport) SendVoiceNote(ctx context.Context, chatJID, path string, duration time.Duration) error {
	return t.recordOutgoingMedia(ctx, chatJID, path, "", domain.MediaKindVoice, domain.MediaKindAudio, duration)
}

func (t *Transport) DownloadMedia(ctx context.Context, msg domain.Message, downloadDir string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if msg.MediaKind == domain.MediaKindNone {
		return "", fmt.Errorf("message does not contain downloadable media")
	}
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		return "", err
	}
	name := msg.MediaFileName
	if name == "" {
		name = fmt.Sprintf("%s%s", msg.ID, media.Extension(msg.MediaMIME, msg.MediaKind))
	}
	targetPath := filepath.Join(downloadDir, name)
	content := []byte("demo media placeholder")
	if err := os.WriteFile(targetPath, content, 0o600); err != nil {
		return "", err
	}
	if err := t.repo.MarkMessageDownloaded(ctx, msg.ChatJID, msg.ID, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

func (t *Transport) recordOutgoingMedia(ctx context.Context, chatJID, path, caption string, previewKind, storedKind domain.MediaKind, duration time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now().UTC()
	preview := media.Preview(previewKind, filepath.Base(path), caption)
	msg := domain.Message{
		ID:            fmt.Sprintf("demo-media-%d", now.UnixNano()),
		ChatJID:       chatJID,
		SenderJID:     "self@s.whatsapp.net",
		SenderName:    "You",
		Text:          preview,
		Timestamp:     now,
		FromMe:        true,
		Receipt:       domain.ReceiptStateSent,
		IsGroup:       chatJID == "project-alpha@g.us",
		MediaKind:     storedKind,
		MediaMIME:     detectMIME(path),
		MediaFileName: filepath.Base(path),
		MediaSeconds:  durationSeconds(duration),
	}
	if err := t.repo.RecordMessageWithChatTitle(ctx, msg, "Unknown", false); err != nil {
		return err
	}
	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: fmt.Sprintf("%s sent in demo mode", media.StatusLabel(previewKind))})
	return nil
}

func (t *Transport) RequestHistory(ctx context.Context, chatJID string, count int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	oldest, err := t.repo.OldestMessage(ctx, chatJID)
	if err != nil {
		return err
	}
	base := time.Now().UTC()
	if oldest != nil {
		base = oldest.Timestamp
	}
	for i := 0; i < min(3, count); i++ {
		ts := base.Add(-time.Duration((i + 1) * int(time.Hour)))
		msg := domain.Message{
			ID:         fmt.Sprintf("history-%d-%d", ts.Unix(), i),
			ChatJID:    chatJID,
			SenderJID:  chatJID,
			SenderName: "Archive",
			Text:       fmt.Sprintf("Older demo message %d", i+1),
			Timestamp:  ts,
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    chatJID == "project-alpha@g.us",
		}
		if err := t.repo.RecordMessage(ctx, msg, false); err != nil {
			return err
		}
	}

	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: "Loaded older demo history"})
	return nil
}

func detectMIME(path string) string {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType != "" {
		return mimeType
	}
	// #nosec G304 -- path is a user-selected local attachment path in demo mode.
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	sample := make([]byte, 512)
	n, _ := file.Read(sample)
	return http.DetectContentType(sample[:n])
}

func (t *Transport) seed(ctx context.Context) error {
	chats, err := t.repo.ListChats(ctx, "", 1)
	if err != nil {
		return err
	}
	if len(chats) > 0 {
		return nil
	}

	contacts := []domain.Contact{
		{JID: "alice@s.whatsapp.net", DisplayName: "Alice Mercer", PushName: "Alice"},
		{JID: "bob@s.whatsapp.net", DisplayName: "Bob Chen", PushName: "Bob"},
		{JID: "project-alpha@g.us", DisplayName: "Project Alpha"},
	}
	for _, contact := range contacts {
		if err := t.repo.UpsertContact(ctx, contact); err != nil {
			return err
		}
	}

	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	messages := []domain.Message{
		{
			ID:         "demo-1",
			ChatJID:    "alice@s.whatsapp.net",
			SenderJID:  "alice@s.whatsapp.net",
			SenderName: "Alice",
			Text:       "Coffee later? I found a place with strong Wi-Fi and no crowd.",
			Timestamp:  base.Add(-25 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
		},
		{
			ID:         "demo-2",
			ChatJID:    "alice@s.whatsapp.net",
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "Yes. Let’s do 7:30.",
			Timestamp:  base.Add(-22 * time.Minute),
			FromMe:     true,
			Receipt:    domain.ReceiptStateRead,
		},
		{
			ID:         "demo-3",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "bob@s.whatsapp.net",
			SenderName: "Bob",
			Text:       "Need project numbers by Friday. I pushed the draft sheet.",
			Timestamp:  base.Add(-10 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    true,
		},
		{
			ID:         "demo-4",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "I’ll review the summary tonight and send comments.",
			Timestamp:  base.Add(-8 * time.Minute),
			FromMe:     true,
			Receipt:    domain.ReceiptStateDelivered,
			IsGroup:    true,
		},
	}
	if err := t.repo.RecordHistoryBatch(ctx, domain.ChatSummary{
		JID:         "alice@s.whatsapp.net",
		Title:       "Alice Mercer",
		UnreadCount: 0,
	}, messages[:2]); err != nil {
		return err
	}
	if err := t.repo.RecordHistoryBatch(ctx, domain.ChatSummary{
		JID:         "project-alpha@g.us",
		Title:       "Project Alpha",
		UnreadCount: 1,
		IsGroup:     true,
	}, messages[2:]); err != nil {
		return err
	}
	if t.stress {
		if err := t.seedStress(ctx); err != nil {
			return err
		}
	}
	return nil
}

// seedStress populates a wide-and-deep demo dataset so the TUI can be
// exercised against scrolling, truncation, pagination, unread badges, and
// mixed direct/group conversations. It is layered on top of the canonical
// demo seed and only runs when WithStress(true) is set on a fresh cache.
func (t *Transport) seedStress(ctx context.Context) error {
	const (
		directChatCount  = 220
		groupChatCount   = 80
		minMessages      = 20
		maxMessages      = 260
		unreadEveryNth   = 4
		longMessageEvery = 9
	)

	firstNames := []string{
		"Aanya", "Ishaan", "Meera", "Rohan", "Diya", "Aarav", "Kavya", "Vihaan",
		"Sara", "Arjun", "Anika", "Reyansh", "Pari", "Kabir", "Mira", "Yash",
		"Priya", "Vivaan", "Saanvi", "Aditya", "Anaya", "Krishna", "Riya", "Aarush",
		"Tara", "Dev", "Avni", "Shaurya", "Myra", "Atharv",
	}
	lastNames := []string{
		"Sharma", "Iyer", "Mehta", "Patel", "Reddy", "Nair", "Banerjee", "Kapoor",
		"Singh", "Joshi", "Das", "Khan", "Bose", "Rao", "Gupta", "Shah",
	}
	groupTitles := []string{
		"Engineering · Standup",
		"Design Critique",
		"Founders Circle",
		"Pune Foodies",
		"Hostel '14 Reunion",
		"Goa Trip 🏝",
		"Investor Updates",
		"Hiring · Backend",
		"Late-night Hackers",
		"Marathon Training",
		"Book Club — Q2",
		"Parents · School",
		"Apartment 4B",
		"Cricket Sundays",
		"Music Recs",
		"Newsletter Drafts",
		"Wedding Squad",
		"Roommates",
		"Lunch Roulette",
		"Tabletop · Wednesdays",
	}
	wordPool := []string{
		"morning", "deck", "shipping", "blocked", "incident", "diff", "review",
		"merge", "queue", "latency", "rollout", "regression", "fixture", "scope",
		"deadline", "kickoff", "syllabus", "draft", "polish", "ping", "noticed",
		"forwarded", "sketch", "pencil", "ledger", "metric", "outage", "buffer",
		"context", "cache", "spike", "ratelimit", "rollback", "tracing", "sanity",
		"finals", "kickoff", "headline", "weather", "trains", "monsoon", "season",
		"banger", "ping-me", "lol", "ofc", "TIL", "BRB", "EOD", "FYI",
	}

	rng := newDeterministicRand(20260420)
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)

	type chatSpec struct {
		jid     string
		title   string
		isGroup bool
	}
	chatSpecs := make([]chatSpec, 0, directChatCount+groupChatCount)
	for i := 0; i < directChatCount; i++ {
		first := firstNames[rng.intn(len(firstNames))]
		last := lastNames[rng.intn(len(lastNames))]
		jid := fmt.Sprintf("stress-d-%03d@s.whatsapp.net", i)
		chatSpecs = append(chatSpecs, chatSpec{jid: jid, title: first + " " + last})
	}
	for i := 0; i < groupChatCount; i++ {
		title := groupTitles[i%len(groupTitles)]
		if pass := i / len(groupTitles); pass > 0 {
			title = fmt.Sprintf("%s · %d", title, pass+1)
		}
		jid := fmt.Sprintf("stress-g-%03d@g.us", i)
		chatSpecs = append(chatSpecs, chatSpec{jid: jid, title: title, isGroup: true})
	}

	for _, spec := range chatSpecs {
		contact := domain.Contact{JID: spec.jid, DisplayName: spec.title, PushName: spec.title}
		if err := t.repo.UpsertContact(ctx, contact); err != nil {
			return err
		}

		messageCount := minMessages + rng.intn(maxMessages-minMessages+1)
		messages := make([]domain.Message, 0, messageCount)
		// Spread messages from oldest (back many days) to newest (recent minutes).
		oldestOffset := time.Duration(messageCount*7) * time.Minute
		for i := 0; i < messageCount; i++ {
			ts := base.Add(-oldestOffset + time.Duration(i*7+rng.intn(5))*time.Minute)
			fromMe := rng.intn(3) == 0
			senderName := spec.title
			senderJID := spec.jid
			if spec.isGroup {
				memberFirst := firstNames[rng.intn(len(firstNames))]
				senderName = memberFirst
				senderJID = fmt.Sprintf("%s-mem%02d@s.whatsapp.net", spec.jid, rng.intn(12))
			}
			if fromMe {
				senderName = "You"
				senderJID = "self@s.whatsapp.net"
			}
			text := composeStressMessage(rng, wordPool, i, longMessageEvery)
			receipt := domain.ReceiptStateReceived
			if fromMe {
				switch rng.intn(3) {
				case 0:
					receipt = domain.ReceiptStateSent
				case 1:
					receipt = domain.ReceiptStateDelivered
				default:
					receipt = domain.ReceiptStateRead
				}
			}
			messages = append(messages, domain.Message{
				ID:         fmt.Sprintf("%s-%04d", spec.jid, i),
				ChatJID:    spec.jid,
				SenderJID:  senderJID,
				SenderName: senderName,
				Text:       text,
				Timestamp:  ts,
				FromMe:     fromMe,
				Receipt:    receipt,
				IsGroup:    spec.isGroup,
			})
		}
		unread := 0
		if rng.intn(unreadEveryNth) == 0 {
			unread = 1 + rng.intn(120)
		}
		summary := domain.ChatSummary{
			JID:         spec.jid,
			Title:       spec.title,
			UnreadCount: unread,
			IsGroup:     spec.isGroup,
		}
		if err := t.repo.RecordHistoryBatch(ctx, summary, messages); err != nil {
			return err
		}
	}
	return nil
}

func composeStressMessage(rng *deterministicRand, words []string, index, longEvery int) string {
	if longEvery > 0 && index%longEvery == 0 {
		// Multi-line "long" message — exercises wrapping in the thread view.
		paragraphs := 2 + rng.intn(2)
		var lines []string
		for p := 0; p < paragraphs; p++ {
			lines = append(lines, randomSentence(rng, words, 8+rng.intn(10)))
		}
		return strings.Join(lines, "\n")
	}
	return randomSentence(rng, words, 3+rng.intn(8))
}

func randomSentence(rng *deterministicRand, words []string, length int) string {
	parts := make([]string, length)
	for i := range parts {
		parts[i] = words[rng.intn(len(words))]
	}
	sentence := strings.Join(parts, " ")
	return strings.ToUpper(sentence[:1]) + sentence[1:] + "."
}

// deterministicRand is a tiny xorshift PRNG so the stress seed is reproducible
// run-to-run without depending on math/rand global state.
type deterministicRand struct{ state uint64 }

func newDeterministicRand(seed uint64) *deterministicRand {
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15
	}
	return &deterministicRand{state: seed}
}

func (r *deterministicRand) next() uint64 {
	x := r.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	r.state = x
	return x
}

func (r *deterministicRand) intn(n int) int {
	if n <= 0 {
		return 0
	}
	// #nosec G115 -- the modulo result is < n, which is a positive int.
	return int(r.next() % uint64(n))
}

func (t *Transport) emit(event domain.Event) {
	select {
	case t.events <- event:
	default:
		if t.logger != nil {
			t.logger.Warn("dropping demo event", "type", event.Type)
		}
	}
}

func durationSeconds(duration time.Duration) uint32 {
	seconds := duration.Round(time.Second) / time.Second
	if seconds <= 0 {
		return 0
	}
	if seconds > time.Duration(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(seconds)
}
