#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0.0")

ldflags="-w -s \
-X 'main.builtAt=$builtAt' \
-X 'main.gitCommit=$gitCommit' \
-X 'main.version=$version'"

mkdir -p build/compress

# ============ 是否开启 ech-tunnel 终极全家桶模式（50+ 文件） ============
FULL_MODE=true   # 改成 false 即回到轻量模式（只主流 + musl 常用）

# ========================================
# 1. musl 工具链安装（已修复所有 tar/cc1 问题）
# ========================================
install_musl_toolchains() {
  echo "=== 安装 musl 交叉编译工具链（ech-tunnel 专用终极版）==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local tools=(
    x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross
    mips-linux-musl-cross mipsel-linux-musl-cross
    mips64-linux-musl-cross mips64el-linux-musl-cross
    riscv64-linux-musl-cross powerpc64le-linux-musl-cross loongarch64-linux-musl-cross
  )
  local tmp=$(mktemp -d)
  trap "rm -rf '$tmp'" EXIT

  for t in "${tools[@]}"; do
    echo "正在安装 $t ..."
    curl -fsSL "${BASE}${t}.tgz" -o "$tmp/t.tgz"
    tar -xzf "$tmp/t.tgz" -C "$tmp"
    local dir="$tmp/$(tar -tf "$tmp/t.tgz" | head -1 | cut -d/ -f1)"
    sudo cp -r "$dir" /usr/local/
    sudo ln -sf "/usr/local/$t/bin/"* /usr/local/bin/ 2>/dev/null || true
  done
  echo "musl 工具链安装完成（tar 警告可忽略）"
}

# ========================================
# 2. 主流平台 + FreeBSD 全家桶 + windows 386
# ========================================
BuildMain() {
  echo "=== 编译主流平台 + FreeBSD 三件套 ==="
  docker pull crazymax/xgo:latest
  go install github.com/crazy-max/xgo@latest

  xgo -go 1.25.x -out "$appName" -ldflags="$ldflags" \
    -targets=windows/amd64,windows/386,darwin/amd64,darwin/arm64,\
linux/amd64,linux/arm64,linux/arm-7,linux/arm-6,linux/386,\
freebsd/amd64,freebsd/arm64,freebsd/386 .

  mv "$appName"-* build/
  upx --best --lzma build/"$appName"-* 2>/dev/null || true

  # windows-arm64（xgo 偶尔漏）
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64
  CC="$PWD/zcc-arm64" CXX="$PWD/zcxx-arm64" \
    GOOS=windows GOARCH=arm64 CGO_ENABLED=1 \
    go build -o "build/${appName}-windows-arm64.exe" -ldflags="$ldflags" .
}

# ========================================
# 3. 全 musl 静态平台（含所有冷门）
# ========================================
BuildMuslAll() {
  echo "=== 编译全部 musl 静态平台（含 loong64/riscv64/ppc64le/mips 等）==="
  install_musl_toolchains
  local flags="--extldflags '-static' $ldflags"

  # 常用
  CC=x86_64-linux-musl-gcc       GOOS=linux GOARCH=amd64   CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-amd64"     -ldflags="$flags" .
  CC=aarch64-linux-musl-gcc      GOOS=linux GOARCH=arm64   CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-arm64"     -ldflags="$flags" .
  CC=armv7l-linux-musleabihf-gcc GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-armv7"     -ldflags="$flags" .

  # 冷门全家桶
  CC=mips-linux-musl-gcc         GOOS=linux GOARCH=mips   GOMIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips"      -ldflags="$flags" .
  CC=mipsel-linux-musl-gcc       GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mipsle"    -ldflags="$flags" .
  CC=mips64-linux-musl-gcc       GOOS=linux GOARCH=mips64           CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64"    -ldflags="$flags" .
  CC=mips64el-linux-musl-gcc     GOOS=linux GOARCH=mips64le         CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64le"  -ldflags="$flags" .
  CC=riscv64-linux-musl-gcc      GOOS=linux GOARCH=riscv64          CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-riscv64"   -ldflags="$flags" .
  CC=powerpc64le-linux-musl-gcc  GOOS=linux GOARCH=ppc64le          CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-ppc64le"   -ldflags="$flags" .
  CC=loongarch64-linux-musl-gcc  GOOS=linux GOARCH=loong64          CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-loong64"   -ldflags="$flags" .
}

# ========================================
# 4. Android 四件套
# ========================================
BuildAndroid() {
  echo "=== 编译 Android 四架构 ==="
  wget -q https://dl.google.com/android/repository/android-ndk-r26d-linux.zip
  unzip -q android-ndk-r26d-linux.zip && rm android-ndk-r26d-linux.zip
  local NDK="$PWD/android-ndk-r26d/toolchains/llvm/prebuilt/linux-x86_64/bin"

  CC="${NDK}/x86_64-linux-android24-clang"   GOOS=android GOARCH=amd64 CGO_ENABLED=1 go build -o "build/${appName}-android-amd64" -ldflags="$ldflags" .
  CC="${NDK}/aarch64-linux-android24-clang"  GOOS=android GOARCH=arm64 CGO_ENABLED=1 go build -o "build/${appName}-android-arm64" -ldflags="$ldflags" .
  CC="${NDK}/i686-linux-android24-clang"     GOOS=android GOARCH=386   CGO_ENABLED=1 go build -o "build/${appName}-android-386"   -ldflags="$ldflags" .
  CC="${NDK}/armv7a-linux-androideabi24-clang" GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-android-arm" -ldflags="$ldflags" .

  "${NDK}/llvm-strip" build/${appName}-android-*
}

# ========================================
# 5. 可选：loong64 glibc 双 ABI（新旧世界）
# ========================================
BuildLoongGLIBC() {
  echo "=== 编译 loong64 glibc（abi2.0 默认，新世界）==="
  # 新世界（abi2.0）直接用系统 gcc 即可
  CC=aarch64-linux-gnu-gcc CXX=aarch64-linux-gnu-g++ \
    GOOS=linux GOARCH=loong64 CGO_ENABLED=1 \
    go build -o "build/${appName}-linux-loong64" -ldflags="$ldflags" .

  # 旧世界（abi1.0）极少人用，暂时注释掉，需要再开启
  # CC=loongarch64-unknown-linux-gnu-gcc CXX=loongarch64-unknown-linux-gnu-g++ \
  #   GOOS=linux GOARCH=loong64 CGO_ENABLED=1 \
  #   go build -o "build/${appName}-linux-loong64-abi1.0" -ldflags="$ldflags" .
}

# ========================================
# 6. 打包（完全模仿 OpenList 风格）
# ========================================
MakeCompress() {
  echo "=== 打包 ech-tunnel 全平台二进制 ==="
  cd build
  rm -rf compress && mkdir compress

  # 所有类 Unix（含 Android）→ tar.gz
  for f in ${appName}-linux-* ${appName}-darwin-* ${appName}-freebsd-* ${appName}-android-*; do
    [ -f "$f" ] && tar -czf "compress/${f}.tar.gz" "$f" && rm -f "$f"
  done

  # Windows → zip
  for f in ${appName}-windows-*.exe; do
    [ -f "$f" ] && zip "compress/${f%.exe}.zip" "$f" && rm -f "$f"
  done

  cd compress
  sha256sum * > SHA256SUMS.txt
  echo "ech-tunnel 全平台构建完成！共 $(ls -1 | grep -E '\.(tar\.gz|zip)$' | wc -l) 个文件"
  ls -lh
}

# ========================================
# 主入口
# ========================================
case "$1" in
  release)
    case "$2" in
      android)     BuildAndroid && MakeCompress ;;
      linux_musl)  BuildMuslAll && MakeCompress ;;
      *)
        BuildMain
        $FULL_MODE && BuildLoongGLIBC
        BuildMuslAll
        BuildAndroid
        MakeCompress
        ;;
    esac
    ;;
  *)
    echo "用法: $0 release [android|linux_musl]"
    echo "不传参数时编译全部平台（FULL_MODE=$FULL_MODE）"
    exit 1
    ;;
esac
