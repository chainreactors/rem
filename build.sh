#!/bin/bash

# REM 构建脚本
# 用法:
#   ./build.sh                                    # 使用默认配置编译多平台可执行文件
#   ./build.sh -g                                 # 只生成配置文件，不编译
#   ./build.sh -a "http,socks" -t "tcp"           # 指定模块编译
#   ./build.sh --full                             # 编译包含所有模块的full版本
#   ./build.sh --full -g                          # 生成full版本配置文件
#   ./build.sh -buildmode c-shared -o linux/amd64 # 编译动态库
#   ./build.sh -buildmode c-archive --full        # 编译full版本静态库
#   ./build.sh --tinygo                           # 编译 TinyGo CLI（自动包含 noasm）

# 初始化变量
MOD=""
SERVER=""
CLIENT=""
LOCAL=""
REMOTE=""
QUIET=""
OSARCH=""
APPLICATION=""
TRANSPORT=""
GENERATE_ONLY=false
BUILDMODE=""
DISABLE_ARCH_FLAGS=false
BUILD_TAGS=""
# 设置默认值
DEFAULT_APPLICATION="http,raw,socks,portforward"
DEFAULT_TRANSPORT="tcp,udp,icmp"
DEFAULT_OSARCH="windows/amd64 windows/386 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

# 设置FULL版本的模块列表
FULL_APPLICATION="http,raw,socks,portforward,shadowsocks,trojan"
FULL_TRANSPORT="tcp,udp,websocket,unix,icmp,http,memory"


# 生成配置文件函数
generate_config() {
    # 处理application和transport的条件编译
    INDEX_FILE="runner/index.go"
    TMP_FILE="runner/index.go.tmp"

    # 生成新的index.go文件
    cat > "$TMP_FILE" << 'EOF'
//go:build !tinygo

package runner

// basic
import (
	_ "github.com/chainreactors/rem/protocol/wrapper"
)

// application
import (
EOF

    # 添加application imports
    if [ ! -z "$APPLICATION" ]; then
        # 使用用户指定的模块
        echo "$APPLICATION" | tr ',' '\n' | while read -r app; do
            echo "	_ \"github.com/chainreactors/rem/protocol/serve/$app\"" >> "$TMP_FILE"
        done
    else
        # 使用默认模块
        echo "$DEFAULT_APPLICATION" | tr ',' '\n' | while read -r app; do
            echo "	_ \"github.com/chainreactors/rem/protocol/serve/$app\"" >> "$TMP_FILE"
        done
    fi

    # 添加transport imports
    cat >> "$TMP_FILE" << 'EOF'
)

// transport
import (
EOF

    # 添加transport imports
    if [ ! -z "$TRANSPORT" ]; then
        # 使用用户指定的模块
        echo "$TRANSPORT" | tr ',' '\n' | while read -r trans; do
            echo "	_ \"github.com/chainreactors/rem/protocol/tunnel/$trans\"" >> "$TMP_FILE"
        done
    else
        # 使用默认模块
        echo "$DEFAULT_TRANSPORT" | tr ',' '\n' | while read -r trans; do
            echo "	_ \"github.com/chainreactors/rem/protocol/tunnel/$trans\"" >> "$TMP_FILE"
        done
    fi

    # 完成文件
    cat >> "$TMP_FILE" << 'EOF'
)
EOF

    # 替换原文件
    mv "$TMP_FILE" "$INDEX_FILE"
    gofmt -w "$INDEX_FILE"
}



# 解析命令行参数
while [[ $# -gt 0 ]]; do
    case $1 in
        -m)
            MOD="$2"
            shift 2
            ;;
        -s)
            SERVER="$2"
            shift 2
            ;;
        -c)
            CLIENT="$2"
            shift 2
            ;;
        -l)
            LOCAL="$2"
            shift 2
            ;;
        -r)
            REMOTE="$2"
            shift 2
            ;;
        -q)
            QUIET="true"
            shift
            ;;
        -o)
            OSARCH="$2"
            shift 2
            ;;
        -a)
            APPLICATION="$2"
            shift 2
            ;;
        -t)
            TRANSPORT="$2"
            shift 2
            ;;
        --tags)
            BUILD_TAGS="$2"
            shift 2
            ;;
        -g)
            GENERATE_ONLY=true
            shift
            ;;
        --full)
            APPLICATION="$FULL_APPLICATION"
            TRANSPORT="$FULL_TRANSPORT"
            echo "使用FULL版本配置: APPLICATION=$APPLICATION, TRANSPORT=$TRANSPORT"
            shift
            ;;
        -buildmode)
            BUILDMODE="$2"
            shift 2
            ;;
        --tinygo)
            BUILDMODE="tinygo"
            shift
            ;;
        --disable-arch-flags)
            DISABLE_ARCH_FLAGS=true
            shift
            ;;
        -h|--help)
            echo "REM 构建脚本"
            echo "用法: $0 [选项]"
            echo ""
            echo "选项:"
            echo "  -m MOD              设置默认模式"
            echo "  -s SERVER           设置默认服务器监听地址"
            echo "  -c CLIENT           设置默认客户端连接地址"
            echo "  -l LOCAL            设置默认本地地址"
            echo "  -r REMOTE           设置默认远程地址"
            echo "  -q                  启用 quiet 模式（静默模式）"
            echo "  -o OSARCH           指定目标平台，空格或逗号分隔 (默认: windows/amd64 windows/386 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)"
            echo "  -a APPLICATION      指定应用模块 (如: http,socks,raw)"
            echo "  -t TRANSPORT        指定传输模块 (如: tcp,udp,websocket,simplex)"
            echo "  --tags TAGS         指定 build tags，逗号分隔 (如: oss,graph,dns)"
            echo "  -g                  只生成配置文件，不编译"
            echo "  --full              编译包含所有模块的full版本"
            echo "  -buildmode MODE     指定构建模式 (exe, tinygo, c-shared, c-archive)"
            echo "  --tinygo            构建 TinyGo CLI (等同 -buildmode tinygo)"
            echo "  --disable-arch-flags 禁用架构特定的编译标志 (解决 GCC 兼容性问题)"
            echo "  -h, --help          显示此帮助信息"
            echo ""
            echo "构建模式:"
            echo "  exe                 默认可执行文件 (使用gox进行交叉编译，CGO_ENABLED=0)"
            echo "  tinygo              TinyGo CLI 可执行文件 (默认 tags: noasm)"
            echo "  c-shared            动态链接库 (.dll/.so，CGO_ENABLED=1)"
            echo "  c-archive           静态链接库 (.a，CGO_ENABLED=1)"
            echo ""
            echo "示例:"
            echo "  $0                                    # 使用默认配置编译多平台可执行文件"
            echo "  $0 -g                                 # 只生成配置文件"
            echo "  $0 -q                                 # 编译 quiet 模式的可执行文件"
            echo "  $0 -t simplex --tags oss              # 编译 simplex transport 并添加 oss tag"
            echo "  $0 --full                             # 编译full版本多平台可执行文件"
            echo "  $0 -buildmode c-shared -o linux/amd64 # 编译动态库"
            echo "  $0 -buildmode c-archive --full        # 编译full版本静态库"
            echo "  $0 --tinygo                           # 编译 TinyGo CLI（含 noasm）"
            echo "  $0 --full -buildmode c-shared --disable-arch-flags # 禁用架构标志编译"
            exit 0
            ;;
        *)
            echo "无效的选项: $1" >&2
            echo "用法: $0 [-m mod] [-s server] [-c client] [-l local] [-r remote] [-q] [-o osarch] [-a application] [-t transport] [--tags tags] [-g] [--full] [-buildmode mode] [--tinygo]"
            echo "使用 -h 或 --help 查看详细帮助"
            exit 1
            ;;
    esac
done

# 生成配置文件
generate_config

# 显示用户指定的 build tags
if [ ! -z "$BUILD_TAGS" ]; then
    echo "使用 build tags: $BUILD_TAGS"
fi

# 构建 ldflags
LDFLAGS="-w -s"

# 添加非空参数到 ldflags
if [ ! -z "$MOD" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultMod=$MOD'"
fi
# 注意：SERVER 和 CLIENT 在编译时设置到 DefaultConsole
# 因为 options.go 中的逻辑会根据 ServerAddr/ClientAddr 自动设置
if [ ! -z "$SERVER" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultConsole=$SERVER'"
fi
if [ ! -z "$CLIENT" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultConsole=$CLIENT'"
fi
if [ ! -z "$LOCAL" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultLocal=$LOCAL'"
fi
if [ ! -z "$REMOTE" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultRemote=$REMOTE'"
fi
if [ ! -z "$QUIET" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultQuiet=$QUIET'"
fi

# 如果只是生成配置文件，则退出
if [ "$GENERATE_ONLY" = true ]; then
    echo "配置文件已生成: $INDEX_FILE"
    exit 0
fi

# 设置默认构建模式
if [ -z "$BUILDMODE" ]; then
    BUILDMODE="exe"
fi

# 根据构建模式执行编译
case "$BUILDMODE" in
    "exe")
        # 默认可执行文件模式，使用gox进行交叉编译
        command -v gox >/dev/null 2>&1 || { echo "正在安装gox..."; go install github.com/mitchellh/gox@latest; }

        # 如果没有指定OSARCH，使用默认值
        if [ -z "$OSARCH" ]; then
            OSARCH="$DEFAULT_OSARCH"
        fi

        echo "使用gox编译可执行文件，目标平台: $OSARCH"

        # 构建tags参数
        TAGS="forceposix osusergo netgo"
        if [ ! -z "$BUILD_TAGS" ]; then
            # 将逗号替换为空格
            BUILD_TAGS_SPACE=$(echo "$BUILD_TAGS" | tr ',' ' ')
            TAGS="$TAGS $BUILD_TAGS_SPACE"
        fi

        gox -osarch="$OSARCH" -ldflags "$LDFLAGS" -output="rem_{{.OS}}_{{.Arch}}" -cgo=0 -tags="$TAGS" .
        ;;
    "tinygo")
        mkdir -p dist
        TINYGO_TAGS="noasm"
        if [ ! -z "$BUILD_TAGS" ]; then
            TINYGO_TAGS="$TINYGO_TAGS,$BUILD_TAGS"
        fi
        OUTPUT="dist/rem_tinygo"
        echo "使用 tinygo 编译 cmd/tinygo，tags: $TINYGO_TAGS"
        tinygo build -tags "$TINYGO_TAGS" -no-debug -o "$OUTPUT" ./cmd/tinygo
        echo "TinyGo 编译完成: $OUTPUT"
        ;;
    "c-shared"|"c-archive")
        # 库文件模式，使用原生go build
        echo "编译库文件，模式: $BUILDMODE"

        # 如果没有指定模块，使用FULL版本配置
        if [ -z "$APPLICATION" ] && [ -z "$TRANSPORT" ]; then
            APPLICATION="$FULL_APPLICATION"
            TRANSPORT="$FULL_TRANSPORT"
            echo "库编译使用FULL版本配置: APPLICATION=$APPLICATION, TRANSPORT=$TRANSPORT"
            # 重新生成配置文件
            generate_config
        fi

        # 设置输出目录
        mkdir -p dist/lib

        # 确定目标路径
        TARGET_PATH="./cmd/export/"

        if [ ! -z "$OSARCH" ]; then
            # 解析OSARCH参数，支持空格或逗号分隔
            # 将逗号替换为空格，然后按空格分割
            OSARCH_NORMALIZED=$(echo "$OSARCH" | tr ',' ' ')
            read -ra TARGETS <<< "$OSARCH_NORMALIZED"
            for target in "${TARGETS[@]}"; do
                IFS='/' read -ra PARTS <<< "$target"
                GOOS="${PARTS[0]}"
                GOARCH="${PARTS[1]}"

                # 设置文件扩展名和输出路径
                if [ "$BUILDMODE" = "c-shared" ]; then
                    if [ "$GOOS" = "windows" ]; then
                        EXT=".dll"
                    else
                        EXT=".so"
                    fi
                    OUTPUT="dist/lib/rem_community_${GOOS}_${GOARCH}${EXT}"
                else
                    # c-archive
                    OUTPUT="dist/lib/librem_community_${GOOS}_${GOARCH}.a"
                fi

                echo "编译 $GOOS/$GOARCH -> $OUTPUT"

                # 设置交叉编译环境
                export CGO_ENABLED=1
                export GOOS="$GOOS"
                export GOARCH="$GOARCH"

                # 设置交叉编译器和编译标志
                if [ "$GOOS" = "windows" ]; then
                    if [ "$GOARCH" = "amd64" ]; then
                        export CC=x86_64-w64-mingw32-gcc
                    elif [ "$GOARCH" = "386" ]; then
                        export CC=i686-w64-mingw32-gcc
                    fi
                elif [ "$GOOS" = "linux" ]; then
                    # Linux 平台需要 _GNU_SOURCE 宏来定义 sigset_t 等类型
                    export CGO_CFLAGS="-D_GNU_SOURCE"

                    if [ "$DISABLE_ARCH_FLAGS" = false ]; then
                        if [ "$GOARCH" = "386" ]; then
                            export PKG_CONFIG_PATH=/usr/lib/i386-linux-gnu/pkgconfig
                            export CC="gcc -m32"
                            export CGO_CFLAGS="-m32 -D_GNU_SOURCE"
                            export CGO_LDFLAGS="-m32"
                        elif [ "$GOARCH" = "amd64" ]; then
                            # 检查是否支持 64 位编译
                            if ! gcc -m64 -E - </dev/null >/dev/null 2>&1; then
                                echo "警告: 当前 GCC 不支持 64 位编译，跳过 linux/amd64"
                                echo "提示: 可以使用 --disable-arch-flags 参数禁用架构特定标志"
                                continue
                            fi
                            export CC="gcc -m64"
                            export CGO_CFLAGS="-m64 -D_GNU_SOURCE"
                            export CGO_LDFLAGS="-m64"
                        fi
                    fi
                fi

                # 执行编译
                if [ ! -z "$BUILD_TAGS" ]; then
                    # 将逗号替换为空格
                    BUILD_TAGS_SPACE=$(echo "$BUILD_TAGS" | tr ',' ' ')
                    go build -buildmode="$BUILDMODE" -o "$OUTPUT" -ldflags "$LDFLAGS" -tags="$BUILD_TAGS_SPACE" -buildvcs=false "$TARGET_PATH"
                else
                    go build -buildmode="$BUILDMODE" -o "$OUTPUT" -ldflags "$LDFLAGS" -buildvcs=false "$TARGET_PATH"
                fi
            done
        else
            # 本地平台编译
            # 检测当前平台
            LOCAL_OS=$(go env GOOS)
            LOCAL_ARCH=$(go env GOARCH)

            if [ "$BUILDMODE" = "c-shared" ]; then
                if [ "$LOCAL_OS" = "windows" ]; then
                    EXT=".dll"
                else
                    EXT=".so"
                fi
                OUTPUT="dist/lib/rem_community_${LOCAL_OS}_${LOCAL_ARCH}${EXT}"
            else
                OUTPUT="dist/lib/librem_community_${LOCAL_OS}_${LOCAL_ARCH}.a"
            fi

            echo "编译本地平台 -> $OUTPUT"

            # 为 Linux 本地编译设置 CGO_CFLAGS
            if [ "$LOCAL_OS" = "linux" ]; then
                export CGO_CFLAGS="-D_GNU_SOURCE"

                # 检查是否为 64 位架构且 GCC 不支持 64 位编译
                if [ "$LOCAL_ARCH" = "amd64" ]; then
                    if ! gcc -m64 -E - </dev/null >/dev/null 2>&1; then
                        echo "错误: 当前 GCC 不支持 64 位编译，无法编译 linux/amd64"
                        echo "请安装支持 64 位的 GCC 或在 64 位系统上编译"
                        echo "或者尝试编译 32 位版本: ./build.sh --full -buildmode c-shared -o \"linux/386\""
                        exit 1
                    fi
                    export CC="gcc -m64"
                    export CGO_CFLAGS="-m64 -D_GNU_SOURCE"
                    export CGO_LDFLAGS="-m64"
                elif [ "$LOCAL_ARCH" = "386" ]; then
                    export CC="gcc -m32"
                    export CGO_CFLAGS="-m32 -D_GNU_SOURCE"
                    export CGO_LDFLAGS="-m32"
                fi
            fi

            if [ ! -z "$BUILD_TAGS" ]; then
                # 将逗号替换为空格
                BUILD_TAGS_SPACE=$(echo "$BUILD_TAGS" | tr ',' ' ')
                CGO_ENABLED=1 go build -buildmode="$BUILDMODE" -o "$OUTPUT" -ldflags "$LDFLAGS" -tags="$BUILD_TAGS_SPACE" -buildvcs=false "$TARGET_PATH"
            else
                CGO_ENABLED=1 go build -buildmode="$BUILDMODE" -o "$OUTPUT" -ldflags "$LDFLAGS" -buildvcs=false "$TARGET_PATH"
            fi
        fi

        echo "库文件编译完成！输出目录: dist/lib/"
        ls -la dist/lib/
        ;;
    *)
        echo "不支持的构建模式: $BUILDMODE" >&2
        echo "支持的模式: exe, tinygo, c-shared, c-archive" >&2
        exit 1
        ;;
esac
