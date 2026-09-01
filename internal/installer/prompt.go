package installer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"
)

type prompt struct {
	input  *os.File
	output io.Writer
	reader *bufio.Reader
	close  func() error
}

func openPrompt() (*prompt, error) {
	if runtime.GOOS != "windows" {
		terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err == nil {
			return &prompt{input: terminal, output: terminal, reader: bufio.NewReader(terminal), close: terminal.Close}, nil
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("no interactive terminal is available; use HOSTPIN_NONINTERACTIVE=1 with HOSTPIN_PIN and HOSTPIN_NODE_NAME")
	}
	return &prompt{input: os.Stdin, output: os.Stderr, reader: bufio.NewReader(os.Stdin), close: func() error { return nil }}, nil
}

func (p *prompt) ask(label, fallback string) (string, error) {
	if fallback == "" {
		_, _ = fmt.Fprintf(p.output, "%s: ", label)
	} else {
		_, _ = fmt.Fprintf(p.output, "%s [%s]: ", label, fallback)
	}
	value, err := p.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (p *prompt) password(label string) (string, error) {
	_, _ = fmt.Fprintf(p.output, "%s: ", label)
	value, err := term.ReadPassword(int(p.input.Fd()))
	_, _ = fmt.Fprintln(p.output)
	return strings.TrimSpace(string(value)), err
}

func (p *prompt) confirm(label string, fallback bool) (bool, error) {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}
	value, err := p.ask(label+" ("+hint+")", "")
	if err != nil {
		return false, err
	}
	if value == "" {
		return fallback, nil
	}
	switch strings.ToLower(value) {
	case "y", "yes", "是":
		return true, nil
	case "n", "no", "否":
		return false, nil
	default:
		return false, fmt.Errorf("please answer yes or no")
	}
}
