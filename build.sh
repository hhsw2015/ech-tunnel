#!/usr/bin/env bash
set -e

appName="ech-tunnel"
builtAt="$(date +'%F %T %z')"
gitCommit=$(git log --pretty=format:"%h" -1)
version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v1.0.0")

ldflags="-w -s -X 'main.builtAt=$builtAt' -X 'main.gitCommit=$gitCommit' -X 'main.version=$version'"
mkdir -p build/compress

# ==================== 1. 安装所有 musl 交叉编译工具链（关键修复点）====================
install_musl_toolchains() {
  echo "=== 安装 musl 交叉编译工具链（已修复 GitHub Actions 解压问题）==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/v1/"
  local tools=(
    x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross
    mips-linux-musl-cross mipsel-linux-musl-cross
    mips64-linux-musl-cross mips64el-linux-musl-cross
    riscv64-linux-musl-cross powerpc64le-linux-musl-cross loongarch64-linux-musl-cross
  )

  local tmp=$(mktemp -d)
  trap "rm -rf '$tmp'" EXIT

  for t in "${tools[@]}"; do
    echo "安装 $t ..."
    curl -fsSL "${BASE}${t}.tgz" -o "$tmp/$t.tgz"
    tar -xzf "$tmp/$t.tgz" -C "$tmp" --strip-components=1
    sudo cp -r "$tmp/bin/"* /usr/local/bin/ 2>/dev/null || true
    sudo cp -r "$tmp/lib/"* /usr/local/lib/ 2>/dev/null || true
    sudo cp -r "$tmp/include" /usr/local/ 2>/dev/null || true
  done

  # 验证关键工具链是否真的可用
  for cc in x86_64-linux-musl-gcc aarch64-linux-musl-gcc armv7l-linux-musleabihf-gcc; do
    which "$cc" >/dev/null || { echo "$cc 未安装成功！"; exit 1; }
  done
}

# ==================== 2. 主流平台（抛弃 xgo，改用 zig + 手动 windows-arm64）====================
build_main() {
  echo "=== 编译主流平台（darwin/windows/linux glibc）==="
  # 用 zig 编译所有 glibc 平台（速度极快，零依赖）
  sudo snap install zig --classic --beta

  # darwin amd64/arm64 + linux glibc amd64/arm64/386/armv6/armv7
  zig build-exe -target x86_64-macos-gnu -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-darwin-amd64
  zig build-exe -target aarch64-macos-gnu -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-darwin-arm64
  zig build-exe -target x86_64-linux-gnu -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-linux-amd64
  zig build-exe -target aarch64-linux-gnu -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-linux-arm64
  zig build-exe -target i386-linux-gnu -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-linux-386
  zig build-exe -target arm-linux-gnueabihf -O ReleaseSafe -ldflags "$ldflags" . -femit-bin=build/${appName}-linux-arm-7

  # windows amd64（原生 go 就行）
  GOOS=windows GOARCH=amd64 go build -o build/${appName}-windows-amd64.exe -ldflags="$ldflags" .

  # windows-arm64（用 OpenList 维护的 wrapper）
  curl -fsSL -o zcc-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcc-arm64
  curl -fsSL -o zcxx-arm64 https://github.com/OpenListTeam/OpenList/raw/main/wrapper/zcxx-arm64
  chmod +x zcc-arm64 zcxx-arm64
  CC=$(pwd)/zcc-arm64 CXX=$(pwd)/zcxx-arm64 GOOS=windows GOARCH=arm64 CGO_ENABLED=1 \
    go build -o build/${appName}-windows-arm64.exe -ldflags="$ldflags" .
}

# ==================== 3. 所有 musl 静态平台（常用 + 冷门）====================
build_all_musl() {
  echo "=== 编译全部 musl 静态平台 ==="
  local flags="--extldflags '-static' $ldflags"

  # 常用三件套
  CC=x86_64-linux-musl-gcc       GOOS=linux GOARCH=amd64   CGO_ENABLED=1 go build -o build/${appName}-linux-musl-amd64   -ldflags="$flags" .
  CC=aarch64-linux-musl-gcc      GOOS=linux GOARCH=arm64   CGO_ENABLED=1 go build -o build/${appName}-linux-musl-arm64   -ldflags="$flags" .
  CC=armv7l-linux-musleabihf-gcc GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o build/${appName}-linux-musl-armv7   -ldflags="$flags" .

  # 冷门全家桶（mips/loong64/riscv64/ppc64le）
  CC=mips-linux-musl-gcc          GOOS=linux GOARCH=mips   GO_MIPS=softfloat CGO_ENABLED=1 go build -o build/${appName}-linux-musl-mips      -ldflags="$flags" .
  CC=mipsel-linux-musl-gcc        GOOS=linux GOARCH=mipsle GO_MIPS=softfloat CGO_ENABLED=1 go build -o build/${appName}-linux-musl-mipsle    -ldflags="$flags" .
  CC=mips64-linux-musl-gcc        GOOS=linux GOARCH=mips64            CGO_ENABLED=1 go build -o build/${appName}-linux-musl-mips64    -ldflags="$flags" .
  CC=mips64el-linux-musl-gcc      GOOS=linux GOARCH=mips64le          CGO_ENABLED=1 go build -o build/${appName}-linux-musl-mips64le  -ldflags="$flags" .
  CC=riscv64-linux-musl-gcc       GOOS=linux GOARCH=riscv64           CGO_ENABLED=1 go build -o build/${appName}-linux-musl-riscv64   -ldflags="$flags" .
  CC=powerpc64le-linux-musl-gcc   GOOS=linux GOARCH=ppc64le           CGO_ENABLED=1 go build -o build/${appName}-linux-musl-ppc64le   -ldflags="$flags" .
  CC=loongarch64-linux-musl-gcc   GOOS=linux GOARCH=loong64           CGO_ENABLED=1 go build -o build/${appName}-linux-musl-loong64   -ldflags="$flags" .
}

# ==================== 4. Android 四件套 ====================
build_android() {
  echo "=== 编译 Android 四架构 ==="
  wget -q https://dl.google.com/android/repository/android-ndk-r26d-linux.zip
  unzip -q android-ndk-r26d-linux.zip
  local NDK="$PWD/android-ndk-r26d/toolchains/llvm/prebuilt/linux-x86_64/bin"

  CC=$NDK/x86_64-linux-android24-clang   GOOS=android GOARCH=amd64 CGO_ENABLED=1 go build -o build/${appName}-android-amd64   -ldflags="$ldflags" .
  CC=$NDK/aarch64-linux-android24-clang  GOOS=android GOARCH=arm64 CGO_ENABLED=1 go build -o build/${appName}-android-arm64   -ldflags="$ldflags" .
  CC=$NDK/i686-linux-android24-clang     GOOS=android GOARCH=386   CGO_ENABLED=1 go build -o build/${appName}-android-386     -ldflags="$ldflags" .
  CC=$NDK/armv7a-linux-androideabi24-clang GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o build/${appName}-android-armv7   -ldflags="$ldflags" .

  "$NDK/llvm-strip" build/${appName}-android-*
}

# ==================== 5. 打包压缩 ====================
compress_all() {
  echo "=== 开始打包压缩 ==="
  cd build

  # Linux/Darwin → tar.gz
  for f in ${appName}-linux-* ${appName}-darwin-*; do
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
  echo "=== 全部构建完成！共 $(ls -1 | wc -l) 个文件 ==="
  ls -lh
}

# ==================== 主流程（完美配合你的 GitHub Actions matrix）====================
case "$1" in
  release)
    case "$2" in
      android)     install_musl_toolchains; build_android;     compress_all ;;
      linux_musl)  install_musl_toolchains; build_all_musl;    compress_all ;;
      "")          # 默认全部
        install_musl_toolchains
        build_main
        build_all_musl
        build_android
        compress_all
        ;;
      *) echo "不支持的参数: $2"; exit 1 ;;
    esac
    ;;
  *) echo "用法: $0 release [android|linux_musl]"; exit 1 ;;
esac
