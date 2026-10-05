# git-cli

命令行工具集合，每个 CLI 使用独立目录存放源码、配置和使用说明。

## 工具列表

| 目录 | 工具 | 说明 |
| --- | --- | --- |
| [gitmod](./gitmod/) | `gitmod` | 管理 `go.mod` 中指定模块的版本，详见 [使用说明](./gitmod/README.md) |
| [gitmerge](./gitmerge/) | `gitmerge` | 预检查全部目标，在临时 worktree 中批量合并并逐个推送，详见 [使用说明](./gitmerge/README.md) |

## 目录约定

新增 CLI 时，在仓库根目录下创建对应目录，将该工具的源码、配置和文档放入其中，并更新上方工具列表。各工具的构建操作在各自目录中执行。
