package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Интерактив держится на raw-режиме терминала, который включается через stty.
// Так CLI обходится без зависимостей: библиотеке они не нужны, а тянуть их
// ради одного меню незачем.

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

// interactive сообщает, есть ли живой терминал: без него меню не показываем.
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
	hint := "д/Н"
	if def {
		hint = "Д/н"
	}
	answer := strings.ToLower(ask(fmt.Sprintf("%s [%s] ", question, hint), ""))
	if answer == "" {
		return def
	}
	switch answer {
	case "д", "да", "y", "yes", "da":
		return true
	default:
		return false
	}
}

// Item — пункт меню.
type Item struct {
	Label string
	Hint  string
}

// stty дёргает системную утилиту: это единственный способ переключить терминал
// в raw-режим, не втягивая внешних зависимостей.
func stty(args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// selectItem показывает меню и возвращает номер выбранного пункта.
// Без терминала спрашивает номер обычным вводом.
func selectItem(title string, items []Item, def int) (int, error) {
	switch {
	case len(items) == 0:
		return 0, fmt.Errorf("пустой список выбора")
	case len(items) == 1:
		return 0, nil
	case !interactive():
		return selectNumeric(title, items, def)
	}

	if err := stty("-echo", "cbreak"); err != nil {
		// Терминал не поддался — не беда, спросим номер.
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
			return 0, fmt.Errorf("выбор прерван")
		}
		switch buf[0] {
		case '\r', '\n', ' ':
			return index, nil
		case 3: // Ctrl+C
			return 0, fmt.Errorf("прервано пользователем")
		case 'q', 'Q':
			return 0, fmt.Errorf("выбор отменён")
		case 'k':
			move(-1)
		case 'j':
			move(1)
		case 0x1b: // управляющая последовательность
			seq := make([]byte, 2)
			if _, err := os.Stdin.Read(seq); err != nil {
				return 0, fmt.Errorf("выбор прерван")
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
			// Номер пункта можно набрать цифрами, в том числе двузначный.
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
		raw := ask(fmt.Sprintf("Номер [%d]: ", def+1), strconv.Itoa(def+1))
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		fmt.Println("Нужен номер из списка.")
	}
	return 0, fmt.Errorf("не выбран пункт списка")
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
		lines = append(lines, fmt.Sprintf("%s   ↑ ещё %d%s", dim, offset, reset))
	}
	for i := offset; i < min(len(items), offset+visible); i++ {
		lines = append(lines, itemLine(items[i], i, i == index, width))
	}
	if rest := len(items) - (offset + visible); rest > 0 {
		lines = append(lines, fmt.Sprintf("%s   ↓ ещё %d%s", dim, rest, reset))
	}
	return append(lines, dim+"   ↑/↓ — выбор, Enter — подтвердить, q — выход"+reset)
}

// itemLine сначала обрезает строку по ширине терминала и только потом красит:
// иначе счёт символов уехал бы на невидимых управляющих последовательностях.
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

// termSize спрашивает у stty размер окна; при неудаче отдаёт значение по умолчанию.
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
