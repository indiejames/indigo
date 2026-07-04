// Jumpy is an EasyMotion-style jump plugin for indigo.
//
// Press "f" in normal mode to activate. Every jump target on the visible screen
// gets a 2-character label drawn over it. Type the two label characters to jump
// there instantly. Press Esc at any time to cancel.
//
// Jump targets are any non-whitespace character that immediately follows
// whitespace or appears at the start of a line — essentially the first
// character of every visible token.
package main

import (
	"strings"
	"sync"
	"unicode"

	"github.com/indiejames/indigo/sdk"
)

// labelChars defines the character set used for 2-char labels.
// Home-row characters first for ergonomics.
const labelChars = "asdfjkl;ghqwertyuiopzxcvbnm"

type target struct {
	label string
	line  uint32
	col   uint32
}

// Jumpy holds all state for one running plugin instance.
type Jumpy struct {
	mu  sync.Mutex
	api *sdk.Api

	// jump session state (protected by mu)
	active   bool
	bufID    uint32
	clientID uint64
	targets  []target // nil = not yet computed for this session
	char1    byte     // 0 = waiting for first label char
}

func (j *Jumpy) Init(api *sdk.Api) sdk.Info {
	j.api = api
	api.OnKey("f", j.onKey)        //nolint:errcheck
	api.Decorations(j.decorations) //nolint:errcheck
	api.ShowMessage("jumpy ready: press f to jump") //nolint:errcheck
	return sdk.Info{Name: "jumpy", Version: "0.1.0"}
}

// onKey is the single handler registered for "f". It dispatches on ctx.Mode:
//   - "normal"  → trigger: start a new jump session
//   - "capture" → label input: first or second label character (or Esc to cancel)
func (j *Jumpy) onKey(key string, ctx sdk.KeyContext) sdk.KeyResponse {
	switch ctx.Mode {
	case "normal":
		return j.trigger(ctx)
	case "capture":
		return j.captured(key)
	default:
		return sdk.KeyResponse{}
	}
}

func (j *Jumpy) trigger(ctx sdk.KeyContext) sdk.KeyResponse {
	startLine, endLine, err := j.api.VisibleRange(ctx.ClientID)

	var targets []target
	if err == nil {
		lines, rerr := j.api.ReadLines(ctx.BufID, startLine, endLine+1)
		if rerr == nil {
			targets = computeTargets(lines, startLine)
		}
	}

	j.mu.Lock()
	j.active = true
	j.bufID = ctx.BufID
	j.clientID = ctx.ClientID
	j.targets = targets
	j.char1 = 0
	j.mu.Unlock()

	j.api.ShowMessage("jumpy: activated — type label chars") //nolint:errcheck
	return sdk.KeyResponse{Handled: true, CaptureKeys: 2}
}

func (j *Jumpy) captured(key string) sdk.KeyResponse {
	j.mu.Lock()
	defer j.mu.Unlock()

	if !j.active {
		return sdk.KeyResponse{Handled: true}
	}

	// Esc or any multi-rune key (e.g. "ctrl+c") cancels the session.
	if key == "esc" || len([]rune(key)) != 1 {
		j.reset()
		return sdk.KeyResponse{Handled: true, CaptureKeys: 0}
	}

	c := []rune(key)[0]

	if j.char1 == 0 {
		// Targets may be nil if the decoration poll hasn't run yet after the trigger.
		// In that case, keep capture mode alive and wait for the next poll to populate them.
		if j.targets == nil {
			return sdk.KeyResponse{Handled: true, CaptureKeys: 2}
		}
		// First label character: filter targets to those whose label starts with c.
		if !strings.ContainsRune(labelChars, c) {
			j.reset()
			return sdk.KeyResponse{Handled: true, CaptureKeys: 0}
		}
		cb := byte(c)
		filtered := j.targets[:0]
		for _, t := range j.targets {
			if len(t.label) > 0 && t.label[0] == cb {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			j.reset()
			return sdk.KeyResponse{Handled: true, CaptureKeys: 0}
		}
		j.targets = filtered
		j.char1 = cb
		return sdk.KeyResponse{Handled: true, CaptureKeys: 1}
	}

	// Second label character: resolve the target and jump.
	label := string([]byte{j.char1, byte(c)})
	for _, t := range j.targets {
		if t.label == label {
			line, col := t.line, t.col
			j.reset()
			return sdk.KeyResponse{
				Handled:     true,
				HasCursor:   true,
				CursorLine:  line,
				CursorCol:   col,
				CaptureKeys: 0,
			}
		}
	}

	// No matching label — cancel.
	j.reset()
	return sdk.KeyResponse{Handled: true, CaptureKeys: 0}
}

// decorations is called by the editor on every decoration poll.
// Targets are pre-computed in trigger(); this function just formats them.
// Falls back to computing lazily (via ReadLines) if trigger() didn't finish in time.
func (j *Jumpy) decorations(bufID uint32, clientID uint64, r sdk.Range) []sdk.Decoration {
	j.mu.Lock()
	defer j.mu.Unlock()

	if !j.active || j.bufID != bufID || j.clientID != clientID {
		return nil
	}

	// Lazy fallback: compute targets if trigger() didn't populate them.
	if j.targets == nil {
		lines, err := j.api.ReadLines(bufID, r.Start.Line, r.End.Line+1)
		if err != nil || len(lines) == 0 {
			return nil
		}
		j.targets = computeTargets(lines, r.Start.Line)
		if len(j.targets) == 0 {
			j.reset()
			return nil
		}
	}

	// Build decoration list, filtering and trimming labels for display.
	out := make([]sdk.Decoration, 0, len(j.targets))
	for _, t := range j.targets {
		label := t.label
		if j.char1 != 0 {
			// After first char is typed, show only the second character.
			label = label[1:]
		}
		out = append(out, sdk.Decoration{
			Line: t.line,
			Col:  t.col,
			Text: label,
			Kind: sdk.DecorationOverlay,
		})
	}
	return out
}

func (j *Jumpy) reset() {
	j.active = false
	j.targets = nil
	j.char1 = 0
}

// computeTargets finds jump target positions in lines, assigning 2-char labels.
// A target is any non-whitespace character that immediately follows whitespace
// or appears at the start of the line — essentially the first char of every token.
func computeTargets(lines []string, startLine uint32) []target {
	chars := []rune(labelChars)
	n := len(chars)
	maxTargets := n * n

	var targets []target
	for i, line := range lines {
		lineNum := startLine + uint32(i)
		runes := []rune(line)
		for c, r := range runes {
			if unicode.IsSpace(r) {
				continue
			}
			// Only target word-starting characters (letters, digits, underscore).
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				continue
			}
			// Jump start: col 0 or preceded by whitespace or punctuation.
			if c > 0 && (unicode.IsLetter(runes[c-1]) || unicode.IsDigit(runes[c-1]) || runes[c-1] == '_') {
				continue
			}
			idx := len(targets)
			if idx >= maxTargets {
				return targets
			}
			label := string([]rune{chars[idx/n], chars[idx%n]})
			targets = append(targets, target{
				label: label,
				line:  lineNum,
				col:   uint32(c),
			})
		}
	}
	return targets
}

func main() {
	if err := sdk.Run(&Jumpy{}); err != nil {
		panic(err)
	}
}
