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
	waitForSubstring(t, &out, "esc back")
	waitForSubstring(t, &out, "I’ll review the summary tonight")

	if _, err := ptmx.Write([]byte("q")); err != nil {
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
