package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/chzyer/readline"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "gitbr: %+v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println("用法：gitbr [--remote]\n默认显示本地分支；--remote 获取所有远端的分支，显示最近 30 天有提交的分支。\n↑/↓ 或 j/k 循环选择，Ctrl+N 下一项，空格或回车切换。\nTab 切换选择/搜索，搜索时输入关键词、退格删除，Esc / Ctrl+C 取消。")
		return nil
	}
	remoteMode := len(args) == 1 && args[0] == "--remote"
	if len(args) != 0 && !remoteMode {
		return errors.New("不支持额外参数，请运行 gitbr --help 查看用法")
	}
	inside, err := gitOutput("rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if strings.TrimSpace(inside) != "true" {
		return errors.New("请在 Git 工作目录或其子目录中运行")
	}
	if remoteMode {
		return runRemote()
	}
	output, err := gitOutput("for-each-ref", "--sort=-refname", "--format=%(HEAD)%09%(refname:lstrip=2)", "refs/heads/")
	if err != nil {
		return err
	}
	var branches, labels []string
	current := ""
	for _, line := range strings.Split(strings.TrimRight(output, "\r\n"), "\n") {
		fields := strings.SplitN(strings.TrimSuffix(line, "\r"), "\t", 2)
		if len(fields) != 2 {
			continue
		}
		name := fields[1]
		label := name
		if fields[0] == "*" {
			current = name
			continue
		}
		branches = append(branches, name)
		labels = append(labels, label)
	}
	if current != "" {
		branches = append([]string{current}, branches...)
		labels = append([]string{current + "（当前分支）"}, labels...)
	}
	if len(branches) == 0 {
		fmt.Println("当前仓库没有可切换的本地分支，请先创建分支并提交。")
		return nil
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errors.New("交互选择需要终端，请在 PowerShell、CMD 或 Git Bash 中直接运行 gitbr")
	}
	label := "本地分支"
	if current == "" {
		label += "（当前 HEAD 未指向已有本地分支）"
	}
	_, disabled := os.LookupEnv("NO_COLOR")
	index, err := selectCircular(label, labels, 15,
		"↑/k 上一项，↓/j/Ctrl+N 下一项；Tab 选择/搜索；空格/回车确认，Esc 取消",
		selectionKeyReader{readline.Stdin}, promptWriter{colorable.NewColorable(os.Stdout)},
		!disabled && os.Getenv("TERM") != "dumb")
	if err != nil {
		return err
	}
	if index < 0 {
		return nil
	}
	name := branches[index]
	// 由 Git 检查未提交修改和其他工作区占用，不强制切换或自动暂存。
	if _, err := gitOutput("switch", "--no-guess", "--", name); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "已切换到分支：%s\n", name)
	return nil
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s 失败：%+v\n%s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func isTerminal(file *os.File) bool {
	return isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd())
}

type promptWriter struct{ io.Writer }

func (promptWriter) Close() error { return nil }

type selectionKeyReader struct{ io.ReadCloser }

func (r selectionKeyReader) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	if n == 1 && buffer[0] == ' ' {
		buffer[0] = '\r'
	}
	// 沿用 gitmod 的 Windows 按键处理，避免把方向键序列当作 Esc。
	if runtime.GOOS == "windows" && n == 1 && buffer[0] == 27 {
		buffer[0] = 3
	}
	return n, err
}
