# rem

blog:  
  - https://chainreactors.github.io/wiki/blog/2025/04/13/rem-introduce/

![](https://socialify.git.ci/chainreactors/rem-community/image?description=1&font=Inter&forks=1&issues=1&language=1&name=1&owner=1&pattern=Circuit%20Board&pulls=1&stargazers=1&theme=Light)

## What is it?

rem是全新架构的全场景应用层/传输层代理工具.

相比frp的架构取消了server的概念, server与client融合为平等的agent. 以实现更加自由的流量转发. 并且可以对流量的每个细节自定义.

相比iox实现了更复杂的流量处理, 不单纯是点对点的转发, 而是在点对点之间插入了agent, 在agent之间对流量隧道进行控制, 可以做到流量自定义加密混淆, wrapper各种功能.

## Feature

* 支持TCP, UDP, ICMP, HTTP, WebSocket, Wireguard, Unix, SMB, Memory 传输层
* 支持socks5/socks4, https/http, trojan, shadowsocks, neoreg, suo5 跳转代理
* 支持socks5/socks4, https/http, trojan, shadowsocks, CobaltStrike external C2 应用层协议
* 支持任意方向，任意信道的代理与端口转发
* 支持流量特征自定义与加密方式自定义
* 极简的命令行设计
* 全平台兼容

## docs

https://chainreactors.github.io/wiki/rem/usage/


## Similar or related works

* [frp](https://github.com/fatedier/frp) 最常使用, 最稳定的反向代理工具. 配置相对麻烦, 有一些强特征已被主流防护设备识别, 类似的还有nps, ngrok, rathole, spp.
* [gost](https://github.com/go-gost/gost) 一款强大的正向代理工具, v2版本不支持反向代理, v3开始支持, 未来可期.
* [iox](https://github.com/EddieIvan01/iox) 轻量但稳定的端口转发工具
* [xray](https://github.com/XTLS/Xray-core) 正向代理工具, 在协议的隐蔽性与性能上非常强大, 并拥有最好的密码学特性(向前加密, 无特征等)
