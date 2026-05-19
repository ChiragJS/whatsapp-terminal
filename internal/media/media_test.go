package media

import (
	"testing"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

func TestKindForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want domain.MediaKind
	}{
		{name: "image", path: "photo.webp", want: domain.MediaKindImage},
		{name: "video", path: "clip.mkv", want: domain.MediaKindVideo},
		{name: "audio", path: "voice.opus", want: domain.MediaKindAudio},
		{name: "document", path: "notes.unknown", want: domain.MediaKindDocument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := KindForPath(tt.path); got != tt.want {
				t.Fatalf("KindForPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	t.Parallel()

	if got := Preview(domain.MediaKindVoice, "note.ogg", ""); got != "[voice note] note.ogg" {
		t.Fatalf("Preview(voice) = %q", got)
	}
	if got := Preview(domain.MediaKindImage, "photo.jpg", "  hello  "); got != "[image] photo.jpg — hello" {
		t.Fatalf("Preview(image caption) = %q", got)
	}
}

func TestExtension(t *testing.T) {
	t.Parallel()

	if got := Extension("audio/ogg", domain.MediaKindVoice); got != ".oga" && got != ".ogg" {
		t.Fatalf("Extension(audio/ogg) = %q", got)
	}
	if got := Extension("", domain.MediaKindVoice); got != ".ogg" {
		t.Fatalf("Extension(voice fallback) = %q", got)
	}
	if got := Extension("", domain.MediaKindDocument); got != ".bin" {
		t.Fatalf("Extension(document fallback) = %q", got)
	}
}
