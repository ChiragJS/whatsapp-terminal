package app_test

import (
	"bytes"
	"fmt"
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

	// The composer is allowed to grow generously before it must scroll
	// internally (composerGeometry's maxHeight), so this needs a draft
	// comfortably past that cap to exercise genuine overflow — well above
	// what 10 short lines would need at a 30-row terminal.
	const lineCount = 25
	last := fmt.Sprintf("L%d", lineCount-1)
	for i := 0; i < lineCount; i++ {
		if i > 0 {
			if _, err := ptmx.Write([]byte{0x0A}); err != nil { // ctrl+j
				t.Fatalf("write newline error = %v", err)
			}
			time.Sleep(30 * time.Millisecond)
		}
		if _, err := ptmx.Write([]byte(fmt.Sprintf("L%d", i))); err != nil {
			t.Fatalf("write line error = %v", err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	waitForSubstring(t, &out, last)
	time.Sleep(300 * time.Millisecond)

	// bubbletea's renderer only reprints screen lines that actually changed,
	// so a static marker like the toolbar hint can appear in an earlier,
	// stale frame and never again — anchoring on it risks slicing into that
	// stale frame instead of the true final one. The last line's label has
	// no such problem: it is only ever written once the draft is fully
	// typed, so its last (and only) occurrence unambiguously anchors the
	// final composer frame. The window is also not simply "the stream's
	// tail": background ticks (error-banner expiry, keybindings reload) can
	// append trailing bytes after the composer redraw completes.
	snapshot := out.String()
	anchor := strings.LastIndex(snapshot, last)
	if anchor < 0 {
		t.Fatalf("could not find %q in output despite waitForSubstring succeeding:\n%s", last, snapshot)
	}
	// A blink-driven partial redraw can touch only a line or two and land
	// after the true full-frame paint, so anchoring on "L24" alone with a
	// small fixed window can slice into that noise instead of the frame
	// that actually contains the whole box. Walk back from the anchor to
	// the nearest preceding box-top-left corner ("┌"), which only appears
	// on a full repaint, and take the window from there.
	frameStart := strings.LastIndex(snapshot[:anchor], "┌")
	if frameStart < 0 {
		frameStart = max(0, anchor-2500)
	}
	end := min(len(snapshot), anchor+400)
	latestFrame := snapshot[frameStart:end]

	for i := lineCount - 5; i < lineCount; i++ {
		want := fmt.Sprintf("L%d", i)
		if !strings.Contains(latestFrame, want) {
			t.Fatalf("latest composer frame missing %q — draft should scroll to show the trailing window:\n%s", want, latestFrame)
		}
	}
	// A trailing space anchors an exact line label: "L1 " cannot match
	// inside "L10"/"L19", which legitimately appear in the visible window.
	for _, unwanted := range []string{"L0 ", "L1 ", "L2 "} {
		if strings.Contains(latestFrame, unwanted) {
			t.Fatalf("latest composer frame still shows an early line %q — draft is frozen instead of scrolling:\n%s", unwanted, latestFrame)
		}
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

// TestComposerSingleLongLineShowsAllWrappedRows is a regression test for a
// bug where typing one long word-free run of characters (no spaces, no
// newlines — e.g. holding a key down) into the composer silently dropped a
// middle visual row: the textarea wraps that one logical line across
// several rows, and when its allotted Height exactly matched the number of
// rows needed, bubbles' textarea render skipped one of them. The fix keeps
// one spare row of headroom at all times. Verified against the real
// compiled binary: a synthetic in-process reproduction of this exact bug
// needed several isolated attempts before the true cause (not the several
// plausible-looking ones tried first) was confirmed.
func TestComposerSingleLongLineShowsAllWrappedRows(t *testing.T) {
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

	// At 98 columns of usable width, an unbroken run this long wraps across
	// three visual rows (roughly 94 + 94 + a remainder) — comfortably
	// enough to expose a dropped middle row without needing to overflow
	// the height cap at all. A trailing unique marker (not more "j"s) gives
	// something to anchor on: a run of identical characters has no
	// substring that identifies the true final frame among the many
	// intermediate ones already in the buffer.
	const jCount = 194
	const marker = "ZEBRAX"
	for i := 0; i < jCount; i++ {
		if _, err := ptmx.Write([]byte("j")); err != nil {
			t.Fatalf("write char error = %v", err)
		}
		time.Sleep(8 * time.Millisecond)
	}
	for _, r := range marker {
		if _, err := ptmx.Write([]byte(string(r))); err != nil {
			t.Fatalf("write marker error = %v", err)
		}
		time.Sleep(8 * time.Millisecond)
	}
	waitForSubstring(t, &out, marker)
	time.Sleep(300 * time.Millisecond)

	// bubbletea's renderer only reprints lines that changed, so anchor on
	// the marker's last occurrence and walk back to the nearest preceding
	// box-top-left corner ("┌"), which only appears on a full repaint —
	// exactly the technique verified in TestComposerLongDraftScrollsToLatestLine.
	snapshot := out.String()
	anchor := strings.LastIndex(snapshot, marker)
	if anchor < 0 {
		t.Fatalf("could not find marker %q despite waitForSubstring succeeding:\n%s", marker, snapshot)
	}
	frameStart := strings.LastIndex(snapshot[:anchor], "┌")
	if frameStart < 0 {
		frameStart = max(0, anchor-3000)
	}
	// A fixed offset past the marker, not a search for the closing "└": a
	// redraw that only touches the marker's own line (a cursor blink, say)
	// can legitimately omit the border entirely if bubbletea's diffing
	// renderer treats it as unchanged from the previous full frame.
	frame := snapshot[frameStart:min(len(snapshot), anchor+400)]
	if !strings.Contains(frame, marker) {
		t.Fatalf("frame window lost the marker itself — window logic is wrong:\n%s", frame)
	}
	visibleJs := strings.Count(frame, "j")
	if visibleJs < jCount {
		t.Fatalf("composer shows %d of %d typed 'j' characters — a wrapped row was dropped:\n%s", visibleJs, jCount, frame)
	}

	if _, err := ptmx.Write([]byte("\x1b")); err != nil { // esc: cancel draft, don't send it
		t.Fatalf("write esc error = %v", err)
	}
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
