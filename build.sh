#!/bin/bash

# 循环进入每个工具目录，执行各自的 build.sh；某个工具构建失败时继续其余工具，最后汇总退出码。
root="$(cd "$(dirname "$0")" && pwd)"
failed=""
for dir in "$root"/*/; do
	name="$(basename "$dir")"
	[ -f "$dir/build.sh" ] || continue
	echo "==> 构建 $name"
	if ! (cd "$dir" && ./build.sh); then
		echo "构建 $name 失败" >&2
		failed="$failed $name"
	fi
done
if [ -n "$failed" ]; then
	echo "以下工具构建失败：$failed" >&2
	exit 1
fi
echo "全部构建完成。"
