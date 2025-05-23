# rem

## What is it?

rem是全新架构的全场景应用层/传输层代理工具.

相比frp的架构取消了server的概念, server与client融合为平等的agent. 以实现更加自由的流量转发. 并且可以对流量的每个细节自定义.

相比iox实现了更复杂的流量处理, 不单纯是点对点的转发, 而是在点对点之间插入了agent, 在agent之间对流量隧道进行控制, 可以做到流量自定义加密混淆, wrapper各种功能.

## Feature

* 支持任意方向，任意信道的代理与端口转发
* 支持流量特征自定义与加密方式自定义
* 极简的命令行设计
* 全平台兼容

## docs

https://chainreactors.github.io/wiki/rem/usage/

## Roadmap

### 第一阶段 重构rem

- [x] 调整主体文件结构
- [x] 调整函数,文件,变量命名
- [x] 重构代理逻辑
- [x] 代码解耦  
- [x] 重构monitor与流量控制
- [x] 重新设计cli ui
- [x] 支持rportfwd 
- [x] 重新设计msg
- [x] 重新设计wrapper v0.1.0
- [x] 支持neoregeorg, 将半双工信道升级为全双工
- [x] 支持云函数, cdn等 
- [x] 支持配置任意数量的多级透明socks5/socks4a/socks4/http/https代理
- [x] 支持tls协议 
- [x] 支持级联
- [ ] 支持端口复用(working)
- [ ] 支持端口敲门(working)
- [x] RPORTFWD_LOCAL与PORTFWD_LOCAL
- [x] 重构proxyclient 

**讨论中的高级功能**

- [x] Proxy as a service, 提供一套流量协议标准以及多语言跨平台sdk, 无性能损耗的转发流量 (working)
- [x] 心跳代理, 使用非长连接的方式建立代理, 实现更复杂的流量与统计学特征混淆
- [ ] P2P式的多级代理, 类似STUN协议
- [x] 重载任意C2的listener, 最终目的将listener从C2中解耦出来
- [x] 实现编译期, 自定义templates. 实现随机流量特征与最小文件体积
- [ ] 通过ebpf与raw packet实现更高级的信道建立与隐蔽

## Similar or related works

* [frp](https://github.com/fatedier/frp) 最常使用, 最稳定的反向代理工具. 配置相对麻烦, 有一些强特征已被主流防护设备识别, 类似的还有nps, ngrok, rathole, spp.
* [gost](https://github.com/go-gost/gost) 一款强大的正向代理工具, v2版本不支持反向代理, v3开始支持, 未来可期.
* [iox](https://github.com/EddieIvan01/iox) 轻量但稳定的端口转发工具
* [xray](https://github.com/XTLS/Xray-core) 正向代理工具, 在协议的隐蔽性与性能上非常强大, 并拥有最好的密码学特性(向前加密, 无特征等)