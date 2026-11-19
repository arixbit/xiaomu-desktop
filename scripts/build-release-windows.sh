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

# The build may skip the installers without failing (no NSIS on this
# machine), so a successful build alone does not mean a releasable one.
# Check every expected artifact afterwards, for both architectures, and
# fail listing what is missing. Names are MyGo's: the installer is
# <slug>-setup-<version>-<arch>.exe, the update archive is
# <slug>-<version>-windows-<arch>.tar.gz, and the manifest is
# update-windows-<arch>.json, all under build/windows-<arch>/.
command -v python3 >/dev/null 2>&1 || {
  printf '%s\n' '需要 python3 来读取 mygo.json 的版本。' >&2
  exit 1
}
version=$(python3 -c 'import json; print(json.load(open("mygo.json"))["version"])')

go tool mygo build -platform windows/amd64,windows/arm64 "$@"

missing=""
for arch in amd64 arm64; do
  for f in \
    "build/windows-$arch/xiaomu-setup-$version-$arch.exe" \
    "build/windows-$arch/xiaomu-$version-windows-$arch.tar.gz" \
    "build/windows-$arch/update-windows-$arch.json" \
  ; do
    if [ ! -s "$f" ]; then
      missing="$missing
  $f"
    fi
  done
done

if [ -n "$missing" ]; then
  printf '%s\n' "发布构建不完整，缺少以下产物：$missing" >&2
  exit 1
fi
