package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func resolveConfigPath(path string) (string, error) {
	if path == "" {
		path = os.Getenv("MOD_CONFIG")
	}
	if path == "" {
		path = "mod.yml"
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return "", errors.New(fmt.Sprintf("检查项目配置 %s 失败：%+v", path, err))
			}
			userDir, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New(fmt.Sprintf("获取用户主目录失败：%+v", err))
			}
			path = filepath.Join(userDir, "mod.yml")
		}
	}
	return path, nil
}

func parseModuleEntries(data []byte) (map[string][]moduleEntry, error) {
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
		return nil, errors.New(fmt.Sprintf("解析 go.mod 失败：%+v", err))
	}
	return collectModules(data, file)
}

// 普通命令和 god 共用相同的版本替换逻辑；计算阶段不写入文件。
func prepareModuleUpdate(data []byte, byPath map[string][]moduleEntry, modules []moduleConfig) ([]byte, [][]string, bool) {
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
	// 从后向前修改字节区间，避免前面的替换改变后续位置。
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	updated := append([]byte(nil), data...)
	for _, edit := range edits {
		updated = append(updated[:edit.start], append([]byte(edit.version), updated[edit.end:]...)...)
	}
	return updated, messages, matched
}
