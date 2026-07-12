package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/client"
)

// symbolResultsMsg delivers workspace symbol query results to the App.
type symbolResultsMsg struct {
	syms  []client.ClientSymbol
	query string
}

type symbolPickerState struct {
	bufID   uint32
	query   string
	results []client.ClientSymbol
	cursor  int
	loading bool
	width   int
	height  int
}

type docSymbolPickerState struct {
	allSyms  []client.ClientSymbol
	filter   string
	filtered []client.ClientSymbol
	cursor   int
	width    int
	height   int
}

func newSymbolPicker(bufID uint32, w, h int) *symbolPickerState {
	return &symbolPickerState{bufID: bufID, width: w, height: h}
}

func newDocSymbolPicker(syms []client.ClientSymbol, w, h int) *docSymbolPickerState {
	p := &docSymbolPickerState{allSyms: syms, filtered: syms, width: w, height: h}
	return p
}

func (p *docSymbolPickerState) applyFilter(filter string) {
	p.filter = filter
	if filter == "" {
		p.filtered = p.allSyms
		p.cursor = 0
		return
	}
	low := strings.ToLower(filter)
	var out []client.ClientSymbol
	for _, s := range p.allSyms {
		if strings.Contains(strings.ToLower(s.Name), low) {
			out = append(out, s)
		}
	}
	p.filtered = out
	p.cursor = 0
}

// -- Symbol picker key handler --

func (a App) handleSymbolPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.symbolPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.symbolPicker = nil
		return a, nil
	case "enter":
		if p != nil && len(p.results) > 0 {
			sym := p.results[p.cursor]
			a.symbolPicker = nil
			return a, a.doOpenFileAtPos(sym.Path, sym.Line, sym.Col)
		}
	case "up", "ctrl+p":
		if p != nil && p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p != nil && p.cursor < len(p.results)-1 {
			p.cursor++
		}
	case "backspace":
		if p != nil && len(p.query) > 0 {
			runes := []rune(p.query)
			p.query = string(runes[:len(runes)-1])
			p.loading = true
			return a, a.fetchWorkspaceSymbols()
		}
	default:
		if len(msg.Runes) > 0 && p != nil {
			p.query += string(msg.Runes)
			p.loading = true
			return a, a.fetchWorkspaceSymbols()
		}
	}
	return a, nil
}

func (a App) fetchWorkspaceSymbols() tea.Cmd {
	if a.symbolPicker == nil {
		return nil
	}
	bufID := a.symbolPicker.bufID
	query := a.symbolPicker.query
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		syms, err := rpc.WorkspaceSymbols(ctx, bufID, query)
		if err != nil {
			return symbolResultsMsg{query: query}
		}
		return symbolResultsMsg{syms: syms, query: query}
	}
}

// -- Doc symbol picker key handler --

func (a App) handleDocSymbolPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.docSymbolPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.docSymbolPicker = nil
		return a, nil
	case "enter":
		if p != nil && len(p.filtered) > 0 {
			sym := p.filtered[p.cursor]
			a.docSymbolPicker = nil
			// Same buffer — just move cursor.
			if len(a.buffers) > 0 {
				a.buffers[a.active] = a.buffers[a.active].AtPos(sym.Line, sym.Col, a.bufHeight())
			}
			return a, nil
		}
	case "up", "ctrl+p":
		if p != nil && p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p != nil && p.cursor < len(p.filtered)-1 {
			p.cursor++
		}
	case "backspace":
		if p != nil && len(p.filter) > 0 {
			runes := []rune(p.filter)
			p.applyFilter(string(runes[:len(runes)-1]))
		}
	default:
		if len(msg.Runes) > 0 && p != nil {
			p.applyFilter(p.filter + string(msg.Runes))
		}
	}
	return a, nil
}

// -- Render functions --

const symbolPickerMaxVisible = 16

var (
	symPickerBg = lipgloss.Color("#1E2A38")

	symPickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#44AACC")).
				Background(symPickerBg)

	symPickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#88CCEE")).
				Bold(true).
				Padding(0, 1)

	symPickerInputStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	symPickerItemStyle = lipgloss.NewStyle().
				Background(symPickerBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	symPickerSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2D5F8A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	symPickerKindStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6699AA"))

	symPickerContainerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#556677"))
)

func (p *symbolPickerState) render() string {
	innerW := 50
	if p.width > 0 {
		innerW = min(p.width*2/3, 80)
		innerW = max(innerW, 40)
	}

	var rows []string
	rows = append(rows, symPickerTitleStyle.Width(innerW).Render("Go to Symbol in Project"))
	rows = append(rows, symPickerInputStyle.Width(innerW).Render(" "+p.query+"█"))
	rows = append(rows, lipgloss.NewStyle().
		Background(symPickerBg).
		Foreground(lipgloss.Color("#44AACC")).
		Render(strings.Repeat("─", innerW)))

	if p.loading {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  searching…"))
	} else if len(p.results) == 0 && p.query == "" {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  type to search"))
	} else if len(p.results) == 0 {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  no results"))
	} else {
		vis := min(len(p.results), symbolPickerMaxVisible)
		start := max(0, min(p.cursor-vis/2, len(p.results)-vis))
		end := min(start+vis, len(p.results))

		if start > 0 {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render("  ↑ more"))
		}
		for i := start; i < end; i++ {
			sym := p.results[i]
			// [kind] name  container  file:line
			kind := symPickerKindStyle.Render(fmt.Sprintf("[%s]", sym.KindLabel))
			loc := fmt.Sprintf("  %s:%d", filepath.Base(sym.Path), sym.Line+1)
			container := ""
			if sym.ContainerName != "" {
				container = symPickerContainerStyle.Render(" (" + sym.ContainerName + ")")
			}
			label := kind + " " + sym.Name + container + symPickerContainerStyle.Render(loc)
			if i == p.cursor {
				rows = append(rows, symPickerSelStyle.Width(innerW).Render(label))
			} else {
				rows = append(rows, symPickerItemStyle.Width(innerW).Render(label))
			}
		}
		if end < len(p.results) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render("  ↓ more"))
		}
	}

	return symPickerBorderStyle.Render(strings.Join(rows, "\n"))
}

func (p *docSymbolPickerState) render() string {
	innerW := 50
	if p.width > 0 {
		innerW = min(p.width*2/3, 70)
		innerW = max(innerW, 40)
	}

	var rows []string
	rows = append(rows, symPickerTitleStyle.Width(innerW).Render("Go to Symbol in File"))
	rows = append(rows, symPickerInputStyle.Width(innerW).Render(" "+p.filter+"█"))
	rows = append(rows, lipgloss.NewStyle().
		Background(symPickerBg).
		Foreground(lipgloss.Color("#44AACC")).
		Render(strings.Repeat("─", innerW)))

	if len(p.filtered) == 0 {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  no matching symbols"))
	} else {
		vis := min(len(p.filtered), symbolPickerMaxVisible)
		start := max(0, min(p.cursor-vis/2, len(p.filtered)-vis))
		end := min(start+vis, len(p.filtered))

		if start > 0 {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render("  ↑ more"))
		}
		for i := start; i < end; i++ {
			sym := p.filtered[i]
			kind := symPickerKindStyle.Render(fmt.Sprintf("[%s]", sym.KindLabel))
			container := ""
			if sym.ContainerName != "" {
				container = symPickerContainerStyle.Render(" (" + sym.ContainerName + ")")
			}
			label := kind + " " + sym.Name + container
			if i == p.cursor {
				rows = append(rows, symPickerSelStyle.Width(innerW).Render(label))
			} else {
				rows = append(rows, symPickerItemStyle.Width(innerW).Render(label))
			}
		}
		if end < len(p.filtered) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render("  ↓ more"))
		}
	}

	return symPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
