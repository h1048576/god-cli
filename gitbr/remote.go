package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/mattn/go-colorable"
	"wesure.cn/msf/errors"
)

type remoteBranch struct {
	remote, name, ref, date string
	timestamp               int64
}

func runRemote() error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errors.New("交互选择需要终端，请直接运行 gitbr --remote")
	}
	output, err := gitOutput("remote")
	if err != nil {
		return err
	}
	remotes := strings.Fields(output)
	if len(remotes) == 0 {
		return errors.New("当前仓库没有配置远端")
	}
	cutoff := time.Now().AddDate(0, 0, -30).Unix()
	var branches []remoteBranch
	for _, remote := range remotes {
		fmt.Printf("获取远端 %s 的最新分支...\n", remote)
		// 显式获取全部 heads，兼容仅配置了单分支 fetch 的仓库；不获取标签。
		if _, err := gitOutput("fetch", "--prune", "--no-tags", "--no-recurse-submodules", "--", remote,
			"+refs/heads/*:refs/remotes/"+remote+"/*"); err != nil {
			return err
		}
		prefix := "refs/remotes/" + remote + "/"
		refs, err := gitOutput("for-each-ref", "--format=%(refname)%09%(symref)%09%(committerdate:unix)%09%(committerdate:short)", prefix)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
			fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
			if len(fields) != 4 || fields[1] != "" {
				continue
			}
			stamp, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return errors.New(fmt.Sprintf("无法读取分支 %s 的提交时间：%+v", fields[0], err))
			}
			if stamp < cutoff {
				continue
			}
			branches = append(branches, remoteBranch{remote: remote, name: strings.TrimPrefix(fields[0], prefix), ref: fields[0], date: fields[3], timestamp: stamp})
		}
	}
	sort.Slice(branches, func(i, j int) bool {
		if branches[i].timestamp != branches[j].timestamp {
			return branches[i].timestamp > branches[j].timestamp
		}
		return branches[i].ref > branches[j].ref
	})
	if len(branches) == 0 {
		fmt.Println("没有最近 30 天有提交的远程分支。")
		return nil
	}
	labels := make([]string, len(branches))
	for i, branch := range branches {
		labels[i] = branch.remote + "/" + branch.name + "  " + branch.date
	}
	_, disabled := os.LookupEnv("NO_COLOR")
	index, err := selectCircular("选择远程分支（最近 30 天提交，最新优先）", labels, 15,
		"↑/k 上一项，↓/j/Ctrl+N 下一项；/ 搜索，Tab 选择；空格/回车确认，Esc 取消",
		selectionKeyReader{readline.Stdin}, promptWriter{colorable.NewColorable(os.Stdout)}, !disabled && os.Getenv("TERM") != "dumb")
	if err != nil {
		return err
	}
	if index < 0 {
		fmt.Println("已取消切换。")
		return nil
	}
	branch := branches[index]
	localRef := "refs/heads/" + branch.name
	existing, err := gitOutput("for-each-ref", "--format=%(refname)%09%(upstream)", localRef)
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(strings.TrimRight(existing, "\r\n"), "\n") {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 2 || fields[0] != localRef {
			continue
		}
		found = true
		if fields[1] != branch.ref {
			return errors.New(fmt.Sprintf("本地分支 %s 已存在，但未关联 %s/%s；请用 gitbr 切换本地分支或先调整上游关联", branch.name, branch.remote, branch.name))
		}
	}
	if found {
		_, err = gitOutput("switch", "--no-guess", "--", branch.name)
	} else {
		_, err = gitOutput("switch", "--track", "-c", branch.name, "--", branch.ref)
	}
	if err != nil {
		return err
	}
	fmt.Printf("已切换到本地分支：%s（跟踪 %s/%s）\n", branch.name, branch.remote, branch.name)
	return nil
}
