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
# 1. 安装 musl 工具链（OpenList 原 URL + 修复解压坑）
# ========================================
install_musl_toolchains() {
  echo "=== 安装 musl 交叉编译工具链（2025 终极修复版，支持所有冷门架构）==="
  local BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  local tools=(
    x86_64-linux-musl-cross aarch64-linux-musl-cross armv7l-linux-musleabihf-cross
    mips-linux-musl-cross mipsel-linux-musl-cross
    mips64-linux-musl-cross mips64el-linux-musl-cross
    riscv64-linux-musl-cross powerpc64le-linux-musl-cross loongarch64-linux-musl-cross
  )

  local tmpdir=$(mktemp -d)
  trap "rm -rf '$tmpdir'" EXIT

  for t in "${tools[@]}"; do
    echo "正在安装 $t ..."
    curl -fsSL "${BASE}${t}.tgz" -o "$tmpdir/toolchain.tgz"

    # 完整解压（不要 strip）
    tar -xzf "$tmpdir/toolchain.tgz" -C "$tmpdir"

    # 找到解压出来的真实目录名（通常就是 $t）
    local extracted_dir="$tmpdir/$(tar -tf "$tmpdir/toolchain.tgz" | head -1 | cut -f1 -d'/')"

    # 暴力把整个工具链目录直接扔到 /usr/local/
    # 这样 libexec/gcc/.../cc1 路径就 100% 正确
    sudo cp -r "$extracted_dir" /usr/local/

    # 同时把 bin 链接到 /usr/local/bin（防止 PATH 问题）
    sudo ln -sf "/usr/local/$t/bin/"* /usr/local/bin/ 2>/dev/null || true
  done

  # 关键验证：检查 cc1 是否真的存在
  for arch in riscv64 powerpc64le loongarch64; do
    if [ -f "/usr/local/${arch}-linux-musl-cross/libexec/gcc/${arch}-linux-musl/"*/cc1 ]; then
      echo "✓ ${arch} 的 cc1 已就绪"
    else
      echo "✗ ${arch} 的 cc1 丢失！构建会失败"
    fi
  done

  echo "所有 musl 工具链安装完成！"
}

# ========================================
# 2. 主流平台（xgo + windows-arm64，原版保持）
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
# 3. 常用 musl 静态平台（amd64 / arm64 / armv7）+ 冷门（原版合并）
# ========================================
BuildMuslAll() {
  echo "=== 编译全部 musl 静态平台（常用 + 冷门）==="
  install_musl_toolchains  # 调用安装
  local muslflags="--extldflags '-static' $ldflags"
  
  # 常用
  CC=x86_64-linux-musl-gcc GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-amd64" -ldflags="$muslflags" .
  CC=aarch64-linux-musl-gcc GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-arm64" -ldflags="$muslflags" .
  CC=armv7l-linux-musleabihf-gcc GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-armv7" -ldflags="$muslflags" .
  
  # 冷门（mips / loong64 / riscv64 / ppc64le）
  CC=mips-linux-musl-gcc GOOS=linux GOARCH=mips GO_MIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips" -ldflags="$muslflags" .
  CC=mipsel-linux-musl-gcc GOOS=linux GOARCH=mipsle GO_MIPS=softfloat CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mipsle" -ldflags="$muslflags" .
  CC=mips64-linux-musl-gcc GOOS=linux GOARCH=mips64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64" -ldflags="$muslflags" .
  CC=mips64el-linux-musl-gcc GOOS=linux GOARCH=mips64le CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-mips64le" -ldflags="$muslflags" .
  CC=riscv64-linux-musl-gcc GOOS=linux GOARCH=riscv64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-riscv64" -ldflags="$muslflags" .
  CC=powerpc64le-linux-musl-gcc GOOS=linux GOARCH=ppc64le CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-ppc64le" -ldflags="$muslflags" .
  CC=loongarch64-linux-musl-gcc GOOS=linux GOARCH=loong64 CGO_ENABLED=1 go build -o "build/${appName}-linux-musl-loong64" -ldflags="$muslflags" .
}

# ========================================
# 4. Android 四个架构（原版保持）
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
# 5. 打包（原版保持）
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
# 主入口（原版保持）
# ========================================
case "$1" in
  release)
    case "$2" in
      android)
        BuildAndroid && MakeCompress
        ;;
      linux_musl)
        BuildMuslAll && MakeCompress  # 现在用合并的 All 函数
        ;;
      freebsd)
        echo "FreeBSD 暂未实现"
        exit 0
        ;;
      *)
        # 默认：全部编译
        BuildMain
        BuildMuslAll
        BuildAndroid
        MakeCompress
        ;;
    esac
    ;;
  *)
    echo "用法: $0 release [android|linux_musl]"
    echo "不传参数时编译全部平台"
    exit 1
    ;;
esac
