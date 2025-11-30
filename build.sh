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

# 关键修复：彻底消灭 "Could not resolve host: Bearer"
curl()   { command curl -fsSL --retry 5 --retry-delay 3 "$@"; }
gh_curl(){
  if [ -n "$GITHUB_TOKEN" ] && [ "$GITHUB_TOKEN" != "null" ]; then
    curl -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
  else
    curl "$@"
  fi
}

# ====================== 所有 OpenList 原版函数（完整保留 + 只改 curl）======================

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
    gh_curl -fsSL --retry 3 \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250821/go${oldWorldGoVersion}.linux-amd64.tar.gz" \
      -o go-loong64-abi1.0.tar.gz
    rm -rf go-loong64-abi1.0 && mkdir go-loong64-abi1.0          # 修复这里！
    tar -xzf go-loong64-abi1.0.tar.gz -C go-loong64-abi1.0 --strip-components=1
    rm go-loong64-abi1.0.tar.gz

    gh_curl -fsSL --retry 3 \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250722/loongson-gnu-toolchain-8.3.novec-x86_64-loongarch64-linux-gnu-rc1.1.tar.xz" \
      -o gcc8-loong64-abi1.0.tar.xz
    rm -rf gcc8-loong64-abi1.0 && mkdir gcc8-loong64-abi1.0      # 这里也保险起见写全
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
    echo "building for linux-loong64-abi2.0"
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

# 下面三个你最关心的函数 —— 完整保留，零修改
BuildReleaseLinuxMusl() {
  mkdir -p "build"
  local muslflags="--extldflags '-static -fpic' $ldflags"
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross mips-linux-musl-cross mips64-linux-musl-cross mips64el-linux-musl-cross mipsel-linux-musl-cross powerpc64le-linux-musl-cross s390x-linux-musl-cross loongarch64-linux-musl-cross)
  for i in "${FILES[@]}"; do curl -fsSL -o "${i}.tgz" "${BASE}${i}.tgz" && sudo tar xf "${i}.tgz" --strip-components=1 -C /usr/local && rm -f "${i}.tgz"; done
  local OS_ARCHES=(linux-musl-amd64 linux-musl-arm64 linux-musl-mips linux-musl-mips64 linux-musl-mips64le linux-musl-mipsle linux-musl-ppc64le linux-musl-s390x linux-musl-loong64)
  local CGO_ARGS=(x86_64-linux-musl-gcc aarch64-linux-musl-gcc mips-linux-musl-gcc mips64-linux-musl-gcc mips64el-linux-musl-gcc mipsel-linux-musl-gcc powerpc64le-linux-musl-gcc s390x-linux-musl-gcc loongarch64-linux-musl-gcc)
  for i in "${!OS_ARCHES[@]}"; do
    export GOOS=${OS_ARCHES[$i]%%-*} GOARCH=${OS_ARCHES[$i]##*-} CC=${CGO_ARGS[$i]} CGO_ENABLED=1
    echo "building for ${OS_ARCHES[$i]}"
    go build -o "./build/$appName-${OS_ARCHES[$i]}" -ldflags="$muslflags" .
  done
}

BuildReleaseLinuxMuslArm() {
  mkdir -p "build"
  local muslflags="--extldflags '-static -fpic' $ldflags"
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local FILES=(arm-linux-musleabi-cross arm-linux-musleabihf-cross armel-linux-musleabi-cross armel-linux-musleabihf-cross armv5l-linux-musleabi-cross armv5l-linux-musleabihf-cross armv6-linux-musleabi-cross armv6-linux-musleabihf-cross armv7l-linux-musleabihf-cross armv7m-linux-musleabi-cross armv7r-linux-musleabihf-cross)
  for i in "${FILES[@]}"; do curl -fsSL -o "${i}.tgz" "${BASE}${i}.tgz" && sudo tar xf "${i}.tgz" --strip-components=1 -C /usr/local && rm -f "${i}.tgz"; done
  local OS_ARCHES=(linux-musleabi-arm linux-musleabihf-arm linux-musleabi-armel linux-musleabihf-armel linux-musleabi-armv5l linux-musleabihf-armv5l linux-musleabi-armv6 linux-musleabihf-armv6 linux-musleabihf-armv7l linux-musleabi-armv7m linux-musleabihf-armv7r)
  local CGO_ARGS=(arm-linux-musleabi-gcc arm-linux-musleabihf-gcc armel,armel-linux-musleabi-gcc armel-linux-musleabihf-gcc armv5l-linux-musleabi-gcc armv5l-linux-musleabihf-gcc armv6-linux-musleabi-gcc armv6-linux-musleabihf-gcc armv7l-linux-musleabihf-gcc armv7m-linux-musleabi-gcc armv7r-linux-musleabihf-gcc)
  local GOARMS=('' '' '' '' '5' '5' '6' '6' '7' '7' '7')
  for i in "${!OS_ARCHES[@]}"; do
    export GOOS=linux GOARCH=arm CC=${CGO_ARGS[$i]} CGO_ENABLED=1 GOARM=${GOARMS[$i]}
    echo "building for ${OS_ARCHES[$i]}"
    go build -o "./build/$appName-${OS_ARCHES[$i]}" -ldflags="$muslflags" .
  done
}

BuildReleaseAndroid() {
  mkdir -p "build"
  wget -q https://dl.google.com/android/repository/android-ndk-r26b-linux.zip
  unzip -q android-ndk-r26b-linux.zip && rm android-ndk-r26b-linux.zip
  local NDK="android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin"
  local arches=(amd64 arm64 386 arm)
  local clangs=(x86_64-linux-android24-clang aarch64-linux-android24-clang i686-linux-android24-clang armv7a-linux-androideabi24-clang)
  for i in "${!arches[@]}"; do
    echo "building for android-${arches[$i]}"
    export GOOS=android GOARCH=${arches[$i]} CC="$NDK/${clangs[$i]}" CGO_ENABLED=1
    [ "${arches[$i]}" = "arm" ] && export GOARCH=arm GOARM=7
    go build -o "./build/$appName-android-${arches[$i]}" -ldflags="$ldflags" .
    "$NDK/llvm-strip" "./build/$appName-android-${arches[$i]}" 2>/dev/null || true
  done
}

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
