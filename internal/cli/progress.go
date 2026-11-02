package cli

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	spinnerDelay = 150 * time.Millisecond
	spinnerFrame = 80 * time.Millisecond
)

type progressWriter struct {
	mu sync.Mutex
	io.Writer
	// width is the spinner line being shown, in cells; it is blanked before
	// anything else is written so a log line never lands on top of it.
	width int
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.clear()
	return writer.Writer.Write(data)
}

func (writer *progressWriter) clear() {
	if writer.width != 0 {
		fmt.Fprintf(writer.Writer, "\r%s\r", strings.Repeat(" ", writer.width))
		writer.width = 0
	}
}

// draw replaces the spinner line. It uses only carriage returns and spaces, so
// it needs no escape sequences and shows on every terminal.
func (writer *progressWriter) draw(line string, columns int) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	// The last column would wrap on some terminals and then stop overwriting.
	if count := utf8.RuneCountInString(line); columns > 1 && count >= columns {
		line = string([]rune(line)[:columns-2]) + "…"
	}
	count := utf8.RuneCountInString(line)
	fmt.Fprintf(writer.Writer, "\r%s%s", line, strings.Repeat(" ", max(0, writer.width-count)))
	writer.width = count
}

// terminalColumns reports the width of the terminal behind writer, or false
// when output goes to a pipe or file, where a spinner would be noise.
func terminalColumns(writer io.Writer) (int, bool) {
	progress, ok := writer.(*progressWriter)
	if !ok {
		return 0, false
	}
	file, ok := progress.Writer.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return 0, false
	}
	columns, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 80, true
	}
	return columns, true
}

// Delaying the first line keeps fast commands silent; plain lines work in logs too.
func startProgress(writer io.Writer, operation string, interval time.Duration) func() {
	return watchProgress(writer, func() string { return operation }, interval)
}

// stepLabel is the operation a spinner shows, with the step running now.
type stepLabel struct {
	operation string
	step      atomic.Pointer[string]
}

// set takes the step in any case; the resolver's messages are written as lines.
func (label *stepLabel) set(step string) {
	if step != "" {
		step = strings.ToLower(step[:1]) + step[1:]
	}
	label.step.Store(&step)
}
func (label *stepLabel) String() string {
	if step := label.step.Load(); step != nil && *step != "" {
		return label.operation + ": " + *step
	}
	return label.operation
}

// watchProgress asks for the operation on each update, so it names the step
// running then: a spinner on a terminal, plain lines anywhere else.
func watchProgress(writer io.Writer, operation func() string, interval time.Duration) func() {
	if progress, ok := writer.(*progressWriter); ok {
		if columns, terminal := terminalColumns(writer); terminal {
			// Warnings from staging arrive through log; give them the same
			// writer so they scroll above the spinner.
			output, flags := log.Writer(), log.Flags()
			log.SetOutput(writer)
			log.SetFlags(0)
			stop := runSpinner(progress, operation, columns)
			return sync.OnceFunc(func() {
				stop()
				log.SetOutput(output)
				log.SetFlags(flags)
			})
		}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				fmt.Fprintf(writer, "agentpack: %s… (%s elapsed)\n", operation(), time.Since(started).Round(time.Second))
				timer.Reset(5 * interval)
			}
		}
	}()
	return sync.OnceFunc(func() { close(stop); <-done })
}

func runSpinner(writer *progressWriter, operation func() string, columns int) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		select {
		case <-stop:
			return
		case <-time.After(spinnerDelay):
		}
		ticker := time.NewTicker(spinnerFrame)
		defer ticker.Stop()
		for frame := 0; ; frame++ {
			line := spinnerFrames[frame%len(spinnerFrames)] + " " + operation()
			if elapsed := time.Since(started); elapsed >= time.Second {
				line += fmt.Sprintf(" (%s)", elapsed.Round(time.Second))
			}
			writer.draw(line, columns)
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(stop)
		<-done
		writer.mu.Lock()
		defer writer.mu.Unlock()
		writer.clear()
	})
}
