# remote-mfi-for-xcertplay

[English](README.md) | [中文](README.zh-CN.md)

将通过 CH341 USB-I2C 桥连接的实体 MFi 认证协处理器封装为 [xcertplay](https://github.com/shilapi/xcertplay) 的 Remote MFi HTTP API。服务内串行执行芯片操作，可供多台客户端共享。

**实验性版本：尚未完成真实 MFi 签名及 CarPlay `AA05 AuthenticationSucceeded` 验证。** Asuswrt-Merlin 和群晖 Container Manager 部署也待实测。

## 快速开始

需要 Linux `amd64` 或 `arm64`、CH341/MFi 设备，以及对应 `/dev/bus/usb` 节点的读写权限。先用 `lsusb` 确认 VID:PID，默认值为 `1a86:5512`。

从 [Releases](https://github.com/cuckoohello/remote-mfi-for-xcertplay/releases) 下载匹配 CPU 和 libc 的压缩包，用 `sha256sum -c` 校验配套 `.sha256` 文件后解压。产物为 `linux_{amd64,arm64}_{glibc,musl}` 四种组合，另加 `linux_arm64_merlin`，专为 Asuswrt-Merlin HND 5.02L 路由器（如 RT-AX86U 388.x）交叉编译，使用官方 `am-toolchains` 的 glibc 2.26 工具链；默认 glibc 基线为 2.35，musl 使用 Alpine 3.20 构建。

按发行版安装运行依赖：

```sh
# Debian / Ubuntu
sudo apt install libusb-1.0-0 tzdata
# RHEL / Rocky
sudo dnf install libusbx tzdata
# Alpine
sudo apk add libusb tzdata
```

启动解压出的程序：

```sh
export MFI_BEARER_TOKEN='替换为足够长的随机字符串'
export TZ=Asia/Shanghai
./remote-mfi-for-xcertplay
```

实际 USB 标识不同时，将 `MFI_CH341_USB_IDS` 设为实测 VID:PID。普通用户按[运维手册](docs/02-runbook.md#usb-权限)配置设备权限；宿主机 root 通常不需要 udev 权限规则。

Docker 镜像为 `ghcr.io/cuckoohello/remote-mfi-for-xcertplay:<release-tag>`，启动步骤见 [Docker 部署](docs/02-runbook.md#docker)。镜像以容器 root 运行，只需 `-v /dev/bus/usb:/dev/bus/usb` 和 `--device-cgroup-rule='c 189:* rmw'`。

HTTP 默认监听 `:8080`，仅本机访问时设置 `MFI_HTTP_ADDR=127.0.0.1:8080`。`MFI_BEARER_TOKEN` 可选，留空会关闭业务和诊断接口的鉴权，此时应使用隔离网络或 loopback。HTTPS 需外部代理提供。

## API

| Method | Path | 作用 |
| --- | --- | --- |
| `GET` | `/mfi/certificate` | 读取协议主版本、证书及 SHA-256；首次成功后进程内缓存，换设备须重启服务 |
| `POST` | `/mfi/sign` | 对 base64 challenge 签名；成功结果按 `requestId` 缓存 60 秒 |
| `POST` | `/mfi/reset` | 兼容 no-op，接受 `{}` |
| `GET` | `/debug/usb` | USB 与请求诊断，支持 HTML/JSON |
| `GET` | `/healthz` | 无鉴权的 USB/会话状态；需检查 JSON 中的 `ok` |

xcertplay 的 Remote MFi base URL 填 `http://HOST:8080`，token 与服务一致。浏览器打开 `http://HOST:8080/debug/usb?token=TOKEN` 查看诊断；健康状态 `ready` 不代表 MFi 签名已验证。

## 开发

需要 Go 1.23+、Make、C 编译工具链、`pkg-config` 和 libusb 开发头文件。

```sh
make check
make build
./remote-mfi-for-xcertplay --version
```

`make check` 执行单元测试、race detector 和 `go vet`，不需要 USB 硬件。本地、Docker 和 CI/release 共用 [Makefile](Makefile)；可覆盖 `OUTPUT`、`VERSION`、`COMMIT`、`BUILD_DATE`，默认分别为项目二进制名、`dev`、当前短 commit 和当前 UTC 时间。

## 文档

1. [API 契约](docs/01-api-contract.md)
2. [配置与运维](docs/02-runbook.md)
3. [测试与硬件验收](docs/03-acceptance-checklist.md)

Release 压缩包包含二进制、中英文 README 和许可证，其余文档见[仓库](https://github.com/cuckoohello/remote-mfi-for-xcertplay/tree/main/docs)。

## 开源许可

GPL-3.0-only。CH341 与 MFi 协议实现参考 GPL 授权的 xcertplay commit [`3ac55e3`](https://github.com/shilapi/xcertplay/tree/3ac55e3)。
