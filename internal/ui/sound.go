package ui

import (
	"fmt"
	"os"
)

type sounder interface {
	Bell() error
}

type terminalBell struct{}

func newTerminalBell() sounder {
	return terminalBell{}
}

func (terminalBell) Bell() error {
	_, err := fmt.Fprint(os.Stdout, "\a")
	return err
}
