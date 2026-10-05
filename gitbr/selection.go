package main

import (
	"io"
	"strings"
	"unicode"

	"github.com/chzyer/readline"
	"github.com/manifoldco/promptui/screenbuf"
)

// 搜索结果保留原始索引，确认时始终返回实际分支对应的项。
func selectCircular(label string, items []string, size int, help string, input io.ReadCloser, output io.WriteCloser, color bool) (int, error) {
	if len(items) == 0 {
		return -1, nil
	}
	matches := make([]int, len(items))
	for i := range items {
		matches[i] = i
	}
	cursor, start := 0, 0
	searching := false
	var query []rune
	filter := func() {
		matches = matches[:0]
		for i, item := range items {
			if strings.Contains(strings.ToLower(item), strings.ToLower(string(query))) {
				matches = append(matches, i)
			}
		}
		cursor, start = 0, 0
	}
	config := &readline.Config{Stdin: readline.NewCancelableStdin(input), Stdout: output, HistoryLimit: -1, UniqueEditLine: true}
	rl, err := readline.NewEx(config)
	if err != nil {
		return -1, err
	}
	defer rl.Close()
	rl.Write([]byte("\x1b[?25l"))
	screen := screenbuf.New(rl)
	defer func() { screen.Reset(); screen.WriteString(""); screen.Flush(); rl.Write([]byte("\x1b[?25h")) }()
	config.SetListener(func(line []rune, pos int, key rune) ([]rune, int, bool) {
		count := len(matches)
		switch {
		case key == readline.CharEnter:
			return nil, 0, true
		case key == readline.CharTab:
			searching = !searching
		case key == '/' && !searching:
			searching = true
		case key == readline.CharNext || (key == 'j' && !searching):
			if count > 0 {
				cursor = (cursor + 1) % count
			}
		case key == readline.CharPrev || (key == 'k' && !searching):
			if count > 0 {
				cursor = (cursor + count - 1) % count
			}
		case searching && (key == readline.CharBackspace || key == readline.CharCtrlH):
			if len(query) > 0 {
				query = query[:len(query)-1]
				filter()
			}
		case searching && key == readline.CharCtrlU:
			query = nil
			filter()
		case searching && unicode.IsPrint(key):
			query = append(query, key)
			filter()
		case !searching && (key == 'h' || key == readline.CharBackward):
			cursor -= size
			if cursor < 0 {
				cursor = 0
			}
		case !searching && (key == 'l' || key == readline.CharForward):
			cursor += size
			if cursor >= count {
				cursor = count - 1
			}
			if cursor < 0 {
				cursor = 0
			}
		}
		if cursor < start {
			start = cursor
		}
		if cursor >= start+size {
			start = cursor - size + 1
		}
		screen.WriteString(label)
		mode := "选择"
		if searching {
			mode = "搜索输入（Tab 返回选择，Ctrl+U 清空）"
		}
		screen.WriteString(mode + " | 关键词：" + string(query))
		for row := 0; row < size; row++ {
			i := start + row
			text := ""
			if i < len(matches) {
				marker := " "
				if row == 0 && start > 0 {
					marker = "↑"
				}
				if row == size-1 && i < len(matches)-1 {
					marker = "↓"
				}
				text = items[matches[i]]
				if i == cursor {
					if color {
						text = "\x1b[36m" + text + "\x1b[0m"
					}
					text = marker + " > " + text
				} else {
					text = marker + "   " + text
				}
			} else if row == 0 {
				text = "无匹配分支，请修改关键词"
			}
			screen.WriteString(text)
		}
		screen.WriteString(help)
		screen.Flush()
		return nil, 0, true
	})
	for {
		_, err = rl.Readline()
		if err == readline.ErrInterrupt || err == io.EOF {
			return -1, nil
		}
		if err != nil {
			return -1, err
		}
		if len(matches) > 0 {
			return matches[cursor], nil
		}
	}
}
