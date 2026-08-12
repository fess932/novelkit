package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The menus rely on raw terminal mode, switched on through stty. That keeps the
// CLI dependency-free: the library needs none, and pulling one in for a single
// menu would be a poor trade.

const (
	esc     = "\x1b["
	dim     = esc + "2m"
	cyan    = esc + "36m"
	bold    = esc + "1m"
	reset   = esc + "0m"
	hideCur = esc + "?25l"
	showCur = esc + "?25h"
)

var stdin = bufio.NewReader(os.Stdin)

// interactive reports whether there is a live terminal; without one, no menus.
func interactive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fo, err := os.Stdout.Stat()
	return err == nil && fo.Mode()&os.ModeCharDevice != 0
}

func ask(question, fallback string) string {
	fmt.Print(question)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return fallback
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer
	}
	return fallback
}

func confirm(question string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	answer := strings.ToLower(ask(fmt.Sprintf("%s [%s] ", question, hint), ""))
	if answer == "" {
		return def
	}
	switch answer {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// Item is one menu entry.
type Item struct {
	Label string
	Hint  string
}

// stty shells out to the system tool: it is the only way to put the terminal
// into raw mode without pulling in a dependency.
func stty(args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// selectItem shows a menu and returns the index of the chosen entry.
// Without a terminal it falls back to asking for a number.
func selectItem(title string, items []Item, def int) (int, error) {
	switch {
	case len(items) == 0:
		return 0, fmt.Errorf("empty menu")
	case len(items) == 1:
		return 0, nil
	case !interactive():
		return selectNumeric(title, items, def)
	}

	if err := stty("-echo", "cbreak"); err != nil {
		// The terminal would not cooperate; asking for a number will do.
		return selectNumeric(title, items, def)
	}
	defer func() {
		_ = stty("echo", "-cbreak")
		fmt.Print(showCur)
	}()

	index := min(max(def, 0), len(items)-1)
	visible := max(3, min(len(items), termRows()-6))
	offset := max(0, min(index-visible/2, len(items)-visible))
	printed := 0

	draw := func() {
		if printed > 0 {
			fmt.Printf("%s%dA", esc, printed)
		}
		fmt.Print(esc + "0J")
		lines := frame(title, items, index, offset, visible)
		fmt.Print(strings.Join(lines, "\n") + "\n")
		printed = len(lines)
	}
	move := func(delta int) {
		index = (index + delta + len(items)) % len(items)
		if index < offset {
			offset = index
		}
		if index >= offset+visible {
			offset = index - visible + 1
		}
		offset = max(0, min(offset, max(0, len(items)-visible)))
		draw()
	}

	fmt.Print(hideCur)
	draw()

	var typed string
	buf := make([]byte, 1)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			return 0, fmt.Errorf("selection interrupted")
		}
		switch buf[0] {
		case '\r', '\n', ' ':
			return index, nil
		case 3: // Ctrl+C
			return 0, fmt.Errorf("interrupted by the user")
		case 'q', 'Q':
			return 0, fmt.Errorf("selection cancelled")
		case 'k':
			move(-1)
		case 'j':
			move(1)
		case 0x1b: // escape sequence
			seq := make([]byte, 2)
			if _, err := os.Stdin.Read(seq); err != nil {
				return 0, fmt.Errorf("selection interrupted")
			}
			if seq[0] != '[' {
				continue
			}
			switch seq[1] {
			case 'A':
				move(-1)
			case 'B':
				move(1)
			case 'H':
				move(-index)
			case 'F':
				move(len(items) - 1 - index)
			case '5': // PageUp
				os.Stdin.Read(buf)
				move(-visible)
			case '6': // PageDown
				os.Stdin.Read(buf)
				move(visible)
			}
		default:
			// An entry can also be picked by typing its number, two digits included.
			if buf[0] >= '0' && buf[0] <= '9' {
				typed += string(buf[0])
				if n, err := strconv.Atoi(typed); err == nil && n >= 1 && n <= len(items) {
					move(n - 1 - index)
					if n*10 > len(items) {
						typed = ""
					}
				} else {
					typed = ""
				}
			}
		}
	}
}

func selectNumeric(title string, items []Item, def int) (int, error) {
	fmt.Println("\n" + title)
	for i, it := range items {
		mark := " "
		if i == def {
			mark = "›"
		}
		fmt.Printf("  %s %2d. %s%s\n", mark, i+1, it.Label, hintOf(it))
	}
	for range 5 {
		raw := ask(fmt.Sprintf("Number [%d]: ", def+1), strconv.Itoa(def+1))
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		fmt.Println("Enter a number from the list.")
	}
	return 0, fmt.Errorf("no entry chosen")
}

func hintOf(it Item) string {
	if it.Hint == "" {
		return ""
	}
	return "  " + it.Hint
}

func frame(title string, items []Item, index, offset, visible int) []string {
	width := termCols() - 1
	lines := []string{"\n" + title}
	if offset > 0 {
		lines = append(lines, fmt.Sprintf("%s   ↑ %d more%s", dim, offset, reset))
	}
	for i := offset; i < min(len(items), offset+visible); i++ {
		lines = append(lines, itemLine(items[i], i, i == index, width))
	}
	if rest := len(items) - (offset + visible); rest > 0 {
		lines = append(lines, fmt.Sprintf("%s   ↓ %d more%s", dim, rest, reset))
	}
	return append(lines, dim+"   ↑/↓ to move, Enter to confirm, q to quit"+reset)
}

// itemLine truncates to the terminal width first and colours afterwards:
// counting characters through invisible escape sequences would go wrong.
func itemLine(it Item, i int, active bool, width int) string {
	prefix := fmt.Sprintf("    %2d. ", i+1)
	if active {
		prefix = fmt.Sprintf(" ❯  %2d. ", i+1)
	}
	runes := []rune(prefix + it.Label + hintOf(it))
	if width > 1 && len(runes) > width {
		runes = append(runes[:width-1], '…')
	}

	head := string(runes[:min(len(runes), len([]rune(prefix))+len([]rune(it.Label)))])
	tail := string(runes[min(len(runes), len([]rune(prefix))+len([]rune(it.Label))):])
	if active {
		head = cyan + bold + head + reset
	}
	if tail != "" {
		tail = dim + tail + reset
	}
	return head + tail
}

func termCols() int { return termSize(1, 80) }
func termRows() int { return termSize(0, 24) }

// termSize asks stty for the window size and falls back to a default.
func termSize(field, def int) int {
	out, err := exec.Command("stty", "size").Output()
	if err != nil {
		return def
	}
	parts := strings.Fields(string(out))
	if len(parts) != 2 {
		return def
	}
	n, err := strconv.Atoi(parts[field])
	if err != nil || n <= 0 {
		return def
	}
	return n
}
