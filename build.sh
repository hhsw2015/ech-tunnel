#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)

# 自动获取最新 tag 作为版本号
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0")

ldflags="\
-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version' \
"

mkdir -p build

# ============ 下面全部保留 OpenList 原版函数结构 ============

BuildRelease() {
  echo "=== 开始编译主流平台（xgo）==="
  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest

  xgo \
    -go 1.25.x \
    -out "$appName" \
    -ldflags="$ldflags" \
    -targets=windows/amd64,windows/arm64,darwin/amd64,darwin/arm64,linux/amd64,linux/arm64,linux/arm-7,linux/arm-6,linux/386,freebsd/amd64 \
    .

  mv "$appName"-* build/
  upx --best --lzma build/"$appName"-* 2>/dev/null || true
}

BuildWinArm64() {
  echo "building for windows-arm64"
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64

  export GOOS=windows
  export GOARCH=arm64
  export CC=$(pwd)/zcc-arm64
  export CXX=$(pwd)/zcxx-arm64
  export CGO_ENABLED=1
  go build -o "build/${appName}-windows-arm64.exe" -ldflags="$ldflags" .
}

BuildReleaseLinuxMusl() {
  echo "=== 编译 musl 静态版（amd64 / arm64 / armv7）==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross)

  for i in "${FILES[@]}"; do
    curl -fsSL -o "${i}.tgz" "${BASE}${i}.tgz"
    sudo tar xf "${i}.tgz" --strip-components=1 -C /usr/local
    rm -f "${i}.tgz"
  done

  local muslflags="--extldflags '-static' $ldflags"

  # amd64 musl
  CC=x86_64-linux-musl-gcc   GOOS=linux GOARCH=amd64   CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-amd64"   -ldflags="$muslflags" .

  # arm64 musl
  CC=aarch64-linux-musl-gcc  GOOS=linux GOARCH=arm64  CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-arm64"   -ldflags="$muslflags" .

  # armv7 musl
  CC=armv7l-linux-musleabihf-gcc GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-armv7" -ldflags="$muslflags" .
}

BuildReleaseAndroid() {
  echo "=== 编译 Android 四个架构 ==="
  wget -q https://dl.google.com/android/repository/android-ndk-r26b-linux.zip
  unzip -q android-ndk-r26b-linux.zip
  rm android-ndk-r26b-linux.zip

  declare -A arches=(
    [amd64]=x86_64-linux-android24-clang
    [arm64]=aarch64-linux-android24-clang
    [386]=i686-linux-android24-clang
    [arm]=armv7a-linux-androideabi24-clang
  )

  for arch in "${!arches[@]}"; do
    clang="${arches[$arch]}"
    goarch=$arch
    goarm=""
    [ "$arch" = "arm" ] && goarm="7"

    CC="./android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin/${clang}" \
    GOOS=android GOARCH=$goarch GOARM=$goarm CGO_ENABLED=1 \
    go build -o "build/${appName}-android-${arch}" -ldflags="$ldflags" .

    ./android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip "build/${appName}-android-${arch}"
  done
}

MakeRelease() {
  echo "=== 打包 + 生成 SHA256SUMS ==="
  cd build

  # Linux / Darwin / FreeBSD → tar.gz
  for f in ${appName}-linux-* ${appName}-darwin-* ${appName}-freebsd-*; do
    [ -f "$f" ] && tar -czf "${f}.tar.gz" "$f" && rm "$f"
  done

  # Windows → zip
  for f in ${appName}-windows-*; do
    [ -f "$f" ] && zip "${f}.zip" "$f" && rm "$f"
  done

  # Android 直接保留裸二进制（习惯如此）
  # 如需压缩可取消下一行注释
  # for f in ${appName}-android-*; do [ -f "$f" ] && gzip "$f"; done

  sha256sum * > SHA256SUMS.txt
  echo "=== 全部完成！输出目录：$(pwd) ==="
  ls -lh
}

# ============ 主逻辑（完全模仿 OpenList 的调用方式） ============

if [ "$1" = "release" ] || [ -z "$1" ]; then
  BuildRelease          # xgo 主流平台 + windows-arm64
  BuildWinArm64         # 补 windows-arm64（xgo 有时会漏）
  BuildReleaseLinuxMusl # musl 静态三件套
  BuildReleaseAndroid   # Android 四件套
  MakeRelease
  echo "ech-tunnel 全平台编译完成！共 $(ls build | wc -l) 个文件"
else
  echo "用法: bash build.sh [release]"
  echo "当前仅支持 release 模式，已为你自动执行"
  bash "$0" release
fi
