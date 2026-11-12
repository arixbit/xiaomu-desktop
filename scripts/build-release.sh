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

exec go tool mygo build -platform darwin/universal -skip-notarize "$@"
