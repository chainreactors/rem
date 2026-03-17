# DNS Tunnel

DNS tunnel 实现基于 DNS 协议的数据传输通道，使用 miekg/dns 库实现。

## 特性

- 支持通过 DNS 查询传输数据
- 使用 base64 编码将数据嵌入 DNS 域名
- 支持 TXT 记录响应
- 实现标准的 net.Conn 接口
- 支持客户端和服务端模式
- **支持本地测试，无需公共 DNS 服务器**
- **双向数据传输支持**
- **优化的数据分片机制**
- **符合 DNS RFC 1035 规范**

## 技术改进

### 1. 双向数据传输

- 客户端 → 服务端：通过 DNS 查询发送数据
- 服务端 → 客户端：通过 DNS 响应返回数据
- 支持流式数据传输

### 2. 优化的数据分片

- 每个 DNS 标签最大 60 字符（符合 RFC 1035 的 63 字符限制）
- 支持大数据量的分片传输
- 自动序列号管理，确保数据完整性

### 3. DNS RFC 1035 合规性

- UDP DNS 消息最大 512 字节
- TCP DNS 消息最大 65535 字节
- 域名标签最大 63 字符
- 完整域名最大 253 字符

## 本地测试

### 方法 1：使用 Go 测试

```bash
cd protocol/tunnel/dns
go test -v -run TestLocalDNSTunnel
```

### 方法 2：使用 dig 命令测试

启动 DNS 服务器后，可以用 dig 命令直接测试：

```bash
# 基本查询
dig @127.0.0.1 -p 5353 test.local TXT

# 发送编码数据（"test" -> "dGVzdA=="）
dig @127.0.0.1 -p 5353 dGVzdA==.0.test.local TXT
```

### 方法 3：使用 rem 命令行工具

```bash
# 服务端（监听本地5353端口）
./rem -l dns://127.0.0.1:5353/test.local -m raw

# 客户端（连接到本地DNS服务器）
./rem -c dns://127.0.0.1:5353/test.local -m proxy
```

## 使用方法

### URL 格式

```
dns://server:port/domain
```

本地测试示例:

```
dns://127.0.0.1:5353/test.local
dns://localhost:5353/tunnel.local
```

生产环境示例:

```
dns://8.8.8.8:53/tunnel.example.com
dns://1.1.1.1:53/c2.domain.com
```

### 客户端示例

```go
import (
    "github.com/chainreactors/rem/protocol/tunnel"
    "github.com/chainreactors/rem/protocol/core"
)

// 创建DNS tunnel客户端（本地测试）
tun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, false)
if err != nil {
    log.Fatal(err)
}

// 连接到本地DNS服务器
conn, err := tun.Dial("dns://127.0.0.1:5353/test.local")
if err != nil {
    log.Fatal(err)
}

// 使用conn进行数据传输
conn.Write([]byte("Hello DNS Tunnel"))
```

### 服务端示例

```go
// 创建DNS tunnel服务端（本地测试，使用非特权端口）
tun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, true)
if err != nil {
    log.Fatal(err)
}

// 监听本地DNS请求
listener, err := tun.Listen("dns://127.0.0.1:5353/test.local")
if err != nil {
    log.Fatal(err)
}

// 接受连接
for {
    conn, err := listener.Accept()
    if err != nil {
        continue
    }

    go handleConnection(conn)
}
```

## 端口说明

- **端口 53**: 标准 DNS 端口，需要管理员权限
- **端口 5353**: mDNS 端口，用于本地测试，无需特权
- **其他高位端口**: 也可用于测试，如 8053, 9053 等

## 实现细节

### 数据编码协议

- 数据通过 base64 编码后嵌入 DNS 查询的域名中
- 格式：`{base64_data}.{sequence_number}.{domain}`
- 每个 DNS 标签最大 60 字符（符合 DNS 规范的 63 字符限制）
- 支持大数据量的分片传输

### 双向通信机制

- 客户端发送：通过 DNS 查询发送数据
- 服务端响应：通过 TXT 记录返回数据
- 支持流式数据传输和响应

### 连接管理

- 基于序列号的连接跟踪
- 支持多连接并发处理
- 自动清理过期连接

## 性能优化

### 数据分片策略

- 每个分片最大 60 字符（base64 编码）
- 原始数据约 45 字节/分片
- 支持大数据量的高效传输

### 内存管理

- 缓冲区自动管理
- 连接池优化
- 内存泄漏防护

## 本地测试优势

1. **无需公网**: 完全在本地运行，不依赖外部 DNS 服务器
2. **快速调试**: 可以看到实时的 DNS 查询和响应
3. **权限友好**: 使用高位端口避免权限问题
4. **隔离环境**: 测试数据不会泄露到公网
5. **双向测试**: 支持完整的双向数据传输测试

## 限制

- 基于 UDP 的 DNS 查询有 512 字节限制
- Base64 编码会增加约 33%的数据量
- 需要 DNS 服务器支持 TXT 记录响应
- 网络延迟可能影响传输性能
