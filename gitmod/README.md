# gitmod

`gitmod` 是一个轻量的 go.mod 模块版本管理命令行工具。它只修改当前目录 `go.mod` 中指定模块的版本号，不执行 `go get`、`go mod tidy`，也不改动 `go.sum`，适合需要频繁切换内部模块版本或分支的多人协作项目。

## 特性

- **交互式选择**：直接运行 `gitmod`，通过方向键选择模块，空格或回车按配置的默认版本更新
- **保留注释和格式**：直接在原始字节上做精确替换，`go.mod` 中的注释、空行、对齐格式原样保留
- **支持 replace**：存在带版本的 `replace` 时读取并修改右侧版本，否则修改 `require` 版本；本地目录替换自动跳过
- **支持分支名**：版本参数可以是语义化版本，也可以是任意分支名（如 `master`、`test`），按原样写入 `go.mod`
- **原子写入**：通过临时文件 + 重命名落盘，写入前检测 `go.mod` 是否被并发修改，避免覆盖他人改动
- **主目录兜底**：当前目录没有 `mod.yml` 时，自动回退读取用户主目录下的 `mod.yml`，多项目共享一份配置
- **彩色输出**：终端下区分「更新 / 不变」状态，自动遵循 `NO_COLOR` 与 `TERM=dumb`
- **跨平台对齐**：表格输出按终端显示宽度计算列宽，中文与全角字符不会导致错位

## 构建

需要 Go 1.17 及以上。从仓库根目录进入 `gitmod` 目录后构建：

```sh
cd gitmod
go build -o gitmod.exe
```

## 配置

工具通过 `mod.yml` 知道每个模块简写对应的模块路径和默认版本。默认优先读取当前工作目录下的 `mod.yml`，不存在时回退读取用户主目录下的 `mod.yml`；可通过环境变量 `MOD_CONFIG` 指定配置路径，`--config` 参数优先级最高（须放在命令之前）。

```yaml
modules:
  rpc: [wesure.com/rpcproto, test]
  common: [health/common, test]
  app-insure: [config/app-insure, master]
```

- 键为**简写**，不能是纯数字，也不能是 `list`、`all`、`help` 等保留字
- 值为 `[模块路径, 默认版本]` 两个元素
- 同一模块路径不允许重复配置

## 用法

```text
gitmod [--config 路径]                 方向键选择，空格或回车按默认版本更新
gitmod [--config 路径] <简写或序号> [版本或分支]
gitmod [--config 路径] list
gitmod [--config 路径] all
```

### 交互模式

在项目目录下直接运行：

```sh
gitmod
```

会列出当前项目匹配的模块，用 `↑`/`↓` 选择，空格或回车按默认版本更新，`Esc` / `Ctrl+C` 取消。

### 按简写更新

```sh
gitmod rpc              # 将 rpc 对应模块更新为默认版本 test
gitmod rpc v1.2.3       # 更新为指定版本
gitmod app-insure master # 更新为指定分支
```

### 按序号更新

先运行 `gitmod list` 查看序号，再按序号操作：

```sh
gitmod list
gitmod 1                # 将序号 1 对应的模块更新为默认版本
gitmod 1 master
```

序号从 1 开始，对应当前项目 `list` 的顺序；修改配置顺序或项目依赖后，序号可能变化，请重新查看。

### 批量更新

```sh
gitmod all              # 所有配置项均按各自默认版本更新
```

### 查看列表

```sh
gitmod list
```

输出示例：

```text
序号  简写         模块                   默认版本  当前版本
1    rpc         wesure.com/rpcproto    test     v0.0.0-20260924090412-c0ff38d59431
2    common      health/common          test     v0.0.0-20260509061412-81733ec9d908
```

## 行为细节

- 只读取当前目录的 `go.mod`，按模块路径精确匹配
- 带旧版本的 `replace` 规则仅在 `require` 版本匹配时生效
- 零版本 `v0.0.0-00010101000000-000000000000` 始终跳过
- 版本已与目标一致时输出「不变」，不做写入
- 写入前若 `go.mod` 发生变化，会取消本次写入并提示重试

## License

MIT
