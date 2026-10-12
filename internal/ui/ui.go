// Package ui renders dial's terminal output: compact tables, usage bars
// and relative reset times, coloured only when stdout is a terminal.
package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

var Color = isTTY() && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func wrap(code, s string) string {
	if !Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Bold(s string) string   { return wrap("1", s) }
func Dim(s string) string    { return wrap("2", s) }
func Green(s string) string  { return wrap("32", s) }
func Yellow(s string) string { return wrap("33", s) }
func Red(s string) string    { return wrap("31", s) }
func Cyan(s string) string   { return wrap("36", s) }
func Orange(s string) string { return wrap("38;5;209", s) }

// Level colours a percentage: green <70, yellow <90, red otherwise.
func Level(pct float64, s string) string {
	switch {
	case pct >= 90:
		return Red(s)
	case pct >= 70:
		return Yellow(s)
	default:
		return Green(s)
	}
}

// Bar draws a fixed-width meter using eighth blocks for smoothness.
func Bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	cells := pct / 100 * float64(width)
	full := int(cells)
	frac := cells - float64(full)
	parts := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	if full < width {
		if p := parts[int(math.Round(frac*7))]; p != "" {
			b.WriteString(p)
		} else {
			b.WriteString(Dim("·"))
		}
		rest := width - full - 1
		if rest > 0 {
			b.WriteString(Dim(strings.Repeat("·", rest)))
		}
	}
	return Level(pct, b.String())
}

// Until renders a reset time as "2h13m" / "3d4h" / "now".
func Until(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "now"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// Ago renders a past time as "just now" / "5m ago" / "3h ago".
func Ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

// When renders an absolute local time compactly ("Thu 09:00").
func When(t time.Time) string {
	lt := t.Local()
	if time.Until(t) < 20*time.Hour {
		return lt.Format("15:04")
	}
	return lt.Format("Mon 15:04")
}

// visibleLen ignores ANSI escapes.
func visibleLen(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && r == 'm':
			esc = false
		case !esc:
			n++
		}
	}
	return n
}

// Pad right-pads s to width visible columns, truncating plain text.
func Pad(s string, width int) string {
	if l := visibleLen(s); l < width {
		return s + strings.Repeat(" ", width-l)
	}
	if !strings.Contains(s, "\x1b") && utf8.RuneCountInString(s) > width {
		r := []rune(s)
		return string(r[:width-1]) + "…"
	}
	return s
}

func Fatal(err error) {
	fmt.Fprintln(os.Stderr, Red("error: ")+err.Error())
	os.Exit(1)
}

func Warn(msg string) { fmt.Fprintln(os.Stderr, Yellow("! ")+msg) }
func OK(msg string)   { fmt.Println(Green("✓ ") + msg) }
