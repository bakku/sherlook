package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A594FF"}
	colSubtle = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#7A7A7A"}
	colSelDim = lipgloss.AdaptiveColor{Light: "#E4E0F5", Dark: "#2E2A40"}

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleFaint    = lipgloss.NewStyle().Foreground(colSubtle)
	styleAccent   = lipgloss.NewStyle().Foreground(colAccent)
	styleLabel    = lipgloss.NewStyle().Foreground(colSubtle).Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleSelDim   = lipgloss.NewStyle().Background(colSelDim)
	styleInfo     = lipgloss.NewStyle()
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDanger   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("1"))
)

type level int

const (
	levelInfo level = iota
	levelOK
	levelWarn
	levelError
)

// status is the one-line message shown in the footer, shared by all screens.
type status struct {
	text  string
	level level
}

func (s *status) set(l level, format string, args ...any) {
	s.level = l
	s.text = fmt.Sprintf(format, args...)
}

func (s *status) clear() { s.text = "" }

func (s *status) render(width int) string {
	st := styleInfo
	switch s.level {
	case levelOK:
		st = styleOK
	case levelWarn:
		st = styleWarn
	case levelError:
		st = styleError
	}
	return st.Render(truncate(" "+s.text, width))
}

// footer shows the status if set, otherwise the key hints.
func footer(width int, st *status, hints string) string {
	if st.text != "" {
		return st.render(width)
	}
	return styleFaint.Render(truncate(" "+hints, width))
}

// header renders the top bar: title on the left, info on the right.
func header(width int, left, right string) string {
	title := styleTitle.Render(" sherlook ")
	l := title + " " + left
	gap := width - ansi.StringWidth(l) - ansi.StringWidth(right) - 1
	if gap < 1 {
		return truncate(l, width)
	}
	return l + strings.Repeat(" ", gap) + styleFaint.Render(right) + " "
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func padRight(s string, width int) string {
	if w := ansi.StringWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// fill pads or cuts content to exactly height lines of the given width.
func fill(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padRight(truncate(l, width), width)
	}
	return strings.Join(lines, "\n")
}
