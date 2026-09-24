# remote-mfi

[English](README.md) | [中文](README.zh-CN.md)

`remote-mfi` 将通过 CH341 USB-I2C 桥连接的实体 MFi 认证协处理器封装为 HTTP API，供 [shilapi/xcertplay](https://github.com/shilapi/xcertplay) 的 Remote MFi 客户端调用。

服务支持 Linux `amd64` 与 `arm64`，既可使用多架构 Docker 镜像，也可直接在宿主机运行动态链接的二进制文件。

## API

| Method | Path | 作用 |
| --- | --- | --- |
| `GET` | `/mfi/certificate` | 从 MFi 芯片读取协议主版本和证书 |
| `POST` | `/mfi/sign` | 对 base64 challenge 签名；同一 `requestId` 在 60 秒内幂等 |
| `POST` | `/mfi/reset` | xcertplay 建立远程会话时调用的兼容 no-op |
| `GET` | `/debug/usb` | 只读 USB、运行状态和最近请求诊断页，支持 HTML/JSON |
| `GET` | `/healthz` | 无鉴权的三态健康检查：`ready`、`missing`、`error` |

所有访问芯片的操作都经过全局串行控制。多台 xcertplay 设备同时请求时，不会交叉执行同一 MFi 芯片的寄存器序列。

冻结的字段级协议见 [docs/02-api-contract.md](docs/02-api-contract.md)。

## 运行要求

- Linux `amd64` 或 `arm64`
- CH341 USB-I2C 桥及其连接的 MFi 认证协处理器
- 通过 `/dev/bus/usb` 暴露 CH341
- `libusb-1.0`（Docker 镜像已包含；宿主机 binary 需单独安装）
- udev 规则允许运行用户访问目标 CH341 VID:PID

默认 USB 标识为 `1a86:5512`。不同硬件可能不同，必须先用 `lsusb` 实测，再通过环境变量覆盖。

```udev
# /etc/udev/rules.d/50-mfi-ch341.rules
SUBSYSTEM=="usb", ATTR{idVendor}=="1a86", ATTR{idProduct}=="5512", MODE="0660", GROUP="plugdev", TAG+="uaccess"
```

重新加载规则并重新插拔设备：

```sh
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=usb
```

## Docker 运行

公开的多架构镜像发布在 GHCR：

```sh
docker pull ghcr.io/cuckoohello/remote-mfi:v0.1.1
```

为了支持 USB 热插拔，需要挂载整个 USB bus，并放行 USB 字符设备主设备号 `189`。容器以非 root 用户运行，因此还要传入宿主机 `plugdev` 的数字 GID：

```sh
USB_GID="$(getent group plugdev | cut -d: -f3)"

docker run -d \
  --name remote-mfi \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MFI_BEARER_TOKEN='替换为足够长的随机字符串' \
  -e MFI_CH341_USB_IDS='1a86:5512' \
  --device-cgroup-rule='c 189:* rmw' \
  --group-add "$USB_GID" \
  -v /dev/bus/usb:/dev/bus/usb \
  ghcr.io/cuckoohello/remote-mfi:v0.1.1
```

`MFI_BEARER_TOKEN` 是可选项。未设置时，`/mfi/*` 和 `/debug/usb` 均不鉴权，只应在隔离网络或仅监听 loopback 时使用。`/healthz` 始终不鉴权。

浏览器打开 `http://HOST:8080/debug/usb?token=TOKEN`，可查看 USB 设备、芯片状态、锁占用、幂等缓存数量和最近 20 条业务请求。

## 宿主机 Binary

从 GitHub Release 下载同时匹配 CPU 架构和 libc 的压缩包：

- `linux_amd64_glibc`
- `linux_amd64_musl`
- `linux_arm64_glibc`
- `linux_arm64_musl`

glibc 产物基于 Debian 12 构建，要求宿主机 glibc 2.36 或更高版本。

安装运行时依赖：

```sh
# Debian / Ubuntu
sudo apt install libusb-1.0-0

# RHEL / Rocky / CentOS
sudo dnf install libusbx

# Alpine
sudo apk add libusb
```

启动服务：

```sh
export MFI_BEARER_TOKEN='替换为足够长的随机字符串'
export MFI_CH341_USB_IDS='1a86:5512'
export MFI_MFI_I2C_ADDRESS='0x11'
export MFI_CH341_I2C_SPEED_KHZ='100'
remote-mfi
```

宿主机产物动态链接 libusb。运行前可用 `ldd ./remote-mfi` 确认所选产物与宿主机 libc 匹配，并能解析 `libusb-1.0.so.0`。

## 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MFI_HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `MFI_BEARER_TOKEN` | 空 | 可选的共享 Bearer Token |
| `MFI_CH341_USB_IDS` | `1a86:5512` | 逗号分隔的小写十六进制 `vid:pid` 候选列表 |
| `MFI_MFI_I2C_ADDRESS` | `0x11` | MFi 协处理器 7-bit I2C 地址 |
| `MFI_CH341_I2C_SPEED_KHZ` | `100` | 可选 `20`、`100`、`400`、`750` |
| `MFI_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `MFI_LOG_FORMAT` | `json` | `json` 或 `text` |
| `TZ` | `Asia/Shanghai` | 日志和诊断页使用的时区 |

## 本地开发

需要 Go 1.23+、C 编译工具链、`pkg-config` 和 libusb 开发头文件。

```sh
make check
make build
./remote-mfi --version
```

`make check` 会执行单元测试、race detector 和 `go vet`。测试使用脚本化 transport，不依赖 USB 硬件。CH341 集成测试以及真实 CarPlay `AA05 AuthenticationSucceeded` 验收仍需实体硬件。

## 文档

- [项目概览](docs/00-overview.md)
- [需求与约束](docs/01-requirements.md)
- [API 契约](docs/02-api-contract.md)
- [技术架构](docs/03-architecture.md)
- [部署与运维手册](docs/04-runbook.md)
- [验收清单](docs/05-acceptance-checklist.md)
- [开放问题](docs/06-open-questions.md)

## 开源许可

GPL-3.0-only。CH341 与 MFi 协议实现参考 GPL 授权的 xcertplay commit [`3ac55e3`](https://github.com/shilapi/xcertplay/tree/3ac55e3)。
