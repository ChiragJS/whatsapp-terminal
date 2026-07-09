package app_test

import (
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestDemoModePTYSmoke(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(filepath.Join("..", ".."))
	tempDir := t.TempDir()

	cmd := exec.Command("go", "run", "./cmd/whatsapp-terminal", "--demo", "--no-alt-screen", "--data-dir", tempDir)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start() error = %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(done)
	}()

	waitForSubstring(t, &out, "Project Alpha")

	if _, err := ptmx.Write([]byte("\r")); err != nil {
		t.Fatalf("write enter error = %v", err)
	}
	waitForSubstring(t, &out, "PROJECT ALPHA")
	waitForSubstring(t, &out, "I’ll review the summary tonight")

	if _, err := ptmx.Write([]byte("\x03")); err != nil {
		t.Fatalf("write quit error = %v", err)
	}

	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatalf("process exit error = %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("demo app did not exit in time\noutput:\n%s", out.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("pty reader did not finish")
	}

	// Autowrap (DECAWM) must be disabled while the TUI runs — over-wide
	// glyph runs would otherwise desynchronize repaints — and restored on
	// exit so the shell is left intact.
	if !strings.Contains(out.String(), "\x1b[?7l") {
		t.Fatal("expected the app to disable terminal autowrap on start")
	}
	if !strings.Contains(out.String(), "\x1b[?7h") {
		t.Fatal("expected the app to restore terminal autowrap on exit")
	}
}

// TestComposerLongDraftScrollsToLatestLine is a regression test for a bug
// where a multi-line draft that outgrew the composer's capped height froze
// at its earliest lines: the box never scrolled to follow the cursor, so
// everything just typed was invisible above a box that had visibly grown
// with room to show it. It drives the real compiled binary through a real
// PTY — the bug depended on bubbles' textarea internals in a way that
// synthetic Update()-only test sequences did not reproduce.
func TestComposerLongDraftScrollsToLatestLine(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(filepath.Join("..", ".."))
	tempDir := t.TempDir()

	cmd := exec.Command("go", "run", "./cmd/whatsapp-terminal", "--demo", "--no-alt-screen", "--data-dir", tempDir)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start() error = %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatalf("pty.Setsize() error = %v", err)
	}

	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(done)
	}()

	waitForSubstring(t, &out, "Project Alpha")
	if _, err := ptmx.Write([]byte("\r")); err != nil {
		t.Fatalf("write enter error = %v", err)
	}
	waitForSubstring(t, &out, "PROJECT ALPHA")
	if _, err := ptmx.Write([]byte("i")); err != nil {
		t.Fatalf("write compose error = %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// A draft comfortably taller than the composer's height cap at this
	// terminal size (7 rows): 10 lines, newest last.
	const lineCount = 10
	for i := 0; i < lineCount; i++ {
		if i > 0 {
			if _, err := ptmx.Write([]byte{0x0A}); err != nil { // ctrl+j
				t.Fatalf("write newline error = %v", err)
			}
			time.Sleep(30 * time.Millisecond)
		}
		if _, err := ptmx.Write([]byte("L" + string(rune('0'+i)))); err != nil {
			t.Fatalf("write line error = %v", err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	waitForSubstring(t, &out, "L9")
	time.Sleep(300 * time.Millisecond)

	// bubbletea's renderer only reprints screen lines that actually changed,
	// so a static marker like the toolbar hint can appear in an earlier,
	// stale frame and never again — anchoring on it risks slicing into that
	// stale frame instead of the true final one. "L9" has no such problem:
	// it is only ever written once the draft is fully typed, so its last
	// (and only) occurrence unambiguously anchors the final composer frame.
	// The window is also not simply "the stream's tail": background ticks
	// (error-banner expiry, keybindings reload) can append trailing bytes
	// after the composer redraw completes.
	snapshot := out.String()
	anchor := strings.LastIndex(snapshot, "L9")
	if anchor < 0 {
		t.Fatalf("could not find L9 in output despite waitForSubstring succeeding:\n%s", snapshot)
	}
	start := max(0, anchor-1200)
	end := min(len(snapshot), anchor+400)
	latestFrame := snapshot[start:end]

	for _, want := range []string{"L3", "L4", "L5", "L6", "L7", "L8", "L9"} {
		if !strings.Contains(latestFrame, want) {
			t.Fatalf("latest composer frame missing %q — draft should scroll to show the trailing window:\n%s", want, latestFrame)
		}
	}
	if strings.Contains(latestFrame, "L0") || strings.Contains(latestFrame, "L1") || strings.Contains(latestFrame, "L2") {
		t.Fatalf("latest composer frame still shows the earliest lines — draft is frozen instead of scrolling:\n%s", latestFrame)
	}

	if _, err := ptmx.Write([]byte("\x1b")); err != nil { // esc: cancel draft, don't send it
		t.Fatalf("write esc error = %v", err)
	}
	// A bare ESC immediately followed by another byte can be misparsed as
	// the start of a longer escape sequence (arrow keys, etc.); give the
	// terminal input reader time to resolve it as a standalone Esc before
	// sending ctrl+c, exactly as a human pressing two separate keys would.
	time.Sleep(100 * time.Millisecond)
	if _, err := ptmx.Write([]byte("\x03")); err != nil {
		t.Fatalf("write quit error = %v", err)
	}

	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatalf("process exit error = %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("demo app did not exit in time\noutput:\n%s", out.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("pty reader did not finish")
	}
}

func waitForSubstring(t *testing.T, out *bytes.Buffer, needle string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), needle) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q\noutput:\n%s", needle, out.String())
}
