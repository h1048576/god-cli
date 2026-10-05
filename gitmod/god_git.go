package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/net/context"
	"wesure.cn/msf/errors"
)

type godGit struct {
	dir string
	ctx context.Context
}

type godWorktree struct {
	path   string
	branch string
}

func (g godGit) execute(args ...string) (string, string, int, error) {
	if g.dir != "" {
		args = append([]string{"-C", g.dir}, args...)
	}
	cmd := exec.CommandContext(g.ctx, "git", args...)
	cmd.Stdin = os.Stdin
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_NAMESPACE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "LC_ALL", "LANG":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func (g godGit) command(args ...string) (string, error) {
	stdout, stderr, _, err := g.execute(args...)
	if err != nil {
		command := "命令"
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				command = args[i]
				break
			}
		}
		return "", errors.New(fmt.Sprintf("git %s 失败：%+v\n%s%s", command, err, stderr, stdout))
	}
	return stdout, nil
}

func (g godGit) optionalCommit(ref string) (string, error) {
	stdout, stderr, code, err := g.execute("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if code == 1 {
		return "", nil
	}
	if err != nil {
		return "", errors.New(fmt.Sprintf("读取引用 %s 失败：%+v\n%s", ref, err, stderr))
	}
	return strings.TrimSpace(stdout), nil
}

func (g godGit) requireClean() error {
	status, err := g.command("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New(fmt.Sprintf("临时 worktree 存在额外修改，已停止操作：\n%s", status))
	}
	return nil
}

func (g godGit) worktrees() ([]godWorktree, error) {
	listing, err := g.command("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var worktrees []godWorktree
	for _, field := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			worktrees = append(worktrees, godWorktree{path: filepath.Clean(filepath.FromSlash(strings.TrimPrefix(field, "worktree ")))})
		} else if strings.HasPrefix(field, "branch ") && len(worktrees) > 0 {
			worktrees[len(worktrees)-1].branch = strings.TrimPrefix(field, "branch ")
		}
	}
	return worktrees, nil
}

func (s *godSession) cleanup() error {
	// 清理不使用已取消的运行上下文，且只处理本次创建的 worktree 和临时引用。
	git := godGit{ctx: context.Background(), dir: s.git.dir}
	var problems []string
	worktrees, err := git.worktrees()
	worktreeClean := err == nil
	if err != nil {
		problems = append(problems, fmt.Sprintf("读取 worktree 列表失败：%+v", err))
	} else {
		for _, branch := range s.branches {
			for _, worktree := range worktrees {
				expected := filepath.Clean(branch.worktree)
				if worktree.path != expected && !(runtime.GOOS == "windows" && strings.EqualFold(worktree.path, expected)) {
					continue
				}
				if _, err := git.command("worktree", "remove", "--force", branch.worktree); err != nil {
					worktreeClean = false
					problems = append(problems, fmt.Sprintf("清理 worktree %s 失败：%+v", branch.worktree, err))
				}
			}
		}
	}
	refs, err := git.command("for-each-ref", "--format=%(refname)", s.refs)
	if err != nil {
		problems = append(problems, fmt.Sprintf("列出临时引用 %s 失败：%+v", s.refs, err))
	} else {
		for _, ref := range strings.Fields(refs) {
			if _, err := git.command("update-ref", "-d", ref); err != nil {
				problems = append(problems, fmt.Sprintf("清理临时引用 %s 失败：%+v", ref, err))
			}
		}
	}
	if worktreeClean {
		if err := os.RemoveAll(s.tempDir); err != nil {
			problems = append(problems, fmt.Sprintf("清理临时目录 %s 失败：%+v", s.tempDir, err))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "；"))
	}
	fmt.Fprintln(s.out, "临时 worktree、目录和引用已清理。")
	return nil
}
