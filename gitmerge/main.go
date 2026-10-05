package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/context"
	"wesure.cn/msf/errors"
)

type gitClient struct {
	dir string
	ctx context.Context
}

type mergeTarget struct {
	name string
	head string
	tree string
}

type mergeSession struct {
	git       gitClient
	tempDir   string
	worktree  string
	refs      string
	pushURL   string
	succeeded []string
	out       io.Writer
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	go func() {
		select {
		case <-interrupts:
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "gitmerge: %+v\n", err)
		os.Exit(1)
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `用法：
  gitmerge [--remote origin] <源分支> <目标分支> [其他目标分支...]

示例：
  gitmerge a b c d
  gitmerge --remote upstream feature test release

源分支优先使用本地已提交的版本，本地不存在时使用远端版本。
目标分支必须已存在于远端，以远端最新提交为准。
先用 git merge-tree 检查全部目标；有冲突则列出分支和文件，不合并、不推送。
全部通过后，在临时 detached worktree 中逐个合并并推送，最后清理临时资源。
不切换当前分支，不修改当前工作区或本地同名目标分支。
推送失败时停止后续操作；已经推送的分支不会自动回滚。
需要支持 merge-tree --write-tree 的 Git（建议 Git 2.38 或以上）。`)
}

func run(ctx context.Context, args []string, out io.Writer) (result error) {
	flags := flag.NewFlagSet("gitmerge", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() { usage(out) }
	remote := flags.String("remote", "origin", "推送远端名称，须放在分支参数之前")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	branches := flags.Args()
	if len(branches) < 2 {
		usage(out)
		return errors.New("请指定一个源分支和至少一个目标分支")
	}
	git := gitClient{ctx: ctx}
	seen := make(map[string]bool)
	for _, name := range branches {
		if name == "HEAD" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "refs/") {
			return errors.New(fmt.Sprintf("请使用分支短名：%q", name))
		}
		if _, err := git.command("check-ref-format", "refs/heads/"+name); err != nil {
			return errors.New(fmt.Sprintf("无效分支名 %q：%+v", name, err))
		}
		if seen[name] {
			return errors.New(fmt.Sprintf("源分支和目标分支不能重复：%s", name))
		}
		seen[name] = true
	}
	root, err := git.command("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	git.dir = strings.TrimSpace(root)
	if *remote == "" || strings.HasPrefix(*remote, "-") {
		return errors.New("远端名称不能为空或以 - 开头")
	}
	urls, err := git.command("remote", "get-url", "--push", "--all", *remote)
	if err != nil {
		return err
	}
	pushURLs := strings.Split(strings.TrimSpace(urls), "\n")
	if len(pushURLs) != 1 || strings.TrimSpace(pushURLs[0]) == "" {
		return errors.New("远端必须只有一个推送地址，避免多个仓库只推送部分成功")
	}
	localSource, err := git.optionalCommit("refs/heads/" + branches[0])
	if err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "git-cli-merge-")
	if err != nil {
		return errors.New(fmt.Sprintf("创建临时目录失败：%+v", err))
	}
	session := &mergeSession{
		git: git, tempDir: tempDir, worktree: filepath.Join(tempDir, "worktree"),
		refs:    "refs/git-cli-merge/" + filepath.Base(tempDir) + "/",
		pushURL: strings.TrimSpace(pushURLs[0]), out: out,
	}
	defer func() {
		if err := session.cleanup(); err != nil {
			if result == nil {
				result = err
			} else {
				result = errors.New(fmt.Sprintf("%+v；清理失败：%+v", result, err))
			}
		}
		if len(session.succeeded) > 0 {
			fmt.Fprintf(out, "已推送成功：%s\n", strings.Join(session.succeeded, "、"))
		} else {
			fmt.Fprintln(out, "没有已确认推送成功的目标分支。")
		}
	}()
	// 从实际推送地址读取快照，并隔离临时引用，不更新本地分支、远端跟踪分支或 FETCH_HEAD。
	fmt.Fprintf(out, "读取远端 %s 的分支快照...\n", *remote)
	if _, err := git.command("fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		"--refmap=", "--", session.pushURL, "+refs/heads/*:"+session.refs+"*"); err != nil {
		return err
	}
	source := localSource
	if source == "" {
		source, err = git.optionalCommit(session.refs + branches[0])
		if err != nil {
			return err
		}
		if source == "" {
			return errors.New(fmt.Sprintf("源分支 %s 在本地和远端均不存在", branches[0]))
		}
		fmt.Fprintf(out, "源分支：%s（远端，%s）\n", branches[0], source)
	} else {
		fmt.Fprintf(out, "源分支：%s（本地已提交版本，%s）\n", branches[0], source)
	}
	targets, err := session.preflight(source, branches[1:])
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "全部目标预检查通过，开始逐个合并并推送。")
	for _, target := range targets {
		if err := session.mergeAndPush(branches[0], source, target); err != nil {
			return errors.New(fmt.Sprintf("处理目标分支 %s 失败，已停止后续操作：%+v", target.name, err))
		}
	}
	return nil
}

func (g gitClient) execute(args ...string) (string, string, int, error) {
	if g.dir != "" {
		args = append([]string{"-C", g.dir}, args...)
	}
	cmd := exec.CommandContext(g.ctx, "git", args...)
	cmd.Stdin = os.Stdin
	// 稳定冲突类型和诊断语言，文件路径仍按原始 UTF-8 输出。
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_NAMESPACE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "LC_ALL", "LANG":
			// 避免继承的仓库定位变量让临时 worktree 命令误操作原工作区。
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

func (g gitClient) command(args ...string) (string, error) {
	stdout, stderr, _, err := g.execute(args...)
	if err != nil {
		return "", errors.New(fmt.Sprintf("git %s 失败：%+v\n%s%s", args[0], err, stderr, stdout))
	}
	return stdout, nil
}

func (g gitClient) optionalCommit(ref string) (string, error) {
	stdout, stderr, code, err := g.execute("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if code == 1 {
		return "", nil
	}
	if err != nil {
		return "", errors.New(fmt.Sprintf("读取引用 %s 失败：%+v\n%s", ref, err, stderr))
	}
	return strings.TrimSpace(stdout), nil
}

func (s *mergeSession) preflight(source string, names []string) ([]mergeTarget, error) {
	var targets []mergeTarget
	failed := false
	for _, name := range names {
		if err := s.git.ctx.Err(); err != nil {
			return nil, err
		}
		head, err := s.git.optionalCommit(s.refs + name)
		if err != nil {
			return nil, err
		}
		if head == "" {
			fmt.Fprintf(s.out, "[失败] %s：远端分支不存在\n", name)
			failed = true
			continue
		}
		stdout, stderr, code, err := s.git.execute("merge-tree", "--write-tree", "--name-only", "--messages", "-z", head, source)
		if code != 0 && code != 1 {
			fmt.Fprintf(s.out, "[失败] %s：git merge-tree 执行失败：%+v\n%s\n", name, err, stderr)
			failed = true
			continue
		}
		tree, files, messages, parseErr := parseMergeTree(stdout)
		if parseErr != nil {
			fmt.Fprintf(s.out, "[失败] %s：%+v\n", name, parseErr)
			failed = true
			continue
		}
		if code == 1 {
			failed = true
			fmt.Fprintf(s.out, "[冲突] %s\n", name)
			for _, file := range files {
				fmt.Fprintf(s.out, "  文件/路径：%s\n", strconv.Quote(file))
			}
			for _, message := range messages {
				fmt.Fprintf(s.out, "  %s\n", strings.TrimSpace(message))
			}
			if len(files) == 0 {
				fmt.Fprintln(s.out, "  Git 未提供具体文件列表，请查看上述冲突详情。")
			}
			continue
		}
		fmt.Fprintf(s.out, "[通过] %s\n", name)
		targets = append(targets, mergeTarget{name: name, head: head, tree: tree})
	}
	if failed {
		return nil, errors.New("预检查未全部通过，未创建合并 worktree，未合并或推送任何分支")
	}
	return targets, nil
}

// -z 输出由 tree OID、文件列表、空字段和结构化消息组成。
// 某些目录重命名冲突仅存在于消息中，不能只检查文件列表是否为空。
func parseMergeTree(output string) (string, []string, []string, error) {
	fields := strings.Split(output, "\x00")
	if len(fields) < 2 || (len(fields[0]) != 40 && len(fields[0]) != 64) {
		return "", nil, nil, errors.New("无法解析 merge-tree 输出，请检查 Git 是否支持 --write-tree -z")
	}
	paths := make(map[string]bool)
	i := 1
	for i < len(fields) && fields[i] != "" {
		paths[fields[i]] = true
		i++
	}
	i++
	var messages []string
	for i < len(fields) && fields[i] != "" {
		count, err := strconv.Atoi(fields[i])
		if err != nil || count < 0 || count > len(fields)-i-3 {
			return "", nil, nil, errors.New("无法解析 merge-tree 冲突详情")
		}
		kind := fields[i+1+count]
		message := fields[i+2+count]
		if strings.HasPrefix(kind, "CONFLICT") {
			for _, path := range fields[i+1 : i+1+count] {
				paths[path] = true
			}
			messages = append(messages, message)
		}
		i += count + 3
	}
	files := make([]string, 0, len(paths))
	for path := range paths {
		files = append(files, path)
	}
	sort.Strings(files)
	return fields[0], files, messages, nil
}

func (s *mergeSession) mergeAndPush(sourceName, source string, target mergeTarget) error {
	fmt.Fprintf(s.out, "[合并] %s -> %s\n", sourceName, target.name)
	if _, err := s.git.command("worktree", "add", "--detach", s.worktree, target.head); err != nil {
		return err
	}
	git := gitClient{ctx: s.git.ctx, dir: s.worktree}
	// 禁用自动 stash 和递归子模块操作；强制 ort，与 merge-tree 的预检查策略一致。
	if _, err := git.command("-c", "submodule.recurse=false", "merge", "--strategy=ort", "--ff",
		"--no-edit", "--no-autostash", "-m", fmt.Sprintf("合并分支 %s 到 %s", sourceName, target.name), source); err != nil {
		return err
	}
	head, err := git.command("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	tree, err := git.command("rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(tree) != target.tree {
		return errors.New("实际合并结果与预检查不一致，已取消该分支推送")
	}
	fmt.Fprintf(s.out, "[推送] %s\n", target.name)
	// 使用确切提交和完整目标引用，不依赖 upstream 或 push.default，不强制推送。
	if _, err := s.git.command("push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--",
		s.pushURL, strings.TrimSpace(head)+":refs/heads/"+target.name); err != nil {
		return err
	}
	s.succeeded = append(s.succeeded, target.name)
	fmt.Fprintf(s.out, "[完成] %s 已推送\n", target.name)
	return s.removeWorktree()
}

func (s *mergeSession) removeWorktree() error {
	// 即使收到 Ctrl+C，也使用独立上下文执行清理。
	git := gitClient{ctx: context.Background(), dir: s.git.dir}
	listing, err := git.command("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(listing, "\x00") {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(field, "worktree ")
		actual := filepath.Clean(filepath.FromSlash(path))
		expected := filepath.Clean(s.worktree)
		if actual != expected && !(runtime.GOOS == "windows" && strings.EqualFold(actual, expected)) {
			continue
		}
		// 仅移除本次创建的临时目录；失败的合并可能在其中留下冲突文件。
		if _, err := git.command("worktree", "remove", "--force", s.worktree); err != nil {
			return errors.New(fmt.Sprintf("移除临时 worktree %s 失败：%+v", s.worktree, err))
		}
	}
	return nil
}

func (s *mergeSession) cleanup() error {
	var problems []string
	worktreeErr := s.removeWorktree()
	if worktreeErr != nil {
		problems = append(problems, fmt.Sprintf("%+v", worktreeErr))
	}
	git := gitClient{ctx: context.Background(), dir: s.git.dir}
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
	if worktreeErr == nil {
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
