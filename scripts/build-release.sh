#!/bin/sh
# Release builds must include signed update archives and manifests for both macOS architectures.
# Pass --with-universal-bridge during a migration release to also produce the signed universal
# update archive and manifest for older v0.3.0-v0.9.7 installations.
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
import shutil
import subprocess
import sys

raw_args = sys.argv[1:]
build_args = []
with_universal_bridge = False
flags = iter(raw_args)
output_override = ""
for arg in flags:
    if arg in ("-with-universal-bridge", "--with-universal-bridge"):
        with_universal_bridge = True
        continue

    # Go's flag parser stops at -- or at the first positional argument.
    if arg in ("--", "-") or not arg.startswith("-"):
        project = next(flags, ".") if arg == "--" else arg
        if Path(project).resolve() != Path.cwd() or next(flags, None) is not None:
            sys.exit("此脚本只能构建当前小幕项目。")
        build_args.append(arg)
        if arg == "--" and project != ".":
            build_args.append(project)
        break

    option, separator, value = arg.partition("=")
    if option in ("-platform", "--platform"):
        sys.exit("macOS 发布固定构建 arm64 与 amd64，不能指定 -platform。")
    if option in ("-upload", "--upload"):
        sys.exit("请校验本地构建产物后单独上传，不能指定 -upload。")
    build_args.append(arg)
    if option in ("-o", "--o", "-sign", "--sign"):
        if not separator:
            value = next(flags, None)
            if value is not None:
                build_args.append(value)
        if value is None:
            sys.exit(f"{option} 缺少参数。")
        if option in ("-o", "--o"):
            output_override = value

with open("mygo.json", encoding="utf-8") as manifest:
    config = json.load(manifest)
version = config["version"]
slug = config.get("slug") or "xiaomu"
name = config.get("name") or "Xiaomu"
out = Path(os.path.normpath(output_override or config.get("out") or "dist"))

architectures = (
    ("arm64", "arm64"),
    ("amd64", "x86_64"),
)
targets = ",".join(f"darwin/{arch}" for arch, _ in architectures)
result = subprocess.run(
    ["go", "tool", "mygo", "build", "-platform", targets, "-skip-notarize", *build_args]
)
if result.returncode:
    sys.exit(result.returncode)

# MyGo v0.10.0 builds Xiaomu.app, the .dmg and the signed update archive, but no
# longer produces the manual-install macOS .zip automatically. Package each
# freshly signed app bundle with ditto so permissions, symlinks and signatures
# stay intact.
for arch, _ in architectures:
    platform_dir = out / f"darwin-{arch}"
    app_bundle = platform_dir / f"{name}.app"
    zip_path = platform_dir / f"{slug}-{version}-macos-{arch}.zip"
    if app_bundle.is_dir():
        if zip_path.exists():
            zip_path.unlink()
        zip_res = subprocess.run(
            ["ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(app_bundle), str(zip_path)]
        )
        if zip_res.returncode:
            sys.exit(zip_res.returncode)

if with_universal_bridge:
    # Transition bridge only: produce the signed universal update archive and
    # manifest (without DMG/ZIP) so legacy Universal users auto-update onto a
    # release whose updater switches them to arm64/amd64 on the next upgrade.
    bridge_res = subprocess.run(
        ["go", "tool", "mygo", "build", "-platform", "darwin/universal", "-skip-dmg", "-skip-notarize", *build_args]
    )
    if bridge_res.returncode:
        sys.exit(bridge_res.returncode)

missing = []
arch_errors = []
lipo_bin = shutil.which("lipo")

for arch, macho_arch in architectures:
    platform = f"darwin-{arch}"
    platform_dir = out / platform
    names = (
        f"{slug}-{version}-{arch}.dmg",
        f"{slug}-{version}-macos-{arch}.zip",
        f"{slug}-{version}-{platform}.tar.gz",
        f"update-{platform}.json",
    )
    for item in names:
        artifact = platform_dir / item
        if not artifact.is_file() or artifact.stat().st_size == 0:
            missing.append(str(artifact))

    if lipo_bin:
        for rel in (f"{name}.app/Contents/MacOS/{name}", f"{name}.app/Contents/Resources/libmpv.dylib"):
            binary = platform_dir / rel
            if binary.is_file():
                probe = subprocess.run([lipo_bin, "-archs", str(binary)], capture_output=True, text=True)
                got = probe.stdout.strip()
                if probe.returncode != 0 or got != macho_arch:
                    arch_errors.append(f"{binary}: 期望 {macho_arch}，实际 {got or probe.stderr.strip()}")

if with_universal_bridge:
    platform = "darwin-universal"
    platform_dir = out / platform
    for item in (f"{slug}-{version}-{platform}.tar.gz", f"update-{platform}.json"):
        artifact = platform_dir / item
        if not artifact.is_file() or artifact.stat().st_size == 0:
            missing.append(str(artifact))
    if lipo_bin:
        for rel in (f"{name}.app/Contents/MacOS/{name}", f"{name}.app/Contents/Resources/libmpv.dylib"):
            binary = platform_dir / rel
            if binary.is_file():
                probe = subprocess.run([lipo_bin, "-archs", str(binary)], capture_output=True, text=True)
                got = set(probe.stdout.strip().split())
                if probe.returncode != 0 or got != {"x86_64", "arm64"}:
                    arch_errors.append(f"{binary}: 期望 x86_64 arm64，实际 {probe.stdout.strip() or probe.stderr.strip()}")

if missing:
    sys.exit("发布构建不完整，以下产物缺失或为空：\n  " + "\n  ".join(missing))
if arch_errors:
    sys.exit("架构校验失败，产物包含非预期的二进制架构：\n  " + "\n  ".join(arch_errors))
PY
