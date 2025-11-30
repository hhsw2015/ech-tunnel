#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)

# 自动获取最新 tag
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0")

ldflags="\
-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version' \
"

mkdir -p build

# ============ 下面全部保留 OpenList 原版函数写法，已修复所有路径问题 ============

BuildWinArm64() {
  local output="$1"
  echo "building for windows-arm64"
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64

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
  upx --best --lzma build/"$appName"-* 2>/dev/null || true

  # 补 windows-arm64
  BuildWinArm64 "build/${appName}-windows-arm64.exe"
}

BuildReleaseLinuxMusl() {
  echo "=== musl 静态版：amd64 / arm64 / armv7 ==="
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
  echo "=== Android 四个架构（已彻底修复路径问题）==="
  wget -q https://dl.google.com/android/repository/android-ndk-r26b-linux.zip
  unzip -q android-ndk-r26b-linux.zip
  rm android-ndk-r26b-linux.zip

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

  for f in ${appName}-linux-* ${appName}-darwin-* ${appName}-freebsd-*; do
    [ -f "$f" ] && tar -czf "${f}.tar.gz" "$f" && rm "$f"
  done

  for f in ${appName}-windows-*.exe; do
    [ -f "$f" ] && zip "${f}.zip" "$f" && rm "$f"
  done

  sha256sum * > SHA256SUMS.txt
  echo "全部完成！共 $(ls -1 | wc -l) 个文件"
  ls -lh
}

# ============ 主入口 ============

if [[ "$1" == "release" ]] || [[ -z "$1" ]]; then
  BuildRelease
  BuildReleaseLinuxMusl
  BuildReleaseAndroid
  MakeRelease
  echo "ech-tunnel 全平台构建成功！"
else
  echo "用法: bash build.sh [release]"
  echo "已自动执行 release 模式"
  bash "$0" release
fi
