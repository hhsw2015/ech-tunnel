#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)

# 自动取最新 tag 没有 tag 就用 v0.0.0
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0")

ldflags="\
-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version' \
"

mkdir -p build

# ========================================
# 下面全部保留 OpenList 原版函数写法 + 已修复所有路径问题
# ========================================

BuildWinArm64() {
  local output="$1"
  echo "building for windows-arm64"
  # 从 OpenList 官方仓库直接下载 wrapper（最稳定）
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64

  # 关键：使用绝对路径
  CC="$PWD/zcc-arm64" CXX="$PWD/zcxx-arm64" \
  GOOS=windows GOARCH=arm64 CGO_ENABLED=1 \
  go build -o "$output" -ldflags="$ldflags" .
}

BuildRelease() {
  echo "=== 主流平台：xgo 编译 ==="
  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest

  xgo \
    -go 1.25.x \
    -out "$appName" \
    -ldflags="$ldflags" \
    -targets=windows/amd64,darwin/amd64,darwin/arm64,linux/amd64,linux/arm64,linux/arm-7,linux/arm-6,linux/386,freebsd/amd64 \
    .

  mv "$appName"-* build/
  # upx 压缩（出错也无所谓）
  upx --best --lzma build/"$appName"-* 2>/dev/null || true

  # 单独补 windows-arm64（xgo 有时漏）
  BuildWinArm64 "build/${appName}-windows-arm64.exe"
}

BuildReleaseLinuxMusl() {
  echo "=== musl 静态链接：amd64 / arm64 / armv7 ==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross)

  for t in "${FILES[@]}"; do
    curl -fsSL "${BASE}${t}.tgz" | sudo tar -xz -C /usr/local --strip-components=1
  done

  local muslflags="--extldflags '-static' $ldflags"

  CC=x86_64-linux-musl-gcc      GOOS=linux GOARCH=amd64   CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-amd64"   -ldflags="$muslflags" .
  CC=aarch64-linux-musl-gcc     GOOS=linux GOARCH=arm64  CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-arm64"   -ldflags="$muslflags" .
  CC=armv7l-linux-musleabihf-gcc GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-armv7" -ldflags="$muslflags" .
}

BuildReleaseAndroid() {
  echo "=== Android 四个架构（已修复绝对路径问题）==="
  wget -q https://dl.google.com/android/repository/android-ndk-r26b-linux.zip
  unzip -q android-ndk-r26b-linux.zip
  rm android-ndk-r26b-linux.zip

  # 关键修复：使用 $PWD 变成绝对路径
  local NDK="$PWD/android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin"

  declare -A targets=(
    [amd64]=x86_64-linux-android24-clang
    [arm64]=aarch64-linux-android24-clang
    [386]=i686-linux-android24-clang
    [arm]=armv7a-linux-androideabi24-clang
  )

  for arch in "${!targets[@]}"; do
    local clang="${targets[$arch]}"
    local goarch="$arch"
    local goarm=""
    [ "$arch" = "arm" ] && goarm="7"

    echo "building android-$arch"
    CC="${NDK}/${clang}" \
    GOOS=android GOARCH=$goarch GOARM=$goarm CGO_ENABLED=1 \
    go build -o "build/${appName}-android-${arch}" -ldflags="$ldflags" .

    "${NDK}/llvm-strip" "build/${appName}-android-${arch}"
  done
}

MakeRelease() {
  echo "=== 打包 + 生成 SHA256SUMS ==="
  cd build

  # Linux / Darwin / FreeBSD → .tar.gz
  for f in ${appName}-linux-* ${appName}-darwin-* ${appName}-freebsd-*; do
    [ -f "$f" ] && tar -czf "${f}.tar.gz" "$f" && rm "$f"
  done

  # Windows → .zip
  for f in ${appName}-windows-*.exe; do
    [ -f "$f" ] && zip "${f}.zip" "$f" && rm "$f"
  done

  # Android 直接保留裸文件（习惯）
  # 如需压缩可去掉下面这行注释
  # for f in ${appName}-android-*; do [ -f "$f" ] && gzip "$f"; done

  sha256sum * > SHA256SUMS.txt
  echo "全部完成！共 $(ls -1 | wc -l) 个文件"
  ls -lh
}

# ========================================
# 主入口（只保留 release 模式）
# ========================================

if [[ "$1" == "release" ]] || [[ -z "$1" ]]; then
  BuildRelease
  BuildReleaseLinuxMusl
  BuildReleaseAndroid
  MakeRelease
else
  echo "ech-tunnel 全平台构建成功！"
  echo "输出目录：$(pwd)/build"
else
  echo "用法: bash build.sh [release]"
  echo "已自动为你执行 release 模式"
  exec bash "$0" release
fi
