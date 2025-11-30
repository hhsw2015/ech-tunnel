#!/usr/bin/env bash
set -euo pipefail

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0.0")

ldflags="-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version'"

# 安全 curl，彻底杜绝 "Could not resolve host: Bearer"
curl()   { command curl -fsSL --retry 5 --retry-delay 3 "$@"; }
gh_curl(){
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
  else
    curl "$@"
  fi
}

mkdir -p build

# ====================== 所有函数（OpenList 生产环境真实使用版）======================

BuildWinArm64() {
  local output="$1"
  echo "building for windows-arm64 → $output"
  curl -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64
  CC="$PWD/zcc-arm64" CXX="$PWD/zcxx-arm64" \
    GOOS=windows GOARCH=arm64 CGO_ENABLED=1 \
    go build -o "$output" -ldflags="$ldflags" .
}

BuildWin7() {
  local prefix="$1"
  go_version=$(go version | grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' | sed 's/go//')
  echo "building windows7 (detected go$go_version)"
  gh_curl -fsSL -o go-win7.zip \
    "https://github.com/XTLS/go-win7/releases/download/patched-${go_version}/go-for-win7-linux-amd64.zip"
  rm -rf go-win7 && unzip -q go-win7.zip -d go-win7 && rm go-win7.zip
  chmod +x ./wrapper/zcc-win7* ./wrapper/zcxx-win7* 2>/dev/null || true

  for arch in 386 amd64; do
    if [ "$arch" = "386" ]; then
      CC="$PWD/wrapper/zcc-win7-386" CXX="$PWD/wrapper/zcxx-win7-386"
    else
      CC="$PWD/wrapper/zcc-win7" CXX="$PWD/wrapper/zcxx-win7"
    fi
    GOOS=windows GOARCH=$arch CC="$CC" CXX="$CXX" CGO_ENABLED=1 \
      "$PWD/go-win7/bin/go" build -o "${prefix}-${arch}.exe" -ldflags="$ldflags" .
  done
}

BuildReleaseLinuxMusl() {
  local muslflags="--extldflags '-static -fpic' $ldflags"
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross mips-linux-musl-cross mips64-linux-musl-cross mips64el-linux-musl-cross mipsel-linux-musl-cross powerpc64le-linux-musl-cross s390x-linux-musl-cross loongarch64-linux-musl-cross)
  for f in "${FILES[@]}"; do curl -o "${f}.tgz" "${BASE}${f}.tgz" && sudo tar xf "${f}.tgz" --strip-components=1 -C /usr/local && rm "${f}.tgz"; done

  local OS_ARCHES=(linux-musl-amd64 linux-musl-arm64 linux-musl-mips linux-musl-mips64 linux-musl-mips64le linux-musl-mipsle linux-musl-ppc64le linux-musl-s390x linux-musl-loong64)
  local CGO_ARGS=(x86_64-linux-musl-gcc aarch64-linux-musl-gcc mips-linux-musl-gcc mips64-linux-musl-gcc mips64el-linux-musl-gcc mipsel-linux-musl-gcc powerpc64le-linux-musl-gcc s390x-linux-musl-gcc loongarch64-linux-musl-gcc)

  for i in "${!OS_ARCHES[@]}"; do
    export GOOS=${OS_ARCHES[$i]%%-*} GOARCH=${OS_ARCHES[$i]##*-} CC="${CGO_ARGS[$i]}" CGO_ENABLED=1
    echo "building ${OS_ARCHES[$i]}"
    go build -o "build/$appName-${OS_ARCHES[$i]}" -ldflags="$muslflags" .
  done
}

BuildReleaseLinuxMuslArm() {
  local muslflags="--extldflags '-static -fpic' $ldflags"
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(arm-linux-musleabi-cross arm-linux-musleabihf-cross armel-linux-musleabi-cross armel-linux-musleabihf-cross armv5l-linux-musleabi-cross armv5l-linux-musleabihf-cross armv6-linux-musleabi-cross armv6-linux-musleabihf-cross armv7l-linux-musleabihf-cross armv7m-linux-musleabi-cross armv7r-linux-musleabihf-cross)
  for f in "${FILES[@]}"; do curl -o "${f}.tgz" "${BASE}${f}.tgz" && sudo tar xf "${f}.tgz" --strip-components=1 -C /usr/local && rm "${f}.tgz"; done

  local OS_ARCHES=(linux-musleabi-arm linux-musleabihf-arm linux-musleabi-armel linux-musleabihf-armel linux-musleabi-armv5l linux-musleabihf-armv5l linux-musleabi-armv6 linux-musleabihf-armv6 linux-musleabihf-armv7l linux-musleabi-armv7m linux-musleabihf-armv7r)
  local CGO_ARGS=(
    arm-linux-musleabi-gcc
    arm-linux-musleabihf-gcc
    armel-linux-musleabi-gcc
    armel-linux-musleabihf-gcc
    armv5l-linux-musleabi-gcc
    armv5l-linux-musleabihf-gcc
    armv6-linux-musleabi-gcc
    armv6-linux-musleabihf-gcc
    armv7l-linux-musleabihf-gcc
    armv7m-linux-musleabi-gcc
    armv7r-linux-musleabihf-gcc
  )
  local GOARMS=('' '' '' '' '5' '5' '6' '6' '7' '7' '7')

  for i in "${!OS_ARCHES[@]}"; do
    export GOOS=linux GOARCH=arm CC="${CGO_ARGS[$i]}" CGO_ENABLED=1 GOARM="${GOARMS[$i]}"
    echo "building ${OS_ARCHES[$i]}"
    go build -o "build/$appName-${OS_ARCHES[$i]}" -ldflags="$muslflags" .
  done
}

BuildReleaseAndroid() {
  wget -q https://dl.google.com/android/repository/android-ndk-r26d-linux.zip
  unzip -q android-ndk-r26d-linux.zip && rm android-ndk-r26d-linux.zip
  local NDK="android-ndk-r26d/toolchains/llvm/prebuilt/linux-x86_64/bin"
  local arches=(amd64 arm64 386 arm)
  local clangs=(x86_64-linux-android24-clang aarch64-linux-android24-clang i686-linux-android24-clang armv7a-linux-androideabi24-clang)

  for i in "${!arches[@]}"; do
    echo "building android-${arches[$i]}"
    export GOOS=android GOARCH="${arches[$i]}" CC="$NDK/${clangs[$i]}" CGO_ENABLED=1
    [ "${arches[$i]}" = "arm" ] && export GOARCH=arm GOARM=7
    go build -o "build/$appName-android-${arches[$i]}" -ldflags="$ldflags" .
  done
}

BuildReleaseFreeBSD() {
  local ver=$(gh_curl -fsSL https://api.github.com/repos/freebsd/freebsd-src/tags |
              jq -r '.[].name' | grep '^release/14\.' | grep -v -- '-p[0-9]*$' | sort -V | tail -1 | cut -d/ -f2)
  [ -z "$ver" ] && ver="14.3"
  echo "Using FreeBSD $ver"

  for arch in amd64 arm64 i386; do
    sudo mkdir -p "/opt/freebsd/$arch"
    wget -q "https://download.freebsd.org/releases/$arch/$ver-RELEASE/base.txz"
    sudo tar -xf base.txz -C "/opt/freebsd/$arch"
    rm base.txz

    local target triple
    case "$arch" in
      amd64)  triple="x86_64-unknown-freebsd$ver" ;;
      arm64)  triple="aarch64-unknown-freebsd$ver" ;;
      i386)   triple="i386-unknown-freebsd$ver" ;;
    esac

    GOOS=freebsd GOARCH=$(echo "$arch" | sed 's/amd64/amd64/;s/i386/386/;s/arm64/arm64/') \
      CC="clang --target=$triple --sysroot=/opt/freebsd/$arch" CGO_ENABLED=1 CGO_LDFLAGS="-fuse-ld=lld" \
      go build -o "build/$appName-freebsd-$arch" -ldflags="$ldflags" .
  done
}

BuildLoongGLIBC() {
  local output="$1" abi="${2:-abi2.0}"

  if [ "$abi" = "abi1.0" ]; then
    echo "building linux-loong64-abi1.0"
    gh_curl -fsSL -o go.tar.gz https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250821/go1.25.0.linux-amd64.tar.gz
    gh_curl -fsSL -o gcc.tar.xz https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250722/loongson-gnu-toolchain-8.3.novec-x86_64-loongarch64-linux-gnu-rc1.1.tar.xz
    rm -rf go-loong64-abi1.0 gcc8-loong64-abi1.0
    mkdir -p go-loong64-abi1.0 gcc8-loong64-abi1.0
    tar -xzf go.tar.gz -C go-loong64-abi1.0 --strip-components=1 && rm go.tar.gz
    tar -Jxf gcc.tar.xz -C gcc8-loong64-abi1.0 --strip-components=1 && rm gcc.tar.xz
    env GOOS=linux GOARCH=loong64 CC="$PWD/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc" CGO_ENABLED=1 \
      go build -a -o "$output" -ldflags="$ldflags" .
  else
    echo "building linux-loong64 (abi2.0)"
    gh_curl -fsSL -o gcc.tar.xz https://github.com/loong64/cross-tools/releases/download/20250507/x86_64-cross-tools-loongarch64-unknown-linux-gnu-legacy.tar.xz
    rm -rf gcc12-loong64 && mkdir -p gcc12-loong64
    tar -Jxf gcc.tar.xz -C gcc12-loong64 --strip-components=1 && rm gcc.tar.xz
    CC="$PWD/gcc12-loong64/bin/loongarch64-unknown-linux-gnu-gcc" \
    CXX="$PWD/gcc12-loong64/bin/loongarch64-unknown-linux-gnu-g++" \
    GOOS=linux GOARCH=loong64 CGO_ENABLED=1 \
      go build -a -o "$output" -ldflags="$ldflags" .
  fi
}

MakeRelease() {
  cd build
  rm -rf compress && mkdir compress
  for f in $appName-linux-* $appName-android-* $appName-darwin-* $appName-freebsd-*; do
    [ -f "$f" ] && tar -czf "compress/${f}.tar.gz" "$f"
  done
  for f in $appName-windows-*.exe; do
    [ -f "$f" ] && zip "compress/$(basename "$f" .exe).zip" "$f"
  done
  cd compress
  sha256sum * > SHA256SUMS.txt
  echo "ech-tunnel 全平台构建完成！共 $(ls -1 *.tar.gz *.zip 2>/dev/null | wc -l) 个文件"
}

# ====================== 主流程：一个都不漏 ======================
BuildReleaseAll() {
  rm -rf build && mkdir -p build

  # xgo 主力平台
  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest
  xgo -out "$appName" -ldflags="$ldflags" \
    -targets=windows/amd64,windows/386,darwin/amd64,darwin/arm64,\
linux/amd64,linux/386,linux/arm64,linux/arm-7,linux/arm-6,linux/arm-5,linux/s390x,linux/ppc64le,linux/riscv64,\
freebsd/amd64,freebsd/arm64,freebsd/386 \
    -go 1.25.x .
  mv "$appName"-* build/

  # 下面 8 个一个都不能少！
  BuildWinArm64          "build/$appName-windows-arm64.exe"
  BuildWin7              "build/$appName-windows7"
  BuildLoongGLIBC        "build/$appName-linux-loong64-abi1.0" abi1.0
  BuildLoongGLIBC        "build/$appName-linux-loong64"         abi2.0
  BuildReleaseLinuxMusl
  BuildReleaseLinuxMuslArm
  BuildReleaseAndroid
  BuildReleaseFreeBSD
}

# ====================== 入口 ======================
case "${1:-}" in
  release)
    BuildReleaseAll
    MakeRelease
    ;;
  *)
    echo "用法: $0 release"
    exit 1
    ;;
esac
