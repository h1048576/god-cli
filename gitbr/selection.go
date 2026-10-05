package main

import (
	"io"

	"github.com/chzyer/readline"
	"github.com/manifoldco/promptui/list"
	"github.com/manifoldco/promptui/screenbuf"
)

// 保留 promptui 的列表和屏幕绘制方式，在按键监听层实现首尾循环。
// 两个 CLI 独立构建，各自保留相同的选择器实现。
func selectCircular(label string, items []string, size int, help string, input io.ReadCloser, output io.WriteCloser, color bool) (int, error) {
	if len(items) == 0 {
		return -1, nil
	}
	choices, err := list.New(items, size)
	if err != nil {
		return -1, err
	}
	config := &readline.Config{Stdin: readline.NewCancelableStdin(input), Stdout: output, HistoryLimit: -1, UniqueEditLine: true}
	rl, err := readline.NewEx(config)
	if err != nil {
		return -1, err
	}
	defer rl.Close()
	rl.Write([]byte("\x1b[?25l"))
	screen := screenbuf.New(rl)
	defer func() {
		screen.Reset()
		screen.WriteString("")
		screen.Flush()
		rl.Write([]byte("\x1b[?25h"))
	}()
	config.SetListener(func(line []rune, pos int, key rune) ([]rune, int, bool) {
		switch key {
		case readline.CharEnter:
			return nil, 0, true
		case 'j', readline.CharNext: // CharNext（0x0e）同时对应 Ctrl+N 和向下箭头。
			if choices.Index() == len(items)-1 {
				choices.SetCursor(0)
			} else {
				choices.Next()
			}
		case 'k', readline.CharPrev:
			if choices.Index() == 0 {
				choices.SetCursor(len(items) - 1)
			} else {
				choices.Prev()
			}
		case 'h', readline.CharBackward:
			choices.PageUp()
		case 'l', readline.CharForward:
			choices.PageDown()
		}
		screen.WriteString(label)
		visible, cursor := choices.Items()
		for i, item := range visible {
			marker := " "
			if i == 0 && choices.CanPageUp() {
				marker = "↑"
			}
			if i == len(visible)-1 && choices.CanPageDown() {
				marker = "↓"
			}
			text := item.(string)
			if i == cursor {
				if color {
					text = "\x1b[36m" + text + "\x1b[0m"
				}
				screen.WriteString(marker + " > " + text)
			} else {
				screen.WriteString(marker + "   " + text)
			}
		}
		screen.WriteString(help)
		screen.Flush()
		return nil, 0, true
	})
	_, err = rl.Readline()
	if err == readline.ErrInterrupt || err == io.EOF {
		return -1, nil
	}
	if err != nil {
		return -1, err
	}
	return choices.Index(), nil
}
