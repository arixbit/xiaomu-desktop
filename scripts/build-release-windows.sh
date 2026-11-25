#!/bin/sh
# Release builds must include a signed update archive and manifest.
set +x
set -eu

cd "$(dirname "$0")/.."

if [ -z "${MYGO_UPDATER_PRIVATE_KEY:-}" ]; then
  key_file=${XIAOMU_UPDATE_KEY_FILE:-"$HOME/Library/Application Support/Xiaomu/release-keys/mygo-update.key"}
  if [ ! -r "$key_file" ]; then
    printf '%s\n' '缺少更新签名密钥。请设置 XIAOMU_UPDATE_KEY_FILE 或 MYGO_UPDATER_PRIVATE_KEY。' >&2
    exit 1
  fi
  MYGO_UPDATER_PRIVATE_KEY=$(cat "$key_file")
  export MYGO_UPDATER_PRIVATE_KEY
fi

if [ -z "$MYGO_UPDATER_PRIVATE_KEY" ]; then
  printf '%s\n' '更新签名密钥为空，发布构建已停止。' >&2
  exit 1
fi

command -v python3 >/dev/null 2>&1 || {
  printf '%s\n' '需要 python3 来读取 mygo.json 并检查发布产物。' >&2
  exit 1
}

python3 - "$@" <<'PY'
import json
import os
from pathlib import Path
import subprocess
import sys

args = sys.argv[1:]
flags = iter(args)
output_override = ""
for arg in flags:
    # Go's flag parser stops at -- or at the first positional argument.
    if arg in ("--", "-") or not arg.startswith("-"):
        project = next(flags, ".") if arg == "--" else arg
        if Path(project).resolve() != Path.cwd() or next(flags, None) is not None:
            sys.exit("此脚本只能构建当前小幕项目。")
        break

    option, separator, value = arg.partition("=")
    if option in ("-platform", "--platform"):
        sys.exit("Windows 发布固定构建 amd64 与 arm64，不能指定 -platform。")
    if option in ("-upload", "--upload"):
        sys.exit("请校验本地构建产物后单独上传，不能指定 -upload。")
    if option in ("-o", "--o", "-sign", "--sign"):
        if not separator:
            value = next(flags, None)
        if value is None:
            sys.exit(f"{option} 缺少参数。")
        if option in ("-o", "--o"):
            output_override = value

with open("mygo.json", encoding="utf-8") as manifest:
    config = json.load(manifest)
version = config["version"]
slug = config.get("slug") or "xiaomu"
out = Path(os.path.normpath(output_override or config.get("out") or "dist"))

architectures = ("amd64", "arm64")
targets = ",".join(f"windows/{arch}" for arch in architectures)
result = subprocess.run(["go", "tool", "mygo", "build", "-platform", targets, *args])
if result.returncode:
    sys.exit(result.returncode)

# The pinned MyGo replaces each platform directory on success. It may skip
# NSIS without failing, so check both new directories for all release files.
missing = []
for arch in architectures:
    platform = f"windows-{arch}"
    names = (
        f"{slug}-setup-{version}-{arch}.exe",
        f"{slug}-{version}-{platform}.tar.gz",
        f"update-{platform}.json",
    )
    for name in names:
        artifact = out / platform / name
        if not artifact.is_file() or artifact.stat().st_size == 0:
            missing.append(str(artifact))

if missing:
    sys.exit("发布构建不完整，以下产物缺失或为空：\n  " + "\n  ".join(missing))
PY
