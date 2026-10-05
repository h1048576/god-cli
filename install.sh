#!/bin/bash

# 先构建全部工具，再把生成的 exe 复制到用户主目录 .god 目录下。
root="$(cd "$(dirname "$0")" && pwd)"
target="$HOME/.god"
mkdir -p "$target" || exit 1
(cd "$root" && ./build.sh) || exit 1
failed=""
for exe in "$root"/*/*.exe; do
	[ -f "$exe" ] || continue
	name="$(basename "$exe")"
	if cp -f "$exe" "$target/"; then
		echo "已安装 $name -> $target/$name"
	else
		echo "复制 $name 失败" >&2
		failed="$failed $name"
	fi
done
if [ -n "$failed" ]; then
	echo "以下工具安装失败：$failed" >&2
	exit 1
fi
echo "全部安装完成。"
echo "请确认 $target 已加入 PATH。"
