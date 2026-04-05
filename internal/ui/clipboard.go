package ui

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type clipboardReader interface {
	ReadImage() ([]byte, error)
}

type systemClipboard struct{}

func newSystemClipboard() clipboardReader {
	return &systemClipboard{}
}

func (c *systemClipboard) ReadImage() ([]byte, error) {
	switch runtime.GOOS {
	case "linux":
		return readLinuxClipboardImage()
	case "darwin":
		return readMacClipboardImage()
	case "windows":
		return nil, fmt.Errorf("clipboard image access is not yet supported on Windows releases")
	default:
		return nil, fmt.Errorf("clipboard image access is not supported on %s", runtime.GOOS)
	}
}

func readLinuxClipboardImage() ([]byte, error) {
	if data, err := readWaylandClipboardImage(); err == nil {
		return data, nil
	}
	if data, err := readX11ClipboardImage(); err == nil {
		return data, nil
	}
	return nil, fmt.Errorf("clipboard image access unavailable: install wl-clipboard for Wayland or xclip for X11")
}

func readWaylandClipboardImage() ([]byte, error) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, err
	}
	out, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(out), "image/png") {
		return nil, fmt.Errorf("clipboard does not currently contain a PNG image")
	}
	data, err := exec.Command("wl-paste", "--no-newline", "--type", "image/png").Output()
	if err != nil {
		return nil, err
	}
	return validateClipboardImage(data)
}

func readX11ClipboardImage() ([]byte, error) {
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, err
	}
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(out), "image/png") {
		return nil, fmt.Errorf("clipboard does not currently contain a PNG image")
	}
	data, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
	if err != nil {
		return nil, err
	}
	return validateClipboardImage(data)
}

func readMacClipboardImage() ([]byte, error) {
	if _, err := exec.LookPath("pngpaste"); err != nil {
		return nil, fmt.Errorf("clipboard image access unavailable: install pngpaste")
	}
	data, err := exec.Command("pngpaste", "-").Output()
	if err != nil {
		return nil, err
	}
	return validateClipboardImage(data)
}

func validateClipboardImage(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("clipboard does not currently contain an image")
	}
	return data, nil
}
