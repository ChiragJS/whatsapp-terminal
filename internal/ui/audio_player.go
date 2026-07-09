package ui

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// audioPlayer plays downloaded voice notes and audio files through a system
// player. One playback at a time; starting a new one replaces the previous.
type audioPlayer interface {
	Play(path string) error
	Stop() error
	Playing() bool
}

type systemAudioPlayer struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func newSystemAudioPlayer() audioPlayer {
	return &systemAudioPlayer{}
}

// playerCommand picks the first available system player. gst-launch-1.0 is
// first because voice recording already requires it, so voice-note playback
// needs no extra dependency.
func playerCommand(path string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("gst-launch-1.0"); err == nil {
		// #nosec G204 -- fixed pipeline; path is a file this app downloaded.
		return exec.Command("gst-launch-1.0", "-q",
			"filesrc", "location="+path,
			"!", "decodebin", "!", "audioconvert", "!", "audioresample", "!", "autoaudiosink",
		), nil
	}
	if _, err := exec.LookPath("ffplay"); err == nil {
		// #nosec G204 -- fixed arguments; path is a file this app downloaded.
		return exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", path), nil
	}
	if _, err := exec.LookPath("mpv"); err == nil {
		// #nosec G204 -- fixed arguments; path is a file this app downloaded.
		return exec.Command("mpv", "--no-video", "--really-quiet", path), nil
	}
	return nil, errors.New("no audio player found: install gstreamer (gst-launch-1.0), ffplay, or mpv")
}

func (p *systemAudioPlayer) Play(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		// Kill only: the reaper goroutine started with this cmd owns Wait,
		// and exec.Cmd.Wait is not safe to call concurrently.
		_ = p.cmd.Process.Kill()
		p.cmd = nil
	}
	cmd, err := playerCommand(path)
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start audio playback: %w", err)
	}
	p.cmd = cmd
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
		}
		p.mu.Unlock()
	}()
	return nil
}

func (p *systemAudioPlayer) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	p.cmd = nil
	return nil
}

func (p *systemAudioPlayer) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}
