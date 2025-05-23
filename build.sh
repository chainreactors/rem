#!/bin/bash

# 检查并安装gox
command -v gox >/dev/null 2>&1 || { echo "正在安装gox..."; go install github.com/mitchellh/gox@latest; }

# 初始化变量
MOD=""
CONSOLE=""
LOCAL=""
REMOTE=""
OSARCH=""
APPLICATION=""
TRANSPORT=""

# 设置默认值
DEFAULT_APPLICATION="http,raw,socks,portforward"
DEFAULT_TRANSPORT="tcp,udp"

# 解析命令行参数
while getopts "m:c:l:r:o:a:t:" opt; do
    case $opt in
        m) MOD="$OPTARG";;
        c) CONSOLE="$OPTARG";;
        l) LOCAL="$OPTARG";;
        r) REMOTE="$OPTARG";;
        o) OSARCH="$OPTARG";;
        a) APPLICATION="$OPTARG";;
        t) TRANSPORT="$OPTARG";;
        \?) echo "无效的选项: -$OPTARG" >&2; exit 1;;
    esac
done

# 处理application和transport的条件编译
INDEX_FILE="runner/index.go"
TMP_FILE="runner/index.go.tmp"

# 生成新的index.go文件
cat > "$TMP_FILE" << 'EOF'
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

# 构建 ldflags
LDFLAGS="-w -s"

# 添加非空参数到 ldflags
if [ ! -z "$MOD" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultMod=$MOD'"
fi
if [ ! -z "$CONSOLE" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultConsole=$CONSOLE'"
fi
if [ ! -z "$LOCAL" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultLocal=$LOCAL'"
fi
if [ ! -z "$REMOTE" ]; then
    LDFLAGS="$LDFLAGS -X 'github.com/chainreactors/rem/runner.DefaultRemote=$REMOTE'"
fi

# 执行编译命令
if [ ! -z "$OSARCH" ]; then
    gox -osarch="$OSARCH" -ldflags "$LDFLAGS" -output="rem_{{.OS}}_{{.Arch}}" .
else
    go build -ldflags "$LDFLAGS" .
fi 