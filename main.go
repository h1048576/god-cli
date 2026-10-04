package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/chzyer/readline"
	"github.com/manifoldco/promptui"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/text/width"
	"gopkg.in/yaml.v3"
	"wesure.cn/msf/errors"
)

const zeroVersion = "v0.0.0-00010101000000-000000000000"

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+~\-]*$`)
var tokenPattern = regexp.MustCompile(`\S+`)
var idPattern = regexp.MustCompile(`^[0-9]+$`)

type config struct {
	Modules yaml.Node `yaml:"modules"`
}

type moduleConfig struct {
	alias   string
	version string
	path    string
}

type replacement struct {
	start   int
	end     int
	version string
}

type moduleEntry struct {
	version string
	start   int
	end     int
	local   string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mod: %+v\n", err)
		os.Exit(1)
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `用法：
  mod [--config 路径]                 方向键选择，空格或回车按默认版本更新
  mod [--config 路径] <简写或序号> [版本或分支]
  mod [--config 路径] list
  mod [--config 路径] all

默认配置：当前工作目录下的 mod.yml，可通过 MOD_CONFIG 环境变量覆盖。
--config 的优先级最高，须放在命令前。
只读取当前目录的 go.mod，按模块路径精确匹配。
存在 replace 时读取和修改右侧版本，否则读取和修改 require 版本。
本地目录替换不修改。
list 按配置顺序列出有实际版本的模块，并从 1 开始编号。
数字对应当前项目 list 的序号，例如 mod 1 或 mod 1 master。
修改配置顺序或项目依赖后，序号可能变化，请重新查看 list。
all 使用每项配置的默认版本。
零版本 v0.0.0-00010101000000-000000000000 始终跳过。
保留注释和格式，不执行 go get 或 go mod tidy，不修改 go.sum。

配置示例：
modules:
  rpc: [wesure.com/rpcproto, test]`)
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("mod", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() { usage(out) }
	configPath := flags.String("config", "", "mod.yml 配置文件的路径")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	args = flags.Args()
	interactive := len(args) == 0
	command := ""
	if !interactive {
		command = args[0]
	}
	if command == "help" && len(args) == 1 {
		usage(out)
		return nil
	}
	if len(args) > 2 || ((command == "all" || command == "list") && len(args) != 1) {
		return errors.New("参数数量错误，请运行 mod --help 查看用法")
	}
	if len(args) == 2 && !validVersion(args[1]) {
		return errors.New("版本或分支格式不合法")
	}
	path := *configPath
	if path == "" {
		path = os.Getenv("MOD_CONFIG")
	}
	if path == "" {
		path = "mod.yml"
	}
	modules, err := readConfig(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return errors.New(fmt.Sprintf("读取当前目录的 go.mod 失败：%+v", err))
	}
	// 分支名仅在解析阶段映射成合法版本；输出仍直接修改原始字节。
	file, err := modfile.Parse("go.mod", data, func(path string, version string) (string, error) {
		if semver.IsValid(version) {
			return version, nil
		}
		_, major, ok := module.SplitPathVersion(path)
		if !ok {
			return "", errors.New(fmt.Sprintf("无效的模块路径：%s", path))
		}
		major = strings.TrimSuffix(strings.TrimLeft(major, "/."), "-unstable")
		if major == "" {
			major = "v0"
		}
		return major + ".0.0", nil
	})
	if err != nil {
		return errors.New(fmt.Sprintf("解析 go.mod 失败：%+v", err))
	}
	byPath, err := collectModules(data, file)
	if err != nil {
		return err
	}
	visible := visibleModules(modules, byPath)
	if interactive {
		if len(visible) == 0 {
			_, err := fmt.Fprintln(out, "当前项目没有匹配的配置。")
			return err
		}
		index, err := selectModule(out, visible, byPath)
		if err != nil {
			return err
		}
		if index < 0 {
			return nil
		}
		command = visible[index].alias
	}
	if command == "list" {
		return listModules(out, visible, byPath)
	}
	if command != "all" {
		if idPattern.MatchString(command) {
			id, err := strconv.Atoi(command)
			if err != nil || id < 1 || id > len(visible) {
				return errors.New(fmt.Sprintf("无效的模块序号 %q，请运行 mod list 查看可用序号", command))
			}
			command = visible[id-1].alias
		}
		var selected []moduleConfig
		for _, item := range modules {
			if item.alias == command {
				if len(args) == 2 {
					item.version = args[1]
				}
				selected = append(selected, item)
			}
		}
		if len(selected) == 0 {
			return errors.New(fmt.Sprintf("配置中不存在简写 %q", command))
		}
		modules = selected
	}
	var edits []replacement
	var messages [][]string
	matched := false
	for _, item := range modules {
		for _, entry := range byPath[item.path] {
			matched = true
			if entry.local != "" {
				messages = append(messages, []string{"跳过", item.alias, "(" + item.path + ")", "本地目录替换", "", ""})
				continue
			}
			oldVersion := entry.version
			if oldVersion == zeroVersion {
				continue
			}
			if oldVersion == item.version {
				messages = append(messages, []string{"不变", item.alias, "(" + item.path + ")", oldVersion, "", ""})
				continue
			}
			edits = append(edits, replacement{entry.start, entry.end, item.version})
			messages = append(messages, []string{"更新", item.alias, "(" + item.path + ")", oldVersion, "->", item.version})
		}
	}
	if !matched && command != "all" {
		return errors.New(fmt.Sprintf("当前 go.mod 的 require 或 replace 中没有模块 %s", modules[0].path))
	}
	if len(edits) > 0 {
		// 从后向前修改字节区间，避免前面的替换改变后续位置。
		sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
		updated := append([]byte(nil), data...)
		for _, edit := range edits {
			updated = append(updated[:edit.start], append([]byte(edit.version), updated[edit.end:]...)...)
		}
		if err := saveMod(data, updated); err != nil {
			return err
		}
	}
	return printChanges(out, messages)
}

func validVersion(version string) bool {
	return versionPattern.MatchString(version) && !strings.Contains(version, "//")
}

// require 和带版本的 replace 行均以版本 token 结束，注释不属于此区间。
func readModuleEntry(data []byte, line *modfile.Line) (moduleEntry, error) {
	tokens := tokenPattern.FindAllIndex(data[line.Start.Byte:line.End.Byte], -1)
	last := tokens[len(tokens)-1]
	start, end := line.Start.Byte+last[0], line.Start.Byte+last[1]
	version := string(data[start:end])
	if strings.HasPrefix(version, `"`) {
		var err error
		version, err = strconv.Unquote(version)
		if err != nil {
			return moduleEntry{}, err
		}
	}
	return moduleEntry{version: version, start: start, end: end}, nil
}

func collectModules(data []byte, file *modfile.File) (map[string][]moduleEntry, error) {
	byPath := make(map[string][]moduleEntry)
	for _, requirement := range file.Require {
		entry, err := readModuleEntry(data, requirement.Syntax)
		if err != nil {
			return nil, err
		}
		byPath[requirement.Mod.Path] = append(byPath[requirement.Mod.Path], entry)
	}
	replaced := make(map[string][]moduleEntry)
	for _, rule := range file.Replace {
		// 指定旧版本的 replace 仅在 require 版本匹配时生效。
		if rule.Old.Version != "" && len(byPath[rule.Old.Path]) > 0 {
			tokens := rule.Syntax.Token
			if !rule.Syntax.InBlock {
				tokens = tokens[1:]
			}
			oldVersion := tokens[1]
			if strings.HasPrefix(oldVersion, `"`) {
				oldVersion, _ = strconv.Unquote(oldVersion)
			}
			matches := false
			for _, entry := range byPath[rule.Old.Path] {
				if entry.version == oldVersion {
					matches = true
				}
			}
			if !matches {
				continue
			}
		}
		entry := moduleEntry{local: rule.New.Path}
		if rule.New.Version != "" {
			var err error
			entry, err = readModuleEntry(data, rule.Syntax)
			if err != nil {
				return nil, err
			}
		}
		replaced[rule.Old.Path] = append(replaced[rule.Old.Path], entry)
	}
	for path, entries := range replaced {
		byPath[path] = entries
	}
	return byPath, nil
}

func readConfig(path string) ([]moduleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("读取配置 %s 失败：%+v", path, err))
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, errors.New(fmt.Sprintf("解析配置 %s 失败：%+v", path, err))
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("mod.yml 只允许一个 YAML 文档")
	}
	if cfg.Modules.Kind != yaml.MappingNode || len(cfg.Modules.Content) == 0 {
		return nil, errors.New("mod.yml 的 modules 不能为空")
	}
	// Decode 校验重复键及值类型；Content 保留 YAML 中的原始配置顺序。
	var valuesByAlias map[string][]string
	if err := cfg.Modules.Decode(&valuesByAlias); err != nil {
		return nil, errors.New(fmt.Sprintf("解析配置 %s 失败：%+v", path, err))
	}
	modules := make([]moduleConfig, 0, len(valuesByAlias))
	paths := make(map[string]string)
	for i := 0; i < len(cfg.Modules.Content); i += 2 {
		alias := cfg.Modules.Content[i].Value
		values := valuesByAlias[alias]
		if alias == "" || idPattern.MatchString(alias) || strings.ContainsAny(alias, " \t\r\n") || strings.HasPrefix(alias, "-") || alias == "list" || alias == "all" || alias == "help" {
			return nil, errors.New(fmt.Sprintf("无效或保留的简写：%q", alias))
		}
		if len(values) != 2 || values[0] == "" || strings.ContainsAny(values[0], " \t\r\n") || !validVersion(values[1]) {
			return nil, errors.New(fmt.Sprintf("配置 %s 必须为 [模块路径, 默认版本]，且两个值都必须有效", alias))
		}
		if previous, exists := paths[values[0]]; exists {
			return nil, errors.New(fmt.Sprintf("配置 %s 和 %s 重复指定模块 %s", previous, alias, values[0]))
		}
		paths[values[0]] = alias
		modules = append(modules, moduleConfig{alias: alias, version: values[1], path: values[0]})
	}
	return modules, nil
}

func hasVersion(entry moduleEntry) bool {
	return entry.local == "" && entry.version != "" && entry.version != zeroVersion
}

// 列表和数字序号使用同一份过滤结果，确保序号与命令选择一致。
func visibleModules(modules []moduleConfig, byPath map[string][]moduleEntry) []moduleConfig {
	var visible []moduleConfig
	for _, item := range modules {
		for _, entry := range byPath[item.path] {
			if hasVersion(entry) {
				visible = append(visible, item)
				break
			}
		}
	}
	return visible
}

func moduleRows(modules []moduleConfig, byPath map[string][]moduleEntry) [][]string {
	rows := [][]string{{"序号", "简写", "模块", "默认版本", "当前版本"}}
	for i, item := range modules {
		var versions []string
		seen := make(map[string]bool)
		for _, entry := range byPath[item.path] {
			if hasVersion(entry) && !seen[entry.version] {
				versions = append(versions, entry.version)
				seen[entry.version] = true
			}
		}
		rows = append(rows, []string{strconv.Itoa(i + 1), item.alias, item.path, item.version, strings.Join(versions, ", ")})
	}
	return rows
}

// 先按未着色文本计算列宽，避免 ANSI 颜色代码影响对齐。
func alignRows(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		for column, cell := range row {
			if size := displayWidth(cell); size > widths[column] {
				widths[column] = size
			}
		}
	}
	for _, row := range rows {
		var line strings.Builder
		for column, cell := range row {
			line.WriteString(cell)
			if column < len(row)-1 {
				line.WriteString(strings.Repeat(" ", widths[column]-displayWidth(cell)+2))
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return lines
}

func listModules(out io.Writer, modules []moduleConfig, byPath map[string][]moduleEntry) error {
	for _, line := range alignRows(moduleRows(modules, byPath)) {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	if len(modules) == 0 {
		_, err := fmt.Fprintln(out, "当前项目没有匹配的配置。")
		return err
	}
	return nil
}

func isTerminal(file *os.File) bool {
	return isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd())
}

func useColor(out io.Writer) bool {
	file, ok := out.(*os.File)
	_, noColor := os.LookupEnv("NO_COLOR")
	return ok && isTerminal(file) && !noColor && os.Getenv("TERM") != "dumb"
}

func printChanges(out io.Writer, rows [][]string) error {
	colored := useColor(out)
	writer := out
	if colored {
		writer = colorable.NewColorable(out.(*os.File))
	}
	for i, line := range alignRows(rows) {
		if colored {
			switch rows[i][0] {
			case "更新":
				line = "\x1b[32m更新\x1b[0m" + strings.TrimPrefix(line, "更新")
			case "不变":
				line = "\x1b[90m不变\x1b[0m" + strings.TrimPrefix(line, "不变")
			}
		}
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return err
		}
	}
	return nil
}

type promptWriter struct{ io.Writer }

func (promptWriter) Close() error { return nil }

type selectionKeyReader struct{ io.ReadCloser }

func (r selectionKeyReader) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	if n == 1 && buffer[0] == ' ' {
		buffer[0] = '\r' // 空格与回车使用相同的确认流程。
	}
	// Windows RawReader 每次返回一个按键事件，方向键已经转成控制字符。
	// 仅转换独立的 Esc，避免把方向键或 Alt 组合键误判为取消。
	if runtime.GOOS == "windows" && n == 1 && buffer[0] == 27 {
		buffer[0] = 3 // 复用 Ctrl+C 的终端恢复及取消流程。
	}
	return n, err
}

func selectModule(out io.Writer, modules []moduleConfig, byPath map[string][]moduleEntry) (int, error) {
	file, ok := out.(*os.File)
	if !ok || !isTerminal(file) || !isTerminal(os.Stdin) {
		return -1, errors.New("交互选择需要终端，请使用 mod list 查看列表，再通过简写或序号更新")
	}
	lines := alignRows(moduleRows(modules, byPath))
	active := "> {{ . }}"
	if useColor(out) {
		active = "> {{ . | cyan }}"
	}
	prompt := promptui.Select{
		Label: "    " + lines[0],
		Items: lines[1:], Size: 10, HideHelp: true, HideSelected: true,
		Stdin: selectionKeyReader{readline.Stdin}, Stdout: promptWriter{colorable.NewColorable(file)},
		Templates: &promptui.SelectTemplates{
			Label: "{{ . }}", Active: active, Inactive: "  {{ . }}", Selected: "{{ . }}",
			Details: "↑/↓ 选择模块，空格或回车按默认版本更新，Esc / Ctrl+C 取消",
		},
	}
	index, _, err := prompt.Run()
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return -1, nil
	}
	return index, err
}

// 按终端显示格数计算宽度：中文和全角字符占两格，组合标记不单独占格。
func displayWidth(value string) int {
	size := 0
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			size += 2
		default:
			size++
		}
	}
	return size
}

func saveMod(original, updated []byte) error {
	info, err := os.Stat("go.mod")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(".", ".mod-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	current, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	if !bytes.Equal(original, current) {
		return errors.New("go.mod 在处理期间发生变化，已取消写入，请重试")
	}
	return os.Rename(tmp.Name(), "go.mod")
}
