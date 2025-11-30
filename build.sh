#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0.0")

ldflags="\
-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version' \
"

# 关键修复：安全的 curl 封装（OpenList 2025 年现行标准写法）
curl()   { command curl -fsSL --retry 5 --retry-delay 3 "$@"; }
gh_curl(){ 
  if [ -n "$GITHUB_TOKEN" ] && [ "$GITHUB_TOKEN" != "null" ]; then
    curl -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
  else
    curl "$@"
  fi
}

# 删除原来的 githubAuthArgs（彻底杜绝 Bearer 错误）
# githubAuthArgs=""

# ====================== OpenList 原版函数（只改 curl 调用方式）======================

BuildWinArm64() {
  echo "building for windows-arm64"
  chmod +x ./wrapper/zcc-arm64 2>/dev/null || true
  chmod +x ./wrapper/zcxx-arm64 2>/dev/null || true
  export GOOS=windows GOARCH=arm64 CC=$(pwd)/wrapper/zcc-arm64 CXX=$(pwd)/wrapper/zcxx-arm64 CGO_ENABLED=1
  go build -o "$1" -ldflags="$ldflags" .
}

BuildWin7() {
  go_version=$(go version | grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' | sed 's/go//')
  echo "Detected Go version: $go_version"
  # 修复：用 gh_curl 替代 $githubAuthArgs
  gh_curl -fsSL --retry 3 -o go-win7.zip \
    "https://github.com/XTLS/go-win7/releases/download/patched-${go_version}/go-for-win7-linux-amd64.zip"
  rm -rf go-win7 && unzip -q go-win7.zip -d go-win7 && rm go-win7.zip
  chmod +x ./wrapper/zcc-win7* ./wrapper/zcxx-win7* 2>/dev/null || true
  for arch in "386" "amd64"; do
    echo "building for windows7-${arch}"
    export GOOS=windows GOARCH=$arch CGO_ENABLED=1
    if [ "$arch" = "386" ]; then
      export CC=$(pwd)/wrapper/zcc-win7-386 CXX=$(pwd)/wrapper/zcxx-win7-386
    else
      export CC=$(pwd)/wrapper/zcc-win7 CXX=$(pwd)/wrapper/zcc-win7
    fi
    $(pwd)/go-win7/bin/go build -o "${1}-${arch}.exe" -ldflags="$ldflags" .
  done
}

BuildLoongGLIBC() {
  local target_abi="$2"
  local output_file="$1"
  local oldWorldGoVersion="1.25.0"

  if [ "$target_abi" = "abi1.0" ]; then
    echo "building for linux-loong64-abi1.0"
  else
    echo "building for linux-loong64-abi2.0"
    target_abi="abi2.0"
  fi

  if [ "$target_abi" = "abi1.0" ]; then
    # 修复：全部换成 gh_curl
    gh_curl -fsSL --retry 3 \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250821/go${oldWorldGoVersion}.linux-amd64.tar.gz" \
      -o go-loong64-abi1.0.tar.gz
    rm -rf go-loong64-abi1.0 && mkdir go-loong64-abi1.0
    tar -xzf go-loong64-abi1.0.tar.gz -C go-loong64-abi1.0 --strip-components=1
    rm go-loong64-abi1.0.tar.gz

    gh_curl -fsSL --retry 3 \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250722/loongson-gnu-toolchain-8.3.novec-x86_64-loongarch64-linux-gnu-rc1.1.tar.xz" \
      -o gcc8-loong64-abi1.0.tar.xz
    rm -rf gcc8-loong64-abi1.0 && mkdir gcc8-loong64-abi1.0
    tar -Jxf gcc8-loong64-abi1.0.tar.xz -C gcc8-loong64-abi1.0 --strip-components=1
    rm gcc8-loong64-abi1.0.tar.xz

    local cache_dir="$(pwd)/go-loong64-abi1.0-cache"
    mkdir -p "$cache_dir"
    env GOOS=linux GOARCH=loong64 \
        CC="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc" \
        CXX="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-g++" \
        CGO_ENABLED=1 GOCACHE="$cache_dir" \
        $(pwd)/go-loong64-abi1.0/bin/go build -a -o "$output_file" -ldflags="$ldflags" .
  else
    # 修复：abi2.0 也换成 gh_curl
    gh_curl -fsSL --retry 3 \
      "https://github.com/loong64/cross-tools/releases/download/20250507/x86_64-cross-tools-loongarch64-unknown-linux-gnu-legacy.tar.xz" \
      -o gcc12-loong64-abi2.0.tar.xz
    rm -rf gcc12-loong64-abi2.0 && mkdir gcc12-loong64-abi2.0
    tar -Jxf gcc12-loong64-abi2.0.tar.xz -C gcc12-loong64-abi2.0 --strip-components=1
    rm gcc12-loong64-abi2.0.tar.xz

    CC=$(pwd)/gcc12-loong64-abi2.0/bin/loongarch64-unknown-linux-gnu-gcc \
    CXX=$(pwd)/gcc12-loong64-abi2.0/bin/loongarch64-unknown-linux-gnu-g++ \
    GOOS=linux GOARCH=loong64 CGO_ENABLED=1 \
      go build -a -o "$output_file" -ldflags="$ldflags" .
  fi
}

# 下面所有函数保持 100% 原样，只是把原来的 $githubAuthArgs 替换成 gh_curl
#（FreeBSD 那段也改一下）
BuildReleaseFreeBSD() {
  mkdir -p "build"
  freebsd_version=$(gh_curl -fsSL --max-time 10 "https://api.github.com/repos/freebsd/freebsd-src/tags" | \
    jq -r '.[].name' | grep '^release/14\.' | grep -v -- '-p[0-9]*$' | sort -V | tail -1 | sed 's/release\///;s/\.0$//')
  [ -z "$freebsd_version" ] && freebsd_version="14.3"
  echo "Using FreeBSD version: $freebsd_version"
  local OS_ARCHES=(amd64 arm64 i386)
  local GO_ARCHES=(amd64 arm64 386)
  local TARGETS=(x86_64-unknown-freebsd${freebsd_version} aarch64-unknown-freebsd${freebsd_version} i386-unknown-freebsd${freebsd_version})
  for i in "${!OS_ARCHES[@]}"; do
    sudo mkdir -p "/opt/freebsd/${OS_ARCHES[$i]}"
    wget -q "https://download.freebsd.org/releases/${OS_ARCHES[$i]}/${freebsd_version}-RELEASE/base.txz"
    sudo tar -xf base.txz -C "/opt/freebsd/${OS_ARCHES[$i]}"
    rm base.txz
    export GOOS=freebsd GOARCH=${GO_ARCHES[$i]} CC="clang --target=${TARGETS[$i]} --sysroot=/opt/freebsd/${OS_ARCHES[$i]}" CGO_ENABLED=1 CGO_LDFLAGS="-fuse-ld=lld"
    echo "building for freebsd-${OS_ARCHES[$i]}"
    go build -o "./build/$appName-freebsd-${OS_ARCHES[$i]}" -ldflags="$ldflags" .
  done
}

# 其余函数（musl、android 等）全部保持你原来的代码不变（它们本来就不用 token）

# ====================== 主入口保持不变 ======================
BuildReleaseAll() {
  rm -rf build .git && mkdir -p build

  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest
  xgo -out "$appName" -ldflags="$ldflags" \
    -targets=windows/amd64,windows/386,darwin/amd64,darwin/arm64,\
linux/amd64,linux/386,linux/arm64,linux/arm-7,linux/arm-6,linux/arm-5,linux/s390x,linux/ppc64le,linux/riscv64 \
    -go 1.25.x .

  mv "$appName"-* build/

  BuildWinArm64 build/"$appName"-windows-arm64.exe
  BuildWin7 build/"$appName"-windows7
  BuildLoongGLIBC build/"$appName"-linux-loong64-abi1.0 abi1.0
  BuildLoongGLIBC build/"$appName"-linux-loong64 abi2.0

  BuildReleaseLinuxMusl
  BuildReleaseLinuxMuslArm
  BuildReleaseAndroid
  BuildReleaseFreeBSD
}

MakeRelease() {
  cd build
  rm -rf compress && mkdir compress
  for i in $(find . -type f -name "$appName-linux-*"); do
    cp "$i" "$appName" && tar -czf "compress/$(basename $i).tar.gz" "$appName" && rm "$appName"
  done
  for i in $(find . -type f -name "$appName-android-*"); do
    cp "$i" "$appName" && tar -czf "compress/$(basename $i).tar.gz" "$appName" && rm "$appName"
  done
  for i in $(find . -type f -name "$appName-darwin-*"); do
    cp "$i" "$appName" && tar -czf "compress/$(basename $i).tar.gz" "$appName" && rm "$appName"
  done
  for i in $(find . -type f -name "$appName-freebsd-*"); do
    cp "$i" "$appName" && tar -czf "compress/$(basename $i).tar.gz" "$appName" && rm "$appName"
  done
  for i in $(find . -type f \( -name "$appName-windows-*" -o -name "$appName-windows7-*" \)); do
    cp "$i" "$appName.exe" && zip "compress/$(basename $i .exe).zip" "$appName.exe" && rm "$appName.exe"
  done
  cd compress
  sha256sum * > SHA256SUMS.txt
  echo "ech-tunnel 全平台构建完成！共 $(ls -1 | grep -E '\.(tar\.gz|zip)$' | wc -l) 个文件"
  ls -lh
}

case "$1" in
  release)
    BuildReleaseAll
    MakeRelease
    ;;
  *)
    echo "用法: $0 release"
    exit 1
    ;;
esac
