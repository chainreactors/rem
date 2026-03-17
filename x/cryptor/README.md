# `x/cryptor` 使用说明

本文档说明两件事：

1. 如何通过 build tag 控制算法是否编译进来。
2. 如何用 `Block` / `Stream` 两层机制做任意分组或流式加解密。

---

## 1. 设计概览

`x/cryptor` 只保留两层抽象：

- `Block`：负责 `Build(key) -> cipher.Block`
- `Stream`：负责 `Encryptor/Decryptor -> cipher.Stream`

核心思想：

- 你只要实现 `Block`，就能通过 `StreamOf(block)` 自动得到 CTR 流。
- 你也可以直接实现 `Stream`（如果你有原生流算法）。

---

## 2. 算法与 build tag

### 默认可用（不加 tag）

- `aes`
- `xor`

### 可选算法（按 tag 编译）

- `sm4` -> 名称 `sm4`
- `twofish` -> 名称 `twofish`
- `tripledes` -> 名称 `tripledes`
- `cast5` -> 名称 `cast5`
- `blowfish` -> 名称 `blowfish`
- `tea` -> 名称 `tea`
- `xtea` -> 名称 `xtea`

示例：

```bash
go build -tags "cast5 xtea" ./...
```

编译后可通过注册中心按名字发现这些算法。

---

## 3. 注册中心 API

常用方法：

- `RegisterBlock(name, factory)`
- `BlockByName(name)`
- `BuildBlock(name, key)`
- `StreamByName(name)`
- `BlockNames()`

说明：

- 名称不区分大小写（内部会归一化）。
- 如果算法未编译进来，会返回 `ErrBlockNotFound`。

---

## 4. 直接做 Block 加解密

适用于你只需要 `cipher.Block` 的场景。

```go
package main

import (
	"fmt"

	"github.com/chainreactors/rem/x/cryptor"
)

func main() {
	block, err := cryptor.BuildBlock("aes", []byte("12345678901234567890123456789012"))
	if err != nil {
		panic(err)
	}

	src := make([]byte, block.BlockSize())
	dst := make([]byte, block.BlockSize())
	block.Encrypt(dst, src)
	fmt.Println("block encrypted size:", len(dst))
}
```

---

## 5. 用 Block 自动组装 Stream（推荐）

### 方式 A：按名称直接取流

```go
stream, err := cryptor.StreamByName("aes")
if err != nil {
	return err
}

enc, err := stream.Encryptor(key, iv)
if err != nil {
	return err
}
dec, err := stream.Decryptor(key, iv)
if err != nil {
	return err
}

enc.XORKeyStream(ciphertext, plaintext)
dec.XORKeyStream(recovered, ciphertext)
```

### 方式 B：先取 Block，再手动 `StreamOf`

```go
block, err := cryptor.BlockByName("cast5")
if err != nil {
	return err
}

stream := cryptor.StreamOf(block)
enc, err := stream.Encryptor(key, iv)
if err != nil {
	return err
}
```

注意：`iv` 长度必须等于 `block.BlockSize()`，否则会返回 `ErrInvalidIVLength`。

---

## 6. 自定义算法扩展

### 6.1 新增一个 Block 算法

```go
type MyBlock struct{}

func (MyBlock) Build(key []byte) (cipher.Block, error) {
	// 返回你的 block 实现
}

func init() {
	cryptor.RegisterBlock("myblock", func() cryptor.Block { return MyBlock{} })
}
```

注册后用户可直接：

- `cryptor.BuildBlock("myblock", key)`
- `cryptor.StreamByName("myblock")`

### 6.2 直接实现 Stream 算法

如果你有天然流算法（不依赖 block），可直接实现：

```go
type MyStream struct{}

func (MyStream) Encryptor(key, iv []byte) (cipher.Stream, error) { /* ... */ }
func (MyStream) Decryptor(key, iv []byte) (cipher.Stream, error) { /* ... */ }
```

然后在业务侧直接使用 `MyStream`，无需经过 `StreamOf`。

---

## 7. 在 wrapper 侧的建议

wrapper 层建议只依赖 `Stream`：

- 从参数读取算法名（如 `opt["algo"]`）
- 调用 `cryptor.StreamByName(algo)`
- 用 `Encryptor/Decryptor` 包装读写流

这样 wrapper 不需要知道具体算法细节，算法开关只由 tag 和注册中心决定。
