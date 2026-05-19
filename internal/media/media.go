package media

import (
	"mime"
	"path/filepath"
	"strings"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

func KindForPath(path string) domain.MediaKind {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return domain.MediaKindImage
	case ".mp4", ".mov", ".mkv", ".webm":
		return domain.MediaKindVideo
	case ".mp3", ".wav", ".m4a", ".aac", ".ogg", ".opus":
		return domain.MediaKindAudio
	}
	return KindForMIME(mime.TypeByExtension(ext))
}

func KindForMIME(mimeType string) domain.MediaKind {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return domain.MediaKindImage
	case strings.HasPrefix(mimeType, "video/"):
		return domain.MediaKindVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return domain.MediaKindAudio
	default:
		return domain.MediaKindDocument
	}
}

func Extension(mimeType string, kind domain.MediaKind) string {
	if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		return extensions[0]
	}
	switch kind {
	case domain.MediaKindImage:
		return ".img"
	case domain.MediaKindVideo:
		return ".mp4"
	case domain.MediaKindVoice:
		return ".ogg"
	case domain.MediaKindAudio:
		return ".audio"
	case domain.MediaKindDocument:
		return ".bin"
	default:
		return ".dat"
	}
}

func Preview(kind domain.MediaKind, fileName, caption string) string {
	label := "[" + string(kind) + "]"
	if kind == domain.MediaKindVoice {
		label = "[voice note]"
	}
	preview := strings.TrimSpace(label + " " + fileName)
	if caption = strings.TrimSpace(caption); caption != "" {
		preview += " — " + caption
	}
	return preview
}

func AttachmentToken(fileName string, kind domain.MediaKind) string {
	switch kind {
	case domain.MediaKindImage:
		return "[Image: " + fileName + "]"
	case domain.MediaKindVideo:
		return "[Video: " + fileName + "]"
	case domain.MediaKindAudio:
		return "[Audio: " + fileName + "]"
	case domain.MediaKindVoice:
		return "[Voice: " + fileName + "]"
	default:
		return "[File: " + fileName + "]"
	}
}

func StatusLabel(kind domain.MediaKind) string {
	switch kind {
	case domain.MediaKindVoice:
		return "Voice note"
	case domain.MediaKindImage:
		return "Image"
	case domain.MediaKindVideo:
		return "Video"
	case domain.MediaKindAudio:
		return "Audio"
	default:
		return "Media"
	}
}
