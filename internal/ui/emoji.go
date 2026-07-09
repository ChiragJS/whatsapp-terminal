package ui

import (
	"regexp"
	"strings"
)

// emojiEntry pairs a Slack-style shortcode with its emoji. The catalog is a
// curated set of the codes people actually type; it is ordered so suggestion
// lists are stable.
type emojiEntry struct {
	name  string
	emoji string
}

var emojiCatalog = []emojiEntry{
	{"joy", "😂"}, {"rofl", "🤣"}, {"smile", "😄"}, {"grin", "😁"},
	{"sweat_smile", "😅"}, {"wink", "😉"}, {"blush", "😊"}, {"innocent", "😇"},
	{"heart_eyes", "😍"}, {"kiss", "😘"}, {"thinking", "🤔"}, {"neutral", "😐"},
	{"smirk", "😏"}, {"unamused", "😒"}, {"eyeroll", "🙄"}, {"grimacing", "😬"},
	{"relieved", "😌"}, {"pensive", "😔"}, {"sleepy", "😴"}, {"cry", "😢"},
	{"sob", "😭"}, {"angry", "😠"}, {"rage", "😡"}, {"scream", "😱"},
	{"astonished", "😲"}, {"open_mouth", "😮"}, {"flushed", "😳"}, {"dizzy_face", "😵"},
	{"exploding_head", "🤯"}, {"cowboy", "🤠"}, {"sunglasses", "😎"}, {"nerd", "🤓"},
	{"worried", "😟"}, {"confused", "😕"}, {"upside_down", "🙃"}, {"zipper_mouth", "🤐"},
	{"face_palm", "🤦"}, {"shrug", "🤷"}, {"salute", "🫡"}, {"melting", "🫠"},
	{"skull", "💀"}, {"clown", "🤡"}, {"ghost", "👻"}, {"robot", "🤖"},
	{"poop", "💩"}, {"heart", "❤️"}, {"orange_heart", "🧡"}, {"yellow_heart", "💛"},
	{"green_heart", "💚"}, {"blue_heart", "💙"}, {"purple_heart", "💜"}, {"black_heart", "🖤"},
	{"white_heart", "🤍"}, {"broken_heart", "💔"}, {"sparkling_heart", "💖"}, {"heartbeat", "💓"},
	{"thumbsup", "👍"}, {"+1", "👍"}, {"thumbsdown", "👎"}, {"-1", "👎"},
	{"ok_hand", "👌"}, {"clap", "👏"}, {"wave", "👋"}, {"raised_hands", "🙌"},
	{"pray", "🙏"}, {"muscle", "💪"}, {"point_up", "☝️"}, {"point_right", "👉"},
	{"crossed_fingers", "🤞"}, {"handshake", "🤝"}, {"v", "✌️"}, {"metal", "🤘"},
	{"fire", "🔥"}, {"sparkles", "✨"}, {"star", "⭐"}, {"zap", "⚡"},
	{"boom", "💥"}, {"tada", "🎉"}, {"confetti", "🎊"}, {"balloon", "🎈"},
	{"gift", "🎁"}, {"trophy", "🏆"}, {"medal", "🏅"}, {"crown", "👑"},
	{"check", "✅"}, {"x", "❌"}, {"warning", "⚠️"}, {"question", "❓"},
	{"exclamation", "❗"}, {"100", "💯"}, {"eyes", "👀"}, {"brain", "🧠"},
	{"bulb", "💡"}, {"rocket", "🚀"}, {"hourglass", "⏳"}, {"clock", "🕐"},
	{"calendar", "📅"}, {"pin", "📌"}, {"paperclip", "📎"}, {"memo", "📝"},
	{"book", "📖"}, {"computer", "💻"}, {"phone", "📱"}, {"camera", "📷"},
	{"mic", "🎤"}, {"music", "🎵"}, {"headphones", "🎧"}, {"game", "🎮"},
	{"coffee", "☕"}, {"tea", "🍵"}, {"beer", "🍺"}, {"cake", "🎂"},
	{"pizza", "🍕"}, {"burger", "🍔"}, {"cookie", "🍪"}, {"apple", "🍎"},
	{"sun", "☀️"}, {"moon", "🌙"}, {"rain", "🌧️"}, {"rainbow", "🌈"},
	{"dog", "🐶"}, {"cat", "🐱"}, {"panda", "🐼"}, {"unicorn", "🦄"},
	{"car", "🚗"}, {"bike", "🚲"}, {"train", "🚆"}, {"plane", "✈️"},
	{"home", "🏠"}, {"office", "🏢"}, {"money", "💰"}, {"gem", "💎"},
	{"sleeping", "💤"}, {"speech", "💬"}, {"wave_bye", "👋"}, {"run", "🏃"},
	{"dance", "💃"}, {"party_face", "🥳"}, {"pleading", "🥺"}, {"smiling_tear", "🥲"},
	{"heart_hands", "🫶"}, {"folded_hands", "🙏"}, {"namaste", "🙏"}, {"diya", "🪔"},
}

var emojiByName = func() map[string]string {
	byName := make(map[string]string, len(emojiCatalog))
	for _, entry := range emojiCatalog {
		if _, exists := byName[entry.name]; !exists {
			byName[entry.name] = entry.emoji
		}
	}
	return byName
}()

// trailingShortcodePrefix matches an unfinished shortcode at the end of the
// draft — the trigger for emoji suggestions.
var trailingShortcodePrefix = regexp.MustCompile(`:([a-z0-9_+-]{2,})$`)

// trailingCompletedShortcode matches a just-finished :shortcode: at the end
// of the draft.
var trailingCompletedShortcode = regexp.MustCompile(`:([a-z0-9_+-]+):$`)

// replaceTrailingEmojiShortcode swaps a completed :shortcode: at the end of
// the draft for its emoji. Unknown codes are left untouched.
func replaceTrailingEmojiShortcode(text string) string {
	match := trailingCompletedShortcode.FindStringSubmatch(text)
	if match == nil {
		return text
	}
	if emoji, ok := emojiByName[match[1]]; ok {
		return text[:len(text)-len(match[0])] + emoji
	}
	return text
}

// emojiSuggestions offers completions while the draft ends in an unfinished
// ":prefix" token. Each suggestion's replacement is the whole draft with the
// token swapped for the emoji.
func emojiSuggestions(draft string, limit int) []pathSuggestion {
	match := trailingShortcodePrefix.FindStringSubmatchIndex(draft)
	if match == nil {
		return nil
	}
	prefix := draft[match[2]:match[3]]
	head := draft[:match[0]]
	var suggestions []pathSuggestion
	for _, entry := range emojiCatalog {
		if !strings.HasPrefix(entry.name, prefix) {
			continue
		}
		suggestions = append(suggestions, pathSuggestion{
			label:       entry.emoji + "  :" + entry.name + ":",
			replacement: head + entry.emoji,
		})
		if len(suggestions) >= limit {
			break
		}
	}
	return suggestions
}
