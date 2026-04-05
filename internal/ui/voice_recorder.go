package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type voiceRecorder interface {
	Start() error
	Stop() (voiceRecordingResult, error)
	Cancel() error
}

type voiceRecordingResult struct {
	Path     string
	Duration time.Duration
}

type systemVoiceRecorder struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	path      string
	startedAt time.Time
}

func newSystemVoiceRecorder() voiceRecorder {
	return &systemVoiceRecorder{}
}

func (r *systemVoiceRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd != nil {
		return errors.New("voice recording is already in progress")
	}
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		return fmt.Errorf("gst-launch-1.0 is required for voice notes: %w", err)
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("whatsapp-terminal-voice-%d.ogg", time.Now().UnixNano()))
	args := []string{
		"-q",
		recordingSourceElement(),
		"!",
		"audioconvert",
		"!",
		"audioresample",
		"!",
		"audio/x-raw,rate=48000,channels=1",
		"!",
		"opusenc",
		"audio-type=voice",
		"bitrate=32000",
		"!",
		"oggmux",
		"!",
		"filesink",
		"location=" + path,
	}
	// #nosec G204 -- command and arguments are assembled from fixed internal values, not user input.
	cmd := exec.Command("gst-launch-1.0", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start voice recording: %w", err)
	}
	r.cmd = cmd
	r.path = path
	r.startedAt = time.Now()
	return nil
}

func (r *systemVoiceRecorder) Stop() (voiceRecordingResult, error) {
	r.mu.Lock()
	cmd := r.cmd
	path := r.path
	startedAt := r.startedAt
	r.mu.Unlock()

	if cmd == nil {
		return voiceRecordingResult{}, errors.New("voice recording is not in progress")
	}
	_ = cmd.Process.Signal(os.Interrupt)
	err := cmd.Wait()
	if err != nil && !isExpectedGStreamerStop(err) {
		_ = os.Remove(path)
		r.reset()
		return voiceRecordingResult{}, fmt.Errorf("stop voice recording: %w", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		r.reset()
		return voiceRecordingResult{}, fmt.Errorf("inspect recorded voice note: %w", statErr)
	}
	if info.Size() == 0 {
		_ = os.Remove(path)
		r.reset()
		return voiceRecordingResult{}, errors.New("recorded voice note is empty")
	}
	r.reset()
	return voiceRecordingResult{Path: path, Duration: time.Since(startedAt)}, nil
}

func (r *systemVoiceRecorder) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd == nil {
		return nil
	}
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
	if r.path != "" {
		_ = os.Remove(r.path)
	}
	r.cmd = nil
	r.path = ""
	r.startedAt = time.Time{}
	return nil
}

func (r *systemVoiceRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmd = nil
	r.path = ""
	r.startedAt = time.Time{}
}

func recordingSourceElement() string {
	if err := exec.Command("gst-inspect-1.0", "pipewiresrc").Run(); err == nil {
		return "pipewiresrc"
	}
	return "autoaudiosrc"
}

func isExpectedGStreamerStop(err error) bool {
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.Signaled()
		}
	}
	return false
}
