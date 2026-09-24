# 04 · 操作手册 (Runbook)

> 本文档是**部署与运维的唯一权威**。任何跨系统操作按本文档执行,不接受口口相传。
>
> **v5.3 起支持两种部署形态**: **Docker(推荐)** 与 **宿主机直跑 binary**。两种形态共享同一份 Go 源码,仅打包方式不同。

---

## 0. 快速导航

- [1. 部署前置人工单](#1-部署前置人工单)
- [2. 镜像构建 (multi-arch)](#2-镜像构建-multi-arch)
- [3. 启动命令模板](#3-启动命令模板) —— [3.1~3.3 Docker](#31-docker--基础版-推荐-生产) / [3.4 宿主机 binary](#34-宿主机-binary-\u76f4\u8dd1)
- [4. 环境变量清单](#4-环境变量清单)
- [5. 变更矩阵](#5-变更矩阵)
- [6. 诊断页使用姿势](#6-诊断页使用姿势)
- [7. 失败即停 SOP](#7-失败即停-sop)
- [8. 回滚方案](#8-回滚方案)

---

## 1. 部署前置人工单

### 1.1 宿主机确认清单 (逐项打勾, 不接受"应该没问题")

| # | 检查项 | 命令 | 期望 |
| --- | --- | --- | --- |
| 1 | Linux 内核 ≥ 4.9 | `uname -r` | `4.9.x` 以上 |
| 2 | Docker Engine ≥ 20.10 | `docker version` | Server 版本 ≥ 20.10 |
| 3 | USB 子系统就绪 | `ls /dev/bus/usb/` | 至少一个 Bus 目录 |
| 4 | CH341 已插入且被识别 | `lsusb \| grep -i 1a86` | 至少一行 (VID:PID **实测确认**, 可能非 `5512`) |
| 5 | CH341 未被内核 CDC-ACM/CDC-Serial 占用 | `dmesg \| tail -30 \| grep -i ch341` | 无 `ch341 attached` 类日志 |
| 6 | (如已装) `i2c-ch341-usb` 未加载 | `lsmod \| grep ch341` | **无输出** (确认走 userspace 路径) |
| 7 | 宿主机时间已同步 | `timedatectl` (systemd 机) | `System clock synchronized: yes` |
| 8 (仅宿主机形态) | CPU 架构确认 | `uname -m` | `x86_64` 或 `aarch64` (armv7 不支持) |
| 9 (仅宿主机形态) | libc 类型确认 | `ldd --version 2>&1 \| head -1` | glibc 类返回 `... GLIBC ...`;Alpine 返回 `musl ...`。选对应 tarball |
| 10 (仅宿主机形态) | libusb-1.0 已装 | `ldconfig -p \| grep libusb-1.0` (glibc) 或 `find / -name libusb-1.0.so* 2>/dev/null` (musl) | 至少一行 `libusb-1.0.so.0` |

**任何一项失败 → 停下写工单,不要自行猜测继续**。

**宿主机形态额外说明**:
- Debian/Ubuntu: `sudo apt install libusb-1.0-0`
- RHEL/CentOS/Rocky: `sudo dnf install libusbx`(注意包名与库名不一致)
- Alpine: `sudo apk add libusb`
- 树莓派 OS 64-bit(Debian 系): 同 Debian/Ubuntu

### 1.2 udev 规则 (仅一次)

**目标**: 让容器内非 root 用户 (`mfi`) 能访问 CH341 USB 节点。

写入 `/etc/udev/rules.d/50-mfi-ch341.rules`:

```
# 允许 plugdev 组访问 CH341 (userspace libusb) 设备
SUBSYSTEM=="usb", ATTR{idVendor}=="1a86", ATTR{idProduct}=="5512", MODE="0660", GROUP="plugdev", TAG+="uaccess"
```

> ⚠️ 如果 `lsusb` 报告的 CH341 PID **不是 `5512`** (常见变种 `5523`),同步修改 `idProduct` 行,并在 `MFI_CH341_USB_IDS` 环境变量里加入实际 VID:PID。

生效:
```sh
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=usb
# 拔插一次 CH341 让规则重新应用
```

### 1.3 关键值一览 (拷贝进人工单)

| 字段 | 精确值 | 来源 |
| --- | --- | --- |
| CH341 常见 VID | `1a86` | 沁恒官方 |
| CH341 常见 PID | `5512` (I2C mode);`5523` 见于部分改板 | 参考 [Ch341DeviceMatcher.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341DeviceMatcher.kt) 明确 "no built-in VID/PID" — **必须实测** |
| MFi 芯片 I2C 7-bit 地址 | `0x11` (默认,拉高 CH341 RST 后选中) | [Ch341I2cTransport.kt#L88-L108](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt#L88-L108) 注释 |
| Docker USB 权限 | `-v /dev/bus/usb:/dev/bus/usb` + `--device-cgroup-rule='c 189:* rmw'` + 宿主 `plugdev` 数字 GID | libusb 入口 + Docker device cgroup + 文件权限三者都必须满足 |
| 容器监听端口 | `8080` (默认) | 见 `MFI_HTTP_ADDR` |
| chipMutex 等锁超时 | 8s (硬编码) | 见 [02-api-contract.md](./02-api-contract.md#幂等并发背压总结-v5) |
| 幂等缓存 TTL | 60s (硬编码) | 覆盖客户端 2 次重试的时间窗口 |

---

## 2. 镜像构建 (multi-arch)

### 2.1 约束
- 必须 `docker buildx` — 单一 `docker build` 不支持多架构 manifest
- `gousb` 需要 cgo, `CGO_ENABLED=1`;交叉编译通过 **QEMU emulate 目标架构 native 编译** 而非手工 cross-toolchain
- libusb 版本 **pin 死**, 防止 apk 升级触发 ABI 不兼容
- 目标 platform: **`linux/amd64` 与 `linux/arm64`**(不支持 armv7)

### 2.2 Dockerfile 骨架(示例,不落文件,由 Codegen 阶段落地)

同一份 Dockerfile 通过 buildx 出两架构;`TARGETPLATFORM` 自动被 buildx 注入:

```dockerfile
# ============ build ============
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETPLATFORM
ARG TARGETARCH
RUN apk add --no-cache build-base pkgconfig libusb-dev=1.0.27-r0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=$TARGETARCH GOFLAGS='-trimpath' \
    go build -ldflags='-s -w' -o /out/remote-mfi ./cmd/remote-mfi

# ============ runtime ============
FROM alpine:3.20
RUN apk add --no-cache libusb=1.0.27-r0 ca-certificates tzdata \
 && addgroup -S mfi && adduser -S -G mfi -H -s /sbin/nologin mfi
COPY --from=build /out/remote-mfi /usr/local/bin/remote-mfi

USER mfi
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8080/healthz | grep -q '"ok":true' || exit 1
ENTRYPOINT ["/usr/local/bin/remote-mfi"]
```

### 2.3 构建 & 推送命令

```sh
# 首次准备 buildx(仅一次)
docker buildx create --name remote-mfi-builder --use
docker buildx inspect --bootstrap

# 多架构 build & push 到 GHCR(推荐:CI 走此路径)
docker buildx build \
  --platform=linux/amd64,linux/arm64 \
  -t ghcr.io/cuckoohello/remote-mfi:v0.1.0 \
  -t ghcr.io/cuckoohello/remote-mfi:latest \
  --push .

# 本地测试(单架构 load 到本地 daemon,无需 push)
docker buildx build --platform=linux/amd64 -t remote-mfi:dev --load .
```

### 2.4 镜像 manifest 验证
```sh
docker buildx imagetools inspect ghcr.io/cuckoohello/remote-mfi:v0.1.0
# 期望输出含 linux/amd64 + linux/arm64 两个 sub-image
```

### 2.5 单架构镜像大小自检
```sh
docker image ls remote-mfi:dev
# 期望: SIZE ≤ 40MB (每架构独立)
```

### 2.6 宿主机 binary 构建(GitHub Releases 用)

宿主机 binary 与 Docker 镜像**共用同一份 Go 源码**,只是打包方式不同。产物矩阵:

| tarball | 目标平台 | 兼容宿主机 | libc |
| --- | --- | --- | --- |
| `remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz` | linux/amd64 | Debian 11+, Ubuntu 20.04+, RHEL 8+ | glibc ≥ 2.31 |
| `remote-mfi_v0.1.0_linux_amd64_musl.tar.gz`  | linux/amd64 | Alpine 3.16+ | musl |
| `remote-mfi_v0.1.0_linux_arm64_glibc.tar.gz` | linux/arm64 | Debian 11+ arm64, 树莓派 OS 64-bit | glibc ≥ 2.31 |
| `remote-mfi_v0.1.0_linux_arm64_musl.tar.gz`  | linux/arm64 | Alpine 3.16+ arm64 | musl |

**构建方式**(在 CI 里用 Docker 容器 native 编 → `docker cp` 出产物 → 打 tarball):

```sh
# glibc/amd64 举例
docker run --rm --platform=linux/amd64 \
  -v $(pwd):/src -w /src \
  golang:1.23-bookworm \
  bash -c 'apt update && apt install -y libusb-1.0-0-dev pkg-config && \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" \
    -o /src/dist/linux_amd64_glibc/remote-mfi ./cmd/remote-mfi'

# musl/amd64
docker run --rm --platform=linux/amd64 \
  -v $(pwd):/src -w /src \
  golang:1.23-alpine \
  sh -c 'apk add --no-cache build-base pkgconfig libusb-dev && \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" \
    -o /src/dist/linux_amd64_musl/remote-mfi ./cmd/remote-mfi'

# arm64 变体: 把 --platform 换成 linux/arm64, 目录后缀改 arm64
```

产物打包:
```sh
cd dist/linux_amd64_glibc && \
  tar czf ../remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz remote-mfi README.md LICENSE
```

**产物内容**(tarball 展开后):
- `remote-mfi` — 单文件 binary,动态链接 libusb-1.0
- `README.md` — 快速起步(含 libusb 装法 + udev 规则 + 启动示例)
- `LICENSE`

### 2.7 GitHub Releases 发布规范

- Tag: `v0.1.0`(语义化版本)
- 附件: 4 个 tarball + 4 个 SHA256 校验文件(`.tar.gz.sha256`)
- 镜像 tag: `ghcr.io/cuckoohello/remote-mfi:v0.1.0` + `:latest`(仅 Release 时 latest 才动)
- Release notes: 引用本仓库 `docs/` 内文档族版本(v5.x)

---

## 3. 启动命令模板

### 3.1 Docker · 基础版 (推荐, 生产)

```sh
USB_GID="$(getent group plugdev | cut -d: -f3)"

docker run -d \
  --name remote-mfi \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MFI_BEARER_TOKEN='REPLACE_ME_LONG_RANDOM_STRING' \
  -e MFI_CH341_USB_IDS='1a86:5512' \
  -e MFI_LOG_LEVEL='info' \
  --device-cgroup-rule='c 189:* rmw' \
  -v /dev/bus/usb:/dev/bus/usb \
  --group-add "$USB_GID" \
  ghcr.io/cuckoohello/remote-mfi:v0.1.0
```

### 3.2 精确设备版 (推荐, 但 BUS/DEVICE 会随拔插变)

先用 `lsusb` 找到 CH341 精确位置:
```sh
$ lsusb | grep 1a86
Bus 001 Device 007: ID 1a86:5512 QinHeng Electronics ...
```

然后:
```sh
USB_GID="$(getent group plugdev | cut -d: -f3)"

docker run -d \
  --name remote-mfi \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MFI_BEARER_TOKEN='REPLACE_ME_LONG_RANDOM_STRING' \
  --device=/dev/bus/usb/001/007 \
  --group-add "$USB_GID" \
  ghcr.io/cuckoohello/remote-mfi:v0.1.0
```

⚠️ 拔插 USB 后 Device 编号可能变,需重启容器或改回 `-v /dev/bus/usb:/dev/bus/usb`。

### 3.3 Docker · 无鉴权版 (仅内网 loopback 使用, 明确风险)

```sh
USB_GID="$(getent group plugdev | cut -d: -f3)"

docker run -d --name remote-mfi \
  -p 127.0.0.1:8080:8080 \      # 仅 loopback
  --device-cgroup-rule='c 189:* rmw' \
  -v /dev/bus/usb:/dev/bus/usb \
  --group-add "$USB_GID" \
  ghcr.io/cuckoohello/remote-mfi:v0.1.0
# MFI_BEARER_TOKEN 未设 → 启动日志会 WARN 一行 "authentication disabled"
```

### 3.4 宿主机 binary 直跑

**适用场景**: 无 Docker、嵌入式盒子、树莓派、开发调试。**用户自行负责依赖装配、udev、进程管理**。

#### 3.4.1 一次性下载 & 校验
```sh
# 1. 从 GitHub Releases 下载对应 tarball
#    先确认 arch 与 libc:
uname -m                                           # x86_64 或 aarch64
ldd --version 2>&1 | head -1                       # glibc 或 musl

# 2. 假设是 amd64 + glibc:
curl -LO https://github.com/cuckoohello/remote-mfi/releases/download/v0.1.0/remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz
curl -LO https://github.com/cuckoohello/remote-mfi/releases/download/v0.1.0/remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz.sha256

# 3. 校验
sha256sum -c remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz.sha256

# 4. 展开
tar xzf remote-mfi_v0.1.0_linux_amd64_glibc.tar.gz
sudo mv remote-mfi /usr/local/bin/
sudo chmod +x /usr/local/bin/remote-mfi
```

#### 3.4.2 前置依赖(**用户自装**)
- **libusb-1.0**:见 [1.1 前置清单](#1-部署前置人工单) 各发行版对应包名
- **udev 规则**:同 [1.2](#12-udev-规则-仅一次),但把 `GROUP=plugdev` 换成运行 `remote-mfi` 的用户所在组
- **用户权限**:运行用户必须能访问 `/dev/bus/usb/**`,或者加入 plugdev 组:
  ```sh
  sudo usermod -aG plugdev $USER
  # 重新登录使组生效
  ```

#### 3.4.3 前台运行(开发调试用)
```sh
export MFI_BEARER_TOKEN='REPLACE_ME_LONG_RANDOM_STRING'
export MFI_CH341_USB_IDS='1a86:5512'
export MFI_LOG_LEVEL=info
export TZ=Asia/Shanghai
remote-mfi
```
观察日志无 error 后, Ctrl+C 停止。

#### 3.4.4 生产运行(**用户自己写 systemd unit**)

本项目**不交付** systemd unit 模板 —— 每台机器的用户/组/日志路径不同,统一模板反而添乱。参考实现:

```ini
# /etc/systemd/system/remote-mfi.service
[Unit]
Description=Remote MFi Authentication Service
After=network.target

[Service]
Type=simple
User=mfi
Group=plugdev
Environment=MFI_BEARER_TOKEN=REPLACE_ME
Environment=MFI_CH341_USB_IDS=1a86:5512
Environment=MFI_LOG_LEVEL=info
Environment=TZ=Asia/Shanghai
ExecStart=/usr/local/bin/remote-mfi
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```
启用:
```sh
sudo systemctl daemon-reload
sudo systemctl enable --now remote-mfi
sudo journalctl -u remote-mfi -f
```

#### 3.4.5 宿主机形态的差异汇总

| 项 | Docker 形态 | 宿主机 binary 形态 |
| --- | --- | --- |
| libusb 装配 | 镜像内已装 | **用户自装** |
| udev 规则 | 宿主机装,容器 `--group-add` 生效 | 宿主机装, 运行用户需在 group 内 |
| 进程管理 | Docker 自带重启 | **用户自建 systemd 或类似** |
| 日志采集 | `docker logs` | stdout → journald / syslog / 文件重定向 |
| 时区来源 | `TZ` 环境变量 | `TZ` 或 `/etc/localtime` (Go 会读) |
| 用户/权限 | 容器内 `mfi` 用户 (Dockerfile 定义) | 由 systemd unit `User=` 指定 |
| 端口冲突 | 由 `-p` 隔离 | 与宿主机其他服务共享,注意冲突 |
| 升级 | 拉新镜像 → 重启容器 | 覆盖 binary → `systemctl restart` |

---

## 4. 环境变量清单

| 变量 | 默认 | 必需 | 说明 |
| --- | --- | --- | --- |
| `MFI_HTTP_ADDR` | `:8080` | ❌ | HTTP 监听地址 |
| `MFI_BEARER_TOKEN` | (空) | ❌ | 空则关闭鉴权,启动日志 WARN;非空则 `/mfi/*` 和 `/debug/*` 均强制校验 |
| `MFI_CH341_USB_IDS` | `1a86:5512` | ❌ | 候选 CH341 VID:PID 清单,逗号分隔,`vid:pid` 全小写 hex |
| `MFI_MFI_I2C_ADDRESS` | `0x11` | ❌ | MFi 协处理器 7-bit I2C 地址 (十六进制或十进制) |
| `MFI_CH341_I2C_SPEED_KHZ` | `100` | ❌ | I2C 时钟, 可选 `20/100/400/750` (与 [Ch341I2cSpeed](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cStreamEncoder.kt#L4-L9) 对齐) |
| `MFI_LOG_LEVEL` | `info` | ❌ | `debug/info/warn/error` |
| `MFI_LOG_FORMAT` | `json` | ❌ | `json`(推荐) 或 `text` (仅调试) |
| `TZ` | `Asia/Shanghai` | ❌ | 容器时区,影响 Recent Requests 与日志时间戳。v5.2 明确使用本地时区 + ISO offset,便于运维现场直读 |

**hardcoded, 不暴露** (v5 决策 — 避免拍脑袋的可调开关):
- chipMutex waitDeadline: 8s
- 幂等缓存 TTL: 60s
- Docker HEALTHCHECK 间隔: 10s (在 Dockerfile 里)
- **v5.2 移除**: "最近成功时间戳窗口 30s"(健康度不再基于时间启发式)

如后续有数据支持要调,再走变更矩阵。

---

## 5. 变更矩阵

任何字段/路径/环境变量的变更,必须走此表。

| 字段 / 路径 / 变量 | 前 | 后 | 原因 | 影响 |
| --- | --- | --- | --- | --- |
| 基础镜像 | (n/a) | `alpine:3.20` | 用户要求 Alpine | 镜像体积 ≤ 40MB;libc 为 musl,不能直接跑 glibc 二进制 |
| Go 后端 | (n/a) | Go 1.23 + `github.com/google/gousb` (cgo) | 单一 CH341 后端 | 需要 build-base + libusb-dev + pkgconfig 三个 apk 包 |
| I2C 后端 | (设计初期含 native) | **仅 CH341 userspace libusb** | 用户明确移除 native | 无需 `--device=/dev/i2c-N`;Docker 挂 `/dev/bus/usb` |
| API 端点数 | (设计初期 6 个) | **5 个** (`/debug/usb.json` 合并进 `/debug/usb` 内容协商) | v5 精简 | 客户端无影响 (客户端不用诊断接口) |
| `MFI_BEARER_TOKEN` | (设计初期"强制") | **可选 (空则关闭鉴权)** | 用户要求 | Runbook 必须显式提示"共享网络需非空",默认部署脚本填占位符 |
| singleflight | (设计中曾引入) | **移除** | 60s 缓存 + 双重检查锁足够 | 少一个依赖 `golang.org/x/sync` |
| `MFI_MAX_QUEUE` 变量 | (设计中曾引入) | **移除** | 8s waitDeadline 已经是自然背压 | 环境变量列表精简 |
| certificate 服务端缓存 | (设计中曾引入) | **移除, 每次现读** | 客户端自缓存,服务端再缓有热插拔陈旧数据风险 | reset 逻辑简化,不用清证书缓存 |
| `stateMutex` 概念 | (设计中曾独立) | **合并**,单锁保护幂等缓存 map (`sync.RWMutex`) | 简化心智 | 无 |
| `/healthz` 鉴权 | (设计中曾强制) | **移除, 保持无鉴权** | 健康检查必须便宜,零敏感信息 | 容器 HEALTHCHECK 命令行不需要传 token |
| libusb 版本 | (未 pin) | `pin =1.0.27-r0` | 防止 apk 升级触发 ABI 不兼容 | Alpine 版本升级时需同步验证 |
| `/mfi/reset` 服务端语义 | 清幂等缓存 + 释放"最近成功"标记 | **纯 no-op** (v5.1) | 多头单共享部署下清缓存会误伤别的头单等待重试的 requestId,触发芯片重复签名 | 幂等缓存仅靠 60s TTL 过期;reset 变得与所有端点并发安全,可从"未定义行为"降级为"常规路径" |
| healthz `chip.status` 枚举 | v5.1: `ready/busy/missing/error` (4 态, 含时间启发式) | v5.2: **`ready/missing/error` (3 态)**, 仅基于当前 libusb 枚举 | `busy` 混淆"忙碌 ≠ 不健康"会触发 Docker 自动重启;"30s 成功启发式"在拔芯片后无请求时会滞留 ready | HEALTHCHECK 更精确;`/debug/usb` 与 healthz 使用同一次枚举,数据不发散 |
| 锁层级契约 | (未明文) | v5.2 明确 3 把锁(chipMutex / cacheMutex / recentMutex)禁止两两嵌套 | 避免死锁 / 优先级反转,让 `-race` 测试有明确 pass/fail 判据 | 见 [01-requirements.md#5.5](./01-requirements.md#55-锁层级契约v52-新增) |
| Recent Requests `note=cached` | v5.1: `cached` | v5.2: **`idempotent-hit`** (更名) | 原名让读者误以为证书也有缓存,与 v5"不缓存证书"矛盾 | 语义收敛到 sign 专属;文档不再需要额外脚注 |
| 时间戳时区 | v5.1: UTC | v5.2: **容器本地时区 + ISO 8601 offset** | 运维现场读 UTC 需心里换算 CST,易错 | `TZ` 环境变量控制,默认 `Asia/Shanghai` |
| 冷启动指标 | v5.1: "冷启动到 /healthz 就绪 ≤ 2s"(定义模糊) | v5.2: **拆两条** — "HTTP 端口 accept ≤ 2s" + "healthz 返 ready ≤ 3s" | 原表述"就绪"未定义指端口还是芯片就绪 | 验收明确 |
| P50 sign 缓存命中目标 | v5.1: < 5ms | v5.2: **< 500µs** | 5ms 是 50× 宽度;map lookup + JSON encode 实际应 < 100µs | 更接近真值,便于压测发现异常 |
| 诊断页 401 UX | v5.1: 直接返 JSON 或空白 | v5.2: HTML 分支返**引导页**,示例两种 token 传法 | 运维用浏览器打开 401 会一头雾水 | 页面自解释,不暴露 token 值 |
| 交付形态 | v5.2: 仅 Docker 镜像 (Alpine) | v5.3: **Docker (multi-arch amd64/arm64, GHCR) + 宿主机 binary (4 变体 amd64/arm64 × glibc/musl, GitHub Releases)** | 实际部署包括嵌入式盒子/树莓派等无 Docker 场景 | Runbook 分 Docker/宿主机两种流程;镜像必须走 buildx;binary 需 4 份 tarball + sha256 |
| CPU 架构支持 | (未明说) | v5.3: **linux/amd64 + linux/arm64**(**不支持 armv7**) | 覆盖服务器 + 树莓派 64-bit; armv7 已过时且用户群小 | 构建矩阵翻倍;测试需 QEMU emulate arm64 |
| libusb 链接方式 | v5.2: Alpine 镜像内装 | v5.3: **动态链接** (Docker 内 apk / 宿主机 apt/dnf/apk) | 静态链接 cgo+musl 复杂度高;动态更简洁 | 宿主机形态用户需自装 libusb-1.0 |
| 镜像 registry | (未指定) | **GHCR** (`ghcr.io/cuckoohello/remote-mfi`) | 与 GitHub Actions 集成,公开仓库无速率限制 | 客户端 `docker pull` 无需登录 |
| 宿主机形态交付 | (无) | v5.3: 仅 **binary + README + LICENSE** tarball,**不含** systemd unit / udev rules / install.sh | 各发行版差异大,统一模板反而添乱 | Runbook §3.4 给出参考 systemd unit,但由用户自建 |

---

## 6. 诊断页使用姿势

### 6.1 浏览器 (最常用)
- 无鉴权部署:
  ```
  http://<host>:8080/debug/usb
  ```
- 带 Bearer token:
  ```
  http://<host>:8080/debug/usb?token=<MFI_BEARER_TOKEN>
  ```
- 页面每 3 秒自动刷新;插拔 CH341 时可实时观察 `CANDIDATE CH341` 行变化。

### 6.2 curl / 脚本 (JSON)
```sh
curl -s -H "Accept: application/json" \
     -H "Authorization: Bearer $MFI_BEARER_TOKEN" \
     http://localhost:8080/debug/usb | jq .
```

### 6.3 关键判读

| 现象 | 结论 |
| --- | --- |
| USB 表里能看到 `1a86:5512` 但 `chip.status = "error"` | CH341 被别的进程 claim,或权限不够;查 `dmesg`、`lsof /dev/bus/usb/**` |
| USB 表里看不到 `1a86:*` | 硬件未插入,或 udev 未生效;`docker exec` 进容器 `ls /dev/bus/usb` 校对 |
| `runtime.lockHeldMs` 长期不归 0 | 某个 sign 卡在芯片上;等 8s 后自动 503;若持续多分钟视为 hang → 见 [7. 失败即停 SOP](#7-失败即停-sop) |
| `runtime.cacheEntries` 单调递增到 > 1000 | 有攻击/循环重放;检查上游头单是否 requestId 未换新 |
| `chip.status = "ready"` 但 healthz 报 `ok:false` | 不可能(v5.2 起两者同源, 出现即代码 bug) |
| Recent Requests 里 `chip busy` 短时间内暴增 | chipMutex 长期被独占;检查是否有慢 sign 请求或死锁 |

---

## 7. 失败即停 SOP

**原则**: 任何超出预期的信号都必须停下取证,不允许自动重启掩盖。

| 现象 | 立即动作 | 取证 |
| --- | --- | --- |
| 启动后 30s 内 `/healthz` 从未返回 `chip:"ready"` | 停容器 (不要 restart 循环掩盖) | `docker logs remote-mfi`, `lsusb`, `dmesg`, `/debug/usb` HTML 截图 |
| `chipMutex` 持续持有 > 60s | 停容器 | 保留最近 5 条 op 日志 + goroutine dump (若已开 pprof) |
| 出现 `panic:` 或 `data race:` | 停容器 | 完整 log,`GORACE=1` 复现 |
| USB device 名字变了但服务未感知 | 停容器 | 检查是否用了精确 `--device=`;改用 `-v /dev/bus/usb:/dev/bus/usb` |
| 客户端报 `certificateSha256 does not match` | 停 sign 流量 | 服务端 debug log (含证书原始 hex),检查是否**中间人**或多个 CH341 混乱 |
| 单个 sign 请求耗时 > 3s (芯片总超时) | 观察, 若连续 3 次触发即停 | debug log,`0x05` 错误码 |

---

## 8. 回滚方案

### 8.1 镜像回滚
```sh
# 前提: 上一版镜像 tag 保留 (推荐每次 tag 语义版本)
docker stop remote-mfi && docker rm remote-mfi
docker run -d --name remote-mfi <相同参数> remote-mfi:0.0.9
```
**验证**: `curl http://localhost:8080/healthz` 返回 `chip:"ready"`。

### 8.2 完全下线 (紧急)
```sh
docker stop remote-mfi && docker rm remote-mfi
# 下游客户端会立刻走 IOException 分支,建议同时通知上游改回本地 MFi
```

### 8.3 配置回滚 (仅环境变量)
```sh
docker stop remote-mfi && docker rm remote-mfi
# 用旧的环境变量重新 docker run,不需要改镜像
```

### 8.4 udev 规则回滚
```sh
sudo rm /etc/udev/rules.d/50-mfi-ch341.rules
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=usb
```

### 8.5 回滚验证 checklist
- [ ] `docker ps` 显示旧版本运行中
- [ ] `curl /healthz` 返 `chip:"ready"`
- [ ] `curl /debug/usb` 能看到 CH341
- [ ] 一次真实 sign 请求成功 (可用 `RemoteMfiAuthenticationClientTest` 里的 fake challenge)
- [ ] 一次 reset 请求成功

---

## 9. 常见运维手法 (备忘录)

### 9.1 容器内看 USB
```sh
docker exec -it remote-mfi sh -c 'ls /dev/bus/usb/*/'
```

### 9.2 抓一次完整交易 (debug 日志)
```sh
docker stop remote-mfi
docker run --rm -it -e MFI_LOG_LEVEL=debug ... ghcr.io/cuckoohello/remote-mfi:v0.1.0
# 触发一次客户端 sign, 观察 event=chip_tx / chip_rx
```

### 9.3 排查权限
```sh
docker exec -it remote-mfi id
# 期望: uid=xxx(mfi) gid=xxx(mfi) groups=xxx(mfi),plugdev
```
