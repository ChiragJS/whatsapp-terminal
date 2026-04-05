package ui

import (
	"fmt"

	sysclipboard "golang.design/x/clipboard"
)

type clipboardReader interface {
	ReadImage() ([]byte, error)
}

type systemClipboard struct {
	initErr error
}

func newSystemClipboard() clipboardReader {
	return &systemClipboard{initErr: sysclipboard.Init()}
}

func (c *systemClipboard) ReadImage() ([]byte, error) {
	if c.initErr != nil {
		return nil, fmt.Errorf("clipboard image access unavailable: %w", c.initErr)
	}
	data := sysclipboard.Read(sysclipboard.FmtImage)
	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard does not currently contain an image")
	}
	return data, nil
}
