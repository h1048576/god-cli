package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/context"
	"wesure.cn/msf/errors"
)

type godBranch struct {
	name      string
	worktree  string
	localHead string
	readyHead string
	original  []byte
	updated   []byte
	messages  [][]string
}

type godSession struct {
	git      godGit
	tempDir  string
	refs     string
	remote   string
	pushURL  string
	modPath  string
	module   moduleConfig
	branches []*godBranch
	pushed   []string
	synced   []string
	skipped  []string
	out      io.Writer
}

func runGod(args []string, configPath, remote string, out io.Writer) (result error) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		usage(out)
		return nil
	}
	if len(args) < 2 {
		return errors.New("用法：gitmod [--config 路径] [--remote origin] god <简写或序号> <分支> [其他分支...]")
	}
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
	git := godGit{ctx: ctx}
	if remote == "" || strings.HasPrefix(remote, "-") {
		return errors.New("远端名称不能为空或以 - 开头")
	}
	seen := make(map[string]bool)
	for _, name := range args[1:] {
		if name == "HEAD" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "refs/") {
			return errors.New(fmt.Sprintf("请使用有效的分支短名：%q", name))
		}
		if _, err := git.command("check-ref-format", "refs/heads/"+name); err != nil {
			return errors.New(fmt.Sprintf("无效分支名 %q：%+v", name, err))
		}
		if _, err := git.command("check-ref-format", "refs/remotes/"+remote+"/"+name); err != nil {
			return err
		}
		if seen[name] {
			return errors.New(fmt.Sprintf("目标分支重复：%s", name))
		}
		seen[name] = true
	}
	root, err := git.command("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	git.dir = filepath.Clean(strings.TrimSpace(root))
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(git.dir, filepath.Join(cwd, "go.mod"))
	if err != nil || !godRelativePath(relative) {
		return errors.New("当前 go.mod 路径必须位于 Git 仓库内")
	}
	path, err := resolveConfigPath(configPath)
	if err != nil {
		return err
	}
	modules, err := readConfig(path)
	if err != nil {
		return err
	}
	selected, err := selectGodModule(args[0], modules)
	if err != nil {
		return err
	}
	urls, err := git.command("remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return err
	}
	pushURLs := strings.Split(strings.TrimSpace(urls), "\n")
	if len(pushURLs) != 1 || strings.TrimSpace(pushURLs[0]) == "" {
		return errors.New("god 要求远端只有一个推送地址")
	}
	pushURL := strings.TrimSpace(pushURLs[0])
	if !filepath.IsAbs(pushURL) && !strings.Contains(pushURL, ":") {
		pushURL = filepath.Join(git.dir, pushURL)
	}
	tempDir, err := os.MkdirTemp("", "gitmod-god-")
	if err != nil {
		return errors.New(fmt.Sprintf("创建临时目录失败：%+v", err))
	}
	s := &godSession{
		git: git, tempDir: tempDir, refs: "refs/gitmod-god/" + filepath.Base(tempDir) + "/",
		remote: remote, pushURL: pushURL, modPath: filepath.ToSlash(relative), module: selected, out: out,
	}
	defer func() {
		if err := s.cleanup(); err != nil {
			if result == nil {
				result = err
			} else {
				result = errors.New(fmt.Sprintf("%+v；清理失败：%+v", result, err))
			}
		}
		if len(s.pushed) == 0 {
			fmt.Fprintln(out, "没有已确认推送成功的分支。")
		} else {
			fmt.Fprintf(out, "已推送成功：%s\n", strings.Join(s.pushed, "、"))
		}
		if len(s.synced) > 0 {
			fmt.Fprintf(out, "本地已同步：%s\n", strings.Join(s.synced, "、"))
		}
		if len(s.skipped) > 0 {
			fmt.Fprintf(out, "本地同步已跳过：%s\n", strings.Join(s.skipped, "、"))
		}
	}()
	fmt.Fprintf(out, "固定模块：%s（%s），目标版本：%s\n", selected.alias, selected.path, selected.version)
	if err := s.prepare(args[1:]); err != nil {
		return errors.New(fmt.Sprintf("准备阶段失败，已停止操作；未修改模块、提交或推送：%+v", err))
	}
	fmt.Fprintln(out, "全部分支已更新且模块检查通过，开始逐个修改、提交并推送。")
	for _, branch := range s.branches {
		if err := s.updateAndPush(branch); err != nil {
			return errors.New(fmt.Sprintf("分支 %s 处理失败，已停止后续操作：%+v", branch.name, err))
		}
	}
	return nil
}

func selectGodModule(selector string, modules []moduleConfig) (moduleConfig, error) {
	if idPattern.MatchString(selector) {
		data, err := os.ReadFile("go.mod")
		if err != nil {
			return moduleConfig{}, errors.New(fmt.Sprintf("读取当前 go.mod 以解析模块序号失败：%+v", err))
		}
		entries, err := parseModuleEntries(data)
		if err != nil {
			return moduleConfig{}, err
		}
		visible := visibleModules(modules, entries)
		id, err := strconv.Atoi(selector)
		if err != nil || id < 1 || id > len(visible) {
			return moduleConfig{}, errors.New(fmt.Sprintf("无效的模块序号 %q，请先运行 gitmod list", selector))
		}
		return visible[id-1], nil
	}
	for _, item := range modules {
		if item.alias == selector {
			return item, nil
		}
	}
	return moduleConfig{}, errors.New(fmt.Sprintf("配置中不存在简写 %q", selector))
}

// 第一阶段只在 detached worktree 中拉取并计算更新内容，所有分支通过后才允许写入。
func (s *godSession) prepare(names []string) error {
	fmt.Fprintf(s.out, "读取远端 %s 的全部目标分支...\n", s.remote)
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--refmap=", "--", s.pushURL}
	for _, name := range names {
		args = append(args, "+refs/heads/"+name+":"+s.refs+name)
	}
	if _, err := s.git.command(args...); err != nil {
		return err
	}
	for i, name := range names {
		localHead, err := s.git.optionalCommit("refs/heads/" + name)
		if err != nil {
			return err
		}
		start := localHead
		if start == "" {
			start, err = s.git.optionalCommit(s.refs + name)
			if err != nil {
				return err
			}
			if start == "" {
				return errors.New(fmt.Sprintf("远端分支 %s 不存在", name))
			}
		}
		branch := &godBranch{name: name, localHead: localHead, worktree: filepath.Join(s.tempDir, strconv.Itoa(i+1))}
		s.branches = append(s.branches, branch)
		fmt.Fprintf(s.out, "[更新分支] %s\n", name)
		if _, err := s.git.command("-c", "submodule.recurse=false", "worktree", "add", "--detach", branch.worktree, start); err != nil {
			return err
		}
		git := godGit{ctx: s.git.ctx, dir: branch.worktree}
		if _, err := git.command("-c", "submodule.recurse=false", "pull", "--ff-only", "--no-rebase",
			"--no-autostash", "--no-tags", "--no-recurse-submodules", "--refmap=", "--", s.pushURL, "refs/heads/"+name); err != nil {
			return errors.New(fmt.Sprintf("分支 %s 更新失败：%+v", name, err))
		}
		branch.readyHead, err = git.optionalCommit("HEAD")
		if err != nil {
			return err
		}
		fmt.Fprintf(s.out, "[更新成功] %s\n", name)
	}
	changed := false
	for _, branch := range s.branches {
		if err := s.planModule(branch); err != nil {
			return errors.New(fmt.Sprintf("分支 %s 模块检查失败：%+v", branch.name, err))
		}
		changed = changed || !bytes.Equal(branch.original, branch.updated)
	}
	if changed {
		for _, variable := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
			if _, err := s.git.command("var", variable); err != nil {
				return errors.New(fmt.Sprintf("请先配置 Git 提交身份：%+v", err))
			}
		}
	}
	return nil
}

func (s *godSession) planModule(branch *godBranch) error {
	git := godGit{ctx: s.git.ctx, dir: branch.worktree}
	if err := git.requireClean(); err != nil {
		return err
	}
	path := filepath.Join(branch.worktree, filepath.FromSlash(s.modPath))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New(fmt.Sprintf("%s 必须是存在的普通文件：%+v", s.modPath, err))
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(branch.worktree, resolved)
	if err != nil || !godRelativePath(relative) {
		return errors.New("go.mod 不能通过符号链接指向临时 worktree 之外")
	}
	if _, err := git.command("--literal-pathspecs", "ls-files", "--error-unmatch", "--", s.modPath); err != nil {
		return errors.New(fmt.Sprintf("%s 必须已由 Git 跟踪：%+v", s.modPath, err))
	}
	branch.original, err = os.ReadFile(path)
	if err != nil {
		return err
	}
	entries, err := parseModuleEntries(branch.original)
	if err != nil {
		return err
	}
	if len(visibleModules([]moduleConfig{s.module}, entries)) == 0 {
		return errors.New(fmt.Sprintf("%s 中没有可更新的模块 %s（缺失、本地替换或零版本）", s.modPath, s.module.path))
	}
	branch.updated, branch.messages, _ = prepareModuleUpdate(branch.original, entries, []moduleConfig{s.module})
	return nil
}

func (s *godSession) updateAndPush(branch *godBranch) error {
	git := godGit{ctx: s.git.ctx, dir: branch.worktree}
	if err := git.requireClean(); err != nil {
		return err
	}
	head, err := git.optionalCommit("HEAD")
	if err != nil {
		return err
	}
	if head != branch.readyHead {
		return errors.New("临时 worktree 的提交在准备后发生变化")
	}
	fmt.Fprintf(s.out, "[更新模块] %s\n", branch.name)
	if !bytes.Equal(branch.original, branch.updated) {
		if err := saveModAt(filepath.Join(branch.worktree, filepath.FromSlash(s.modPath)), branch.original, branch.updated); err != nil {
			return err
		}
		// --only 限定提交文件；verbatim 保证提交信息始终是用户指定的单个 -。
		if _, err := git.command("--literal-pathspecs", "commit", "--only", "--cleanup=verbatim", "-m", "-", "--", s.modPath); err != nil {
			return err
		}
		head, err = git.optionalCommit("HEAD")
		if err != nil {
			return err
		}
		parent, err := git.optionalCommit("HEAD^")
		if err != nil {
			return err
		}
		files, err := git.command("diff", "--name-only", "-z", branch.readyHead, head)
		if err != nil {
			return err
		}
		if parent != branch.readyHead || files != s.modPath+"\x00" {
			return errors.New(fmt.Sprintf("提交 %s 的父提交或文件范围与计划不一致，已取消推送", head))
		}
		fmt.Fprintf(s.out, "[已提交] %s：%s，提交信息：-\n", branch.name, head)
	} else {
		fmt.Fprintf(s.out, "[无需提交] %s：模块版本已一致\n", branch.name)
	}
	if err := printChanges(s.out, branch.messages); err != nil {
		return err
	}
	if err := git.requireClean(); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[推送] %s\n", branch.name)
	if _, err := s.git.command("push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--",
		s.pushURL, head+":refs/heads/"+branch.name); err != nil {
		return errors.New(fmt.Sprintf("推送失败，本次提交为 %s：%+v", head, err))
	}
	s.pushed = append(s.pushed, branch.name)
	if _, err := s.git.command("update-ref", "refs/remotes/"+s.remote+"/"+branch.name, head); err != nil {
		return errors.New(fmt.Sprintf("远端已推送，但更新远端跟踪引用失败：%+v", err))
	}
	if err := s.syncLocal(branch, head); err != nil {
		return errors.New(fmt.Sprintf("远端已推送，但本地分支同步失败：%+v", err))
	}
	fmt.Fprintf(s.out, "[完成] %s\n", branch.name)
	return nil
}

func (s *godSession) syncLocal(branch *godBranch, head string) error {
	if branch.localHead == "" {
		fmt.Fprintf(s.out, "[跳过本地同步] %s：本地分支不存在\n", branch.name)
		s.skipped = append(s.skipped, branch.name)
		return nil
	}
	worktrees, err := s.git.worktrees()
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if worktree.branch == "refs/heads/"+branch.name {
			fmt.Fprintf(s.out, "[跳过本地同步] %s：正在工作区 %s 中使用，请之后手动拉取\n", branch.name, worktree.path)
			s.skipped = append(s.skipped, branch.name)
			return nil
		}
	}
	git := godGit{ctx: s.git.ctx, dir: branch.worktree}
	if _, err := git.command("-c", "submodule.recurse=false", "switch", "--no-guess", branch.name); err != nil {
		return err
	}
	if _, err := git.command("-c", "submodule.recurse=false", "-c", "branch."+branch.name+".mergeOptions=",
		"merge", "--ff-only", "--no-autostash", "--no-edit", head); err != nil {
		return err
	}
	s.synced = append(s.synced, branch.name)
	return nil
}

func godRelativePath(path string) bool {
	return !filepath.IsAbs(path) && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
}
