#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0")

ldflags="\
-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version' \
"

mkdir -p build/compress

# ========================================
# 1. 主流平台（xgo + windows-arm64）
# ========================================
BuildMain() {
  echo "=== 编译主流平台（xgo + windows-arm64）==="
  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest

  xgo -go 1.25.x -out "$appName" -ldflags="$ldflags" \
    -targets=windows/amd64,darwin/amd64,darwin/arm64,linux/amd64,linux/arm64,linux/arm-7,linux/arm-6,linux/386,freebsd/amd64 .

  mv "$appName"-* build/
  upx --best --lzma build/"$appName"-* 2>/dev/null || true

  # windows-arm64（xgo 有时漏）
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64
  CC="$PWD/zcc-arm64" CXX="$PWD/zcxx-arm64" \
    GOOS=windows GOARCH=arm64 CGO_ENABLED=1 \
    go build -o "build/${appName}-windows-arm64.exe" -ldflags="$ldflags" .
}

# ========================================
# 2. 常用 musl 静态平台（amd64 / arm64 / armv7）
# ========================================
BuildMuslCommon() {
  echo "=== 编译常用 musl 静态平台 ==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/v1/"
  local tools=(x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross)

  for t in "${tools[@]}"; do
    curl -fsSL "${BASE}${t}.tgz" | sudo tar -xz -C /usr/local --strip-components=1
  done

  local muslflags="--extldflags '-static' $ldflags"
  CC=x86_64-linux-musl-gcc           GOOS=linux GOARCH=amd64   CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-amd64"     -ldflags="$muslflags" .
  CC=aarch64-linux-musl-gcc          GOOS=linux GOARCH=arm64  CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-arm64"     -ldflags="$muslflags" .
  CC=armv7l-linux-musleabihf-gcc     GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-armv7" -ldflags="$muslflags" .
}

# ========================================
# 3. 全部冷门 musl 平台（mips / loong64 / riscv64 / ppc64le）
# ========================================
BuildMuslAll() {
  echo "=== 编译全部冷门 musl 平台（mips / loong64 / riscv64 / ppc64le 等）==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/v1/"
  local tools=(
    mips-linux-musl-cross mipsel-linux-musl-cross
    mips64-linux-musl-cross mips64el-linux-musl-cross
    riscv64-linux-musl-cross powerpc64le-linux-musl-cross loongarch64-linux-musl-cross
  )

  for t in "${tools[@]}"; do
    echo "正在安装 $t ..."
    curl -fsSL "${BASE}${t}.tgz" | sudo tar -xz -C /usr/local --strip-components=1
  done

  local muslflags="--extldflags '-static' $ldflags"

  # mips 系列（必须加 softfloat）
  CC=mips-linux-musl-gcc       GOOS=linux GOARCH=mips    GO_MIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips"     -ldflags="$muslflags" .
  CC=mipsel-linux-musl-gcc     GOOS=linux GOARCH=mipsle  GO_MIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mipsle"   -ldflags="$muslflags" .
  CC=mips64-linux-musl-gcc     GOOS=linux GOARCH=mips64  CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64"   -ldflags="$muslflags" .
  CC=mips64el-linux-musl-gcc   GOOS=linux GOARCH=mips64le CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64le" -ldflags="$muslflags" .

  # 其他冷门
  CC=riscv64-linux-musl-gcc        GOOS=linux GOARCH=riscv64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-riscv64"  -ldflags="$muslflags" .
  CC=powerpc64le-linux-musl-gcc    GOOS=linux GOARCH=ppc64le CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-ppc64le"  -ldflags="$muslflags" .
  CC=loongarch64-linux-musl-gcc    GOOS=linux GOARCH=loong64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-loong64" -ldflags="$muslflags" .
}

# ========================================
# 4. Android 四个架构
# ========================================
BuildAndroid() {
  echo "=== 编译 Android 四个架构 ==="
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
    goarm=""; [ "$arch" = "arm" ] && goarm="7"
    echo "building android-$arch"
    CC="${NDK}/${targets[$arch]}" \
      GOOS=android GOARCH=$arch GOARM=$goarm CGO_ENABLED=1 \
      go build -o "build/${appName}-android-${arch}" -ldflags="$ldflags" .
    "${NDK}/llvm-strip" "build/${appName}-android-${arch}"
  done
}

# ========================================
# 5. 打包（完全模仿 OpenList）
# ========================================
MakeCompress() {
  echo "=== 打包到 build/compress 目录 ==="
  cd build

  # Linux / Darwin / FreeBSD → tar.gz
  for f in ${appName}-linux-* ${appName}-darwin-* ${appName}-freebsd-*; do
    [ -f "$f" ] && tar -czf "compress/${f}.tar.gz" "$f" && rm -f "$f"
  done

  # Windows → zip
  for f in ${appName}-windows-*.exe; do
    [ -f "$f" ] && zip "compress/${f}.zip" "$f" && rm -f "$f"
  done

  # Android 直接复制
  cp ${appName}-android-* compress/ 2>/dev/null || true

  cd compress
  sha256sum * > SHA256SUMS.txt
  echo "本次生成了 $(ls -1 | wc -l) 个文件，已放入 build/compress/"
  ls -lh
}

# ========================================
# 主入口：完美支持 matrix 调用
# ========================================
case "$1" in
  release)
    case "$2" in
      android)
        BuildAndroid && MakeCompress
        ;;
      linux_musl)
        BuildMuslCommon && MakeCompress
        ;;
      linux_musl_all)
        BuildMuslAll && MakeCompress
        ;;
      freebsd)
        # 如果你以后想加 freebsd，可以在这里调用 BuildFreeBSD
        echo "FreeBSD 暂未实现"
        exit 0
        ;;
      *)
        # 默认：全部编译（GitHub Actions 里 "" 走这里）
        BuildMain
        BuildMuslCommon
        BuildMuslAll
        BuildAndroid
        MakeCompress
        ;;
    esac
    ;;
  *)
    echo "用法: $0 release [android|linux_musl|linux_musl_all]"
    echo "不传参数时编译全部平台"
    exit 1
    ;;
esac
