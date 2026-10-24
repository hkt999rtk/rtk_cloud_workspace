package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
)

func environmentProgressTerminal(out io.Writer) (enabled bool, columns int, color bool) {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok || os.Getenv("TERM") == "dumb" || !isatty.IsTerminal(file.Fd()) && !isatty.IsCygwinTerminal(file.Fd()) {
		return false, 0, false
	}
	columns = environmentTerminalColumns(file.Fd())
	if columns <= 0 {
		columns, _ = strconv.Atoi(os.Getenv("COLUMNS"))
	}
	if columns <= 0 {
		columns = 80
	}
	if columns < 20 {
		return false, columns, false
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	return true, min(columns, 160), !noColor
}

func (p *environmentProgress) clearTerminalLine() {
	if p.lineVisible {
		fmt.Fprint(p.out, "\r\x1b[2K")
		p.lineVisible = false
	}
}

func (p *environmentProgress) terminalMetric() (done, total int, label string) {
	if p.total > 0 {
		return p.checked, p.total, "checks"
	}
	if p.resources > 0 {
		for _, step := range []string{".verification", ".acceptance", ".rollout", ".ownership", ".journal", ".cleanup"} {
			if strings.HasSuffix(p.stage, step) {
				return p.completed, 0, fmt.Sprintf("resources %d/%d", p.completed, p.resources)
			}
		}
		return p.completed, p.resources, "resources"
	}
	return 0, 0, "working"
}

func progressClip(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	if limit < 4 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func terminalProgressTarget(resource string) string {
	parts := strings.Split(resource, "/")
	if len(parts) != 3 {
		return ""
	}
	if parts[2] != "" {
		return parts[0] + "/" + parts[2]
	}
	if parts[1] == "" {
		return parts[0] + "/*"
	}
	return parts[0] + "/" + parts[1] + "/*"
}

// The colored, in-place bar follows progre.sh's terminal presentation. Its
// percentage is a real batch count; unknown work uses a moving marker instead.
func (p *environmentProgress) writeTerminal() {
	if len(p.durablePending) > 0 {
		return
	}
	if p.hasTerminalFD {
		if width := environmentTerminalColumns(p.terminalFD); width > 0 {
			p.columns = min(width, 160)
			if width < 20 {
				p.clearTerminalLine()
				return
			}
		}
	}
	columns := p.columns
	if columns <= 0 {
		columns = 80
	}
	width := min(20, max(4, columns/5))
	done, total, label := p.terminalMetric()
	filled, metric := 0, label
	if total > 0 {
		filled = int(float64(done) / float64(total) * float64(width))
		metric = fmt.Sprintf("%3d%% %s %d/%d", int(float64(done)/float64(total)*100), label, done, total)
	}
	var bar strings.Builder
	for index := 0; index < width; index++ {
		active := total > 0 && index < filled || total == 0 && index == p.frame%width
		glyph, color := ".", 90
		if active {
			glyph = "#"
			color = []int{31, 33, 36, 32}[min(3, index*4/width)]
		}
		if p.color {
			fmt.Fprintf(&bar, "\x1b[%dm%s", color, glyph)
		} else {
			bar.WriteString(glyph)
		}
	}
	if p.color {
		bar.WriteString("\x1b[0m")
	}
	spinner := string("|/-\\"[p.frame%4])
	prefix := fmt.Sprintf("%s [%s] %s | ", spinner, strings.Repeat(".", width), metric)
	detail := environmentProgressStageTask(p.stage)
	if p.resource != "" {
		detail = p.step + " " + terminalProgressTarget(p.resource)
	}
	if p.rollout != "" {
		fields := strings.Fields(p.rollout)
		if len(fields) == 4 {
			detail = fields[1] + " " + strings.Replace(fields[3], "rollout-limit=", "limit=", 1) + " " + terminalProgressTarget(strings.TrimPrefix(fields[0], "waiting-for="))
		}
	} else if len(p.operations) > 0 {
		var first uint64
		for id := range p.operations {
			if first == 0 || id < first {
				first = id
			}
		}
		op := p.operations[first]
		detail = environmentProgressActions[op.action]
		if op.resource != "" {
			detail = op.action + " " + terminalProgressTarget(op.resource)
		}
		if len(p.operations) > 1 {
			detail = fmt.Sprintf("active=%d ", len(p.operations)) + detail
		}
	}
	if p.round != "" {
		detail = "[" + p.round + "] " + detail
	}
	remaining := columns - 1 - len(prefix)
	if remaining < 0 {
		// Very narrow terminals still receive a complete, nonwrapping frame.
		frame := progressClip(fmt.Sprintf("%s [%s] %s", spinner, strings.Repeat("#", filled)+strings.Repeat(".", width-filled), metric), columns-1)
		fmt.Fprintf(p.out, "\r\x1b[2K%s", frame)
	} else {
		fmt.Fprintf(p.out, "\r\x1b[2K%s [%s] %s | %s", spinner, bar.String(), metric, progressClip(detail, remaining))
	}
	p.frame++
	p.lineVisible = true
}

type environmentProgressResultWriter struct{ progress *environmentProgress }

func (w environmentProgressResultWriter) deploymentColorEnabled() bool { return w.progress.color }

// Durable acceptance lines share the renderer lock: clear, print, then redraw.
// Partial writes are buffered until their newline so animation cannot split them.
func (w environmentProgressResultWriter) Write(data []byte) (int, error) {
	p := w.progress
	p.mu.Lock()
	defer p.mu.Unlock()
	p.durablePending = append(p.durablePending, data...)
	last := strings.LastIndexByte(string(p.durablePending), '\n')
	if last >= 0 {
		p.clearTerminalLine()
		written, err := p.out.Write(p.durablePending[:last+1])
		if written < 0 || written > last+1 {
			written, err = 0, io.ErrShortWrite
		}
		p.durablePending = p.durablePending[written:]
		if err == nil && written != last+1 {
			err = io.ErrShortWrite
		}
		if err != nil {
			return len(data), err
		}
	}
	p.writeTerminal()
	return len(data), nil
}

func environmentProgressResultOutput(ctx context.Context, fallback io.Writer) io.Writer {
	if p, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok && p.terminal {
		return environmentProgressResultWriter{p}
	}
	return fallback
}
