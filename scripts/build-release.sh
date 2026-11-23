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

# The build may succeed while skipping an artifact, so a successful build
# alone does not mean a releasable one. Check every expected artifact
# afterwards and fail listing what is missing. Names are MyGo's: the disk
# image is xiaomu-<version>.dmg, the manual install archive is
# xiaomu-<version>-macos-universal.zip, the update archive is
# xiaomu-<version>-darwin-universal.tar.gz, and the manifest is
# update-darwin-universal.json, all under build/darwin-universal/.
# (Same check the Windows script got as its P1 fix; SHA256SUMS is made at
# publish time, not by the build, so it is not checked here.)
command -v python3 >/dev/null 2>&1 || {
  printf '%s\n' '需要 python3 来读取 mygo.json 的版本。' >&2
  exit 1
}
version=$(python3 -c 'import json; print(json.load(open("mygo.json"))["version"])')

go tool mygo build -platform darwin/universal -skip-notarize "$@"

# MyGo v0.10.0 builds Xiaomu.app, the .dmg and the signed update archive, but no
# longer produces the manual-install macOS .zip automatically. Package the freshly
# signed app bundle with ditto so permissions, symlinks and signatures stay intact.
if [ -d "build/darwin-universal/Xiaomu.app" ]; then
  rm -f "build/darwin-universal/xiaomu-$version-macos-universal.zip"
  ditto -c -k --sequesterRsrc --keepParent \
    "build/darwin-universal/Xiaomu.app" \
    "build/darwin-universal/xiaomu-$version-macos-universal.zip"
fi

missing=""
for f in \
  "build/darwin-universal/xiaomu-$version.dmg" \
  "build/darwin-universal/xiaomu-$version-macos-universal.zip" \
  "build/darwin-universal/xiaomu-$version-darwin-universal.tar.gz" \
  "build/darwin-universal/update-darwin-universal.json" \
; do
  if [ ! -s "$f" ]; then
    missing="$missing
  $f"
  fi
done

if [ -n "$missing" ]; then
  printf '%s\n' "发布构建不完整，缺少以下产物：$missing" >&2
  exit 1
fi
