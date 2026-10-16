package product

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/x/term"
)

type progressLine struct {
	writer   io.Writer
	terminal bool
	stop     chan struct{}
	done     chan struct{}
}

func startProgress(w io.Writer, message string) *progressLine {
	p := &progressLine{writer: w}
	if file, ok := w.(*os.File); ok {
		p.terminal = term.IsTerminal(file.Fd())
	}
	if !p.terminal {
		fmt.Fprintf(w, "%s...\n", message)
		return p
	}
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	frames := []string{"◐", "◓", "◑", "◒"}
	fmt.Fprintf(w, "\r\033[2K%s %s", frames[0], message)
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 1
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				fmt.Fprintf(w, "\r\033[2K%s %s", frames[frame%len(frames)], message)
				frame++
			}
		}
	}()
	return p
}

func (p *progressLine) finish(message string, success bool) {
	if p.terminal {
		close(p.stop)
		<-p.done
		fmt.Fprint(p.writer, "\r\033[2K")
	}
	if !success {
		return
	}
	printSuccess(p.writer, message)
}

func printSuccess(w io.Writer, message string) {
	file, ok := w.(*os.File)
	if ok && term.IsTerminal(file.Fd()) {
		fmt.Fprintf(w, "\033[32m✓ %s\033[0m\n", message)
	} else {
		fmt.Fprintf(w, "✓ %s\n", message)
	}
}
