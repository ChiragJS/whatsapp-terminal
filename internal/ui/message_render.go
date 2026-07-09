package ui

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

// WhatsApp inline markup: `mono`, *bold*, _italic_, ~strike~. Content must
// not start or end with whitespace, mirroring how WhatsApp itself decides
// whether delimiters format. Nested markup is not supported; the first
// matching span wins.
var inlineMarkup = regexp.MustCompile(
	"`([^`\n]+)`" +
		`|\*([^\s*](?:[^*\n]*[^\s*])?)\*` +
		`|_([^\s_](?:[^_\n]*[^\s_])?)_` +
		`|~([^\s~](?:[^~\n]*[^\s~])?)~`,
)

// mentionToken matches WhatsApp mentions as they appear in message text: an
// @ followed by the numeric user part of the mentioned JID.
var mentionToken = regexp.MustCompile(`^@(\d{6,20})(\W*)$`)

// mentionAnywhere finds mention tokens inside larger text.
var mentionAnywhere = regexp.MustCompile(`@(\d{6,20})`)

type spanKind int

const (
	spanPlain spanKind = iota
	spanMono
	spanBold
	spanItalic
	spanStrike
	spanMention
)

// styledWord is one whitespace-delimited word tagged with the markup span
// it belongs to. Wrapping happens on words; rendering styles runs of
// consecutive same-kind words as a single span, so styling never splits
// mid-sequence and plain text stays contiguous in the output.
type styledWord struct {
	text string
	kind spanKind
}

func spanStyle(kind spanKind) lipgloss.Style {
	switch kind {
	case spanMono:
		return monoStyle
	case spanBold:
		return bodyStyle.Bold(true)
	case spanItalic:
		return bodyStyle.Italic(true)
	case spanStrike:
		return bodyStyle.Strikethrough(true)
	case spanMention:
		return mentionStyle
	default:
		return bodyStyle
	}
}

func matchKind(groups []string) (string, spanKind) {
	for idx, kind := range []spanKind{spanMono, spanBold, spanItalic, spanStrike} {
		if groups[idx+1] != "" {
			return groups[idx+1], kind
		}
	}
	return groups[0], spanPlain
}

// parseMessageWords turns one paragraph of raw message text into styled
// words: inline markup becomes styled spans and mention tokens resolve to
// contact names.
func parseMessageWords(paragraph string, mentions map[string]string) []styledWord {
	var words []styledWord
	appendWords := func(text string, kind spanKind) {
		for _, word := range strings.Fields(text) {
			words = append(words, resolveMentionWord(word, kind, mentions)...)
		}
	}

	rest := paragraph
	for rest != "" {
		loc := inlineMarkup.FindStringSubmatchIndex(rest)
		if loc == nil {
			appendWords(rest, spanPlain)
			break
		}
		appendWords(rest[:loc[0]], spanPlain)
		groups := make([]string, 0, 5)
		for g := 0; g <= 4; g++ {
			if loc[2*g] < 0 {
				groups = append(groups, "")
				continue
			}
			groups = append(groups, rest[loc[2*g]:loc[2*g+1]])
		}
		content, kind := matchKind(groups)
		appendWords(content, kind)
		rest = rest[loc[1]:]
	}
	return words
}

// resolveMentionWord maps an "@123456789" token to "@Name" in the mention
// style when the numeric user part is a known contact. Names may contain
// spaces, so one token can expand to several styled words.
func resolveMentionWord(word string, kind spanKind, mentions map[string]string) []styledWord {
	match := mentionToken.FindStringSubmatch(word)
	if match == nil {
		return []styledWord{{text: word, kind: kind}}
	}
	name, ok := mentions[match[1]]
	if !ok || name == "" {
		return []styledWord{{text: word, kind: spanMention}}
	}
	parts := strings.Fields("@" + name)
	words := make([]styledWord, 0, len(parts))
	for _, part := range parts {
		words = append(words, styledWord{text: part, kind: spanMention})
	}
	if trailing := match[2]; trailing != "" {
		words[len(words)-1].text += trailing
	}
	return words
}

// renderMessageBody wraps a message body to width, applying WhatsApp inline
// markup and mention resolution. Words wrap on plain-text widths and styling
// is applied per same-kind run afterwards, so ANSI sequences never split
// across wraps and unstyled text stays contiguous bytes.
func renderMessageBody(text string, mentions map[string]string, width int) []string {
	width = max(8, width)
	var lines []string
	for _, paragraph := range strings.Split(strings.TrimSpace(text), "\n") {
		words := parseMessageWords(paragraph, mentions)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		var lineWords []styledWord
		used := 0
		flush := func() {
			lines = append(lines, renderWordRuns(lineWords))
			lineWords = nil
			used = 0
		}
		for _, word := range words {
			cells := ansi.StringWidth(word.text)
			if cells > width {
				if used > 0 {
					flush()
				}
				lineWords = []styledWord{{text: truncateText(word.text, width), kind: word.kind}}
				flush()
				continue
			}
			if used > 0 && used+1+cells > width {
				flush()
			}
			if used > 0 {
				used++
			}
			lineWords = append(lineWords, word)
			used += cells
		}
		if used > 0 {
			flush()
		}
	}
	return lines
}

// renderWordRuns renders one wrapped line, styling each run of consecutive
// same-kind words as a single span so plain text stays contiguous bytes.
func renderWordRuns(words []styledWord) string {
	var b strings.Builder
	for i := 0; i < len(words); {
		j := i
		for j < len(words) && words[j].kind == words[i].kind {
			j++
		}
		texts := make([]string, 0, j-i)
		for _, word := range words[i:j] {
			texts = append(texts, word.text)
		}
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(spanStyle(words[i].kind).Render(strings.Join(texts, " ")))
		i = j
	}
	return b.String()
}

// mediaChip renders a compact metadata line for media messages:
// "◆ voice · 0:12", "◆ document · brief.pdf · 2.1 MB".
func mediaChip(msg domain.Message, width int) string {
	if msg.MediaKind == domain.MediaKindNone {
		return ""
	}
	parts := []string{string(msg.MediaKind)}
	if msg.MediaFileName != "" {
		parts = append(parts, msg.MediaFileName)
	}
	if msg.MediaSeconds > 0 {
		parts = append(parts, fmt.Sprintf("%d:%02d", msg.MediaSeconds/60, msg.MediaSeconds%60))
	}
	if msg.MediaFileLength > 0 {
		parts = append(parts, humanFileSize(msg.MediaFileLength))
	}
	chip := chipKeyStyle.Render("◆") + " " + chipLabelStyle.Render(strings.Join(parts, " · "))
	return truncateText(chip, max(12, width))
}

func humanFileSize(bytes uint64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// isMediaPlaceholderText reports whether the message text is exactly a
// synthetic media placeholder ("[image]", "[voice note] rec.ogg"), in which
// case the media chip already carries all its information. Anything beyond
// the placeholder — captions in particular, even ones that happen to start
// with '[' — must render.
func isMediaPlaceholderText(msg domain.Message) bool {
	if msg.MediaKind == domain.MediaKindNone {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	labels := []string{"[" + string(msg.MediaKind) + "]", "[voice note]"}
	for _, label := range labels {
		if text == label {
			return true
		}
		if name := strings.TrimSpace(msg.MediaFileName); name != "" && text == label+" "+name {
			return true
		}
	}
	return false
}

// receiptTicks renders delivery state as compact ticks on the message
// header: ✓ sent, ✓✓ delivered, ✓✓ (accent) read.
func receiptTicks(msg domain.Message) string {
	if !msg.FromMe {
		return ""
	}
	switch msg.Receipt {
	case domain.ReceiptStateRead:
		return receiptReadStyle.Render("✓✓")
	case domain.ReceiptStateDelivered:
		return receiptDeliveredStyle.Render("✓✓")
	case domain.ReceiptStateSent:
		return receiptSentStyle.Render("✓")
	default:
		return ""
	}
}

// senderStyle returns a stable per-sender color for group messages so
// members are visually distinguishable, like WhatsApp's colored names.
func senderStyle(senderJID string) lipgloss.Style {
	if len(senderPalette) == 0 {
		return memberNameStyle
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(senderJID))
	return senderPalette[int(h.Sum32())%len(senderPalette)]
}

// reactionLine aggregates a message's reactions into one compact line:
// names when one or two people reacted, counts beyond that.
func reactionLine(msg domain.Message, width int) string {
	if len(msg.Reactions) == 0 {
		return ""
	}
	order := make([]string, 0, len(msg.Reactions))
	counts := make(map[string]int, len(msg.Reactions))
	names := make(map[string][]string, len(msg.Reactions))
	for _, reaction := range msg.Reactions {
		if counts[reaction.Emoji] == 0 {
			order = append(order, reaction.Emoji)
		}
		counts[reaction.Emoji]++
		names[reaction.Emoji] = append(names[reaction.Emoji], displaySenderLabel(reaction.SenderName))
	}
	parts := make([]string, 0, len(order))
	for _, emoji := range order {
		if len(msg.Reactions) <= 2 {
			parts = append(parts, emoji+" "+strings.Join(names[emoji], ", "))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", emoji, counts[emoji]))
	}
	return truncateText(subtleStyle.Render(strings.Join(parts, " · ")), max(12, width))
}

// renderThreadMessage renders one message: a timestamp + sender + receipt
// header, the formatted body, reactions, and an optional download
// annotation. Own messages are right-aligned to read like a conversation.
// selected marks the react-mode cursor.
func renderThreadMessage(msg domain.Message, width int, mentions map[string]string, selected bool) string {
	width = max(18, width)
	name := msg.SenderName
	if name == "" {
		name = msg.SenderJID
	}
	var nameStyle lipgloss.Style
	switch {
	case msg.FromMe:
		name = "You"
		nameStyle = youNameStyle
	case msg.IsGroup:
		nameStyle = senderStyle(msg.SenderJID)
	default:
		nameStyle = peerNameStyle
	}
	header := timestampStyle.Render(msg.Timestamp.Local().Format("15:04")) +
		"  " + nameStyle.Render(truncateText(name, max(8, width-10)))
	if ticks := receiptTicks(msg); ticks != "" {
		header += "  " + ticks
	}
	if selected {
		header = railStyle.Render("▌ ") + header
	}

	lines := []string{header}
	if !isMediaPlaceholderText(msg) {
		lines = append(lines, renderMessageBody(msg.Text, mentions, width)...)
	}
	if chip := mediaChip(msg, width); chip != "" {
		lines = append(lines, chip)
	}
	if reactions := reactionLine(msg, width); reactions != "" {
		lines = append(lines, reactions)
	}
	if msg.DownloadedPath != "" {
		lines = append(lines, subtleStyle.Render("↳ saved · "+truncateText(filepath.Base(msg.DownloadedPath), max(8, width-10))))
	}
	if msg.FromMe {
		for i, line := range lines {
			if pad := width - ansi.StringWidth(line); pad > 0 {
				lines[i] = strings.Repeat(" ", pad) + line
			}
		}
	}
	return strings.Join(lines, "\n")
}

// dateSeparator renders a "──  Mon, Jul 7  ──" divider line for day
// boundaries in the thread.
func dateSeparator(day, now time.Time, width int) string {
	day = day.Local()
	now = now.Local()
	var label string
	switch {
	case sameDay(day, now):
		label = "Today"
	case sameDay(day.AddDate(0, 0, 1), now):
		label = "Yesterday"
	case day.Year() == now.Year():
		label = day.Format("Mon, Jan 2")
	default:
		label = day.Format("Mon, Jan 2 2006")
	}
	side := max(2, (width-ansi.StringWidth(label)-4)/2)
	rule := strings.Repeat("─", side)
	return hairlineStyle.Render(rule) + "  " + subtleStyle.Render(label) + "  " + hairlineStyle.Render(rule)
}

// resolveMentionNames maps every mention token found in the given texts to
// a contact name, trying the phone-number JID first and the LID alias
// second. Unresolvable mentions are left out and render as raw tokens.
func resolveMentionNames(lookup func(jid string) string, texts []string) map[string]string {
	names := make(map[string]string)
	for _, text := range texts {
		for _, match := range mentionAnywhere.FindAllStringSubmatch(text, -1) {
			digits := match[1]
			if _, seen := names[digits]; seen {
				continue
			}
			name := lookup(digits + "@s.whatsapp.net")
			if name == "" {
				name = lookup(digits + "@lid")
			}
			if name != "" {
				names[digits] = name
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// substituteMentions replaces "@123…" tokens with "@Name" in plain text.
// Used where styled rendering is unavailable, such as chat-list previews.
func substituteMentions(text string, mentions map[string]string) string {
	if len(mentions) == 0 {
		return text
	}
	return mentionAnywhere.ReplaceAllStringFunc(text, func(token string) string {
		if name := mentions[strings.TrimPrefix(token, "@")]; name != "" {
			return "@" + name
		}
		return token
	})
}
