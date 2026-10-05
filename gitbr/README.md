# gitbr

使用 Go + promptui 实现的终端分支选择界面，交互方式与 gitmod 一致。

在任意 Git 工作目录或其子目录运行 `gitbr`，显示按名称排序的全部本地分支，标记并默认选中当前分支。分支较多时可上下滚动。

- `↑` / `↓`：选择分支。
- 空格或回车：立即切换到选中的分支。
- `Esc`（Windows）/ `Ctrl+C`：取消。
- `gitbr --help`：查看帮助。

切换使用 `git switch --no-guess`，需要 Git 2.23 或更新版本。不创建分支、不拉取、不提交、不推送。Git 允许保留的未提交修改会随切换保留；如果修改将被覆盖，或者分支已被其他 worktree 占用，会报告错误。不强制切换、不自动暂存。

新仓库没有本地分支时会提示；非 Git 工作目录、裸仓库或非交互终端会报告错误。无需配置文件，支持中文分支名。

## 构建和使用

需要 Go 1.17 及以上，依赖版本与 gitmod 保持一致。使用此目录的 `go.mod` 构建（`wesure.cn/msf/errors` 依赖需在本机缓存或可访问的内部依赖源中提供）：

```powershell
$env:GO111MODULE = 'on'
go build -o gitbr.exe .
```

将 `gitbr.exe` 所在目录加入 `PATH` 后，可在项目目录直接运行：

```powershell
gitbr
```

也可以直接调用可执行文件的完整路径。
