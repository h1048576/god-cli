# git-cli

命令行工具集合，每个 CLI 使用独立目录存放源码、配置和使用说明。

## 工具列表

| 目录 | 工具 | 说明 |
| --- | --- | --- |
| [gitmod](./gitmod/) | `gitmod` | 管理 `go.mod` 中指定模块的版本，支持 `god` 在临时 worktree 中跨分支更新、提交并推送，详见 [使用说明](./gitmod/README.md) |
| [gitmerge](./gitmerge/) | `gitmerge` | 预检查全部目标，在临时 worktree 中逐个合并、推送并拉取更新本地分支，详见 [使用说明](./gitmerge/README.md) |
| [gitbr](./gitbr/) | `gitbr` | 交互选择、搜索本地分支，支持 `--remote` 筛选最近 30 天有提交的远程分支并切换，详见 [使用说明](./gitbr/README.md) |

## 目录约定

新增 CLI 时，在仓库根目录下创建对应目录，将该工具的源码、配置和文档放入其中，并更新上方工具列表。各工具的构建操作在各自目录中执行：

- `gitmod`、`gitbr`：module 模式构建，直接执行 `./build.sh`（等价于 `GO111MODULE=on go mod tidy -compat=1.17 && go build .`）
- `gitmerge`：GOPATH 模式构建，执行 `GO111MODULE=off go build -o gitmerge.exe .`

错误处理统一使用 Go 标准库 `errors` / `fmt.Errorf`，不依赖内部模块。
