# 00 · 概览 (Overview)

> 版本: **v5.3** (baseline updated 2026-09-24)
> 语言/运行时: Go
> 交付形态: Docker 镜像 (GHCR, multi-arch) + 宿主机 binary (GitHub Releases)
> 支持架构: linux/amd64, linux/arm64
> 上游客户端 commit: [carplay](https://github.com/shilapi/xcertplay) `master @ 3ac55e3`

---

## 1. 一句话定位

把插在本机 USB 上的 **MFi 认证协处理器** (via CH341 USB-I2C 桥) 通过 3 个 HTTP 端点暴露成 "远程 MFi 服务",供 [RemoteMfiAuthenticationClient.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt) 消费。

---

## 2. 数据流概览

**多头单共享形态**: 一个 remote-mfi 服务后端可以同时被多台 Android head unit 使用。多头单并发请求属于**常规部署形态**,不是异常。

```
┌─────────────────────┐
│ head unit A         │───┐
└─────────────────────┘   │
┌─────────────────────┐   │  HTTP/JSON (Bearer 可选)     ┌────────────────────────────┐
│ head unit B         │───┼──────────────────────────▶ │  remote-mfi (this repo)    │
└─────────────────────┘   │                            │                            │
┌─────────────────────┐   │                            │  ┌──────────────────────┐  │
│ head unit N …       │───┘                            │  │ HTTP handler layer   │  │
└─────────────────────┘                                │  │  + Bearer auth       │  │
                                                       │  │  + requestId 幂等缓存 │  │
                                                       │  └────────┬─────────────┘  │
                                                       │           │                │
                                                       │  ┌────────▼─────────────┐  │
                                                       │  │  chipMutex (全局串行) │  │
                                                       │  │  + waitDeadline 8s   │  │
                                                       │  └────────┬─────────────┘  │
                                                       │           │                │
                                                       │  ┌────────▼─────────────┐  │
                                                       │  │  MFi 寄存器序列       │  │
                                                       │  │  (0x02/0x10/0x20/…)  │  │
                                                       │  └────────┬─────────────┘  │
                                                       │           │                │
                                                       │  ┌────────▼─────────────┐  │
                                                       │  │  CH341 libusb driver │  │
                                                       │  └────────┬─────────────┘  │
                                                       └───────────┼────────────────┘
                                                                   │ USB (VID:PID)
                                                        ┌──────────▼──────────┐
                                                        │  CH341 USB-I2C 桥    │
                                                        │  + MFi 协处理器      │
                                                        └─────────────────────┘
```

---

## 3. 目标 (Goals)

- G1: 与上游 [README.md#Remote MFI](https://github.com/shilapi/xcertplay/blob/3ac55e3/README.md) 定义的 3 端点契约 100% 兼容,不引入客户端改动。
- G2: 单一后端 — CH341 USB-I2C via **userspace libusb**,与客户端 [Ch341I2cTransport.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt) 的流协议对齐,便于双侧共用 encoder 逻辑。
- G3: 并发安全 — 多下游并行请求,芯片硬件必须严格串行访问。
- G4: 可运维 — 交付 Docker 镜像 (Alpine),提供网页化 USB 诊断入口 + `/healthz`。
- G5: 认证鉴权可选 — `MFI_BEARER_TOKEN` 未设或空则关闭鉴权;非空则强制校验 (`/healthz` 除外)。

---

## 4. 非目标 (Non-Goals) — 明确不做

| 项 | 原因 |
| --- | --- |
| BAA (Basic Apple Attestation) | 客户端 [README](https://github.com/shilapi/xcertplay/blob/3ac55e3/README.md) 说仅测过 BAA,但本服务定位是 "代理实体 MFi 芯片",BAA 是另一维度 |
| 原生 `/dev/i2c-N` (native I2C) | 用户明确要求 CH341-only |
| GPIO / USB 断电式硬件复位 | `/mfi/reset` 服务端为 **no-op** (v5.1),不清任何状态、不动物理芯片,详见 [02-api-contract.md#e3-post-mfireset](./02-api-contract.md#e3-post-mfireset) |
| TLS 终结 (HTTPS) | 由外层反向代理 (nginx / traefik / k8s ingress) 兜底 |
| 多芯片路由 / 多实例调度 | 一个进程绑定一个 CH341 会话,超出即另起容器 |
| 外部持久化 (Redis / DB) | 幂等缓存单进程内存即可,60s TTL |
| 客户端 / xcertplay 侧改动 | 上游作为只读契约来源,不 fork |

---

## 5. 术语表

| 术语 | 含义 |
| --- | --- |
| **MFi 协处理器** | Made-for-iPhone 认证芯片, 通过 I2C 提供 protocolMajor / certificate / signChallenge |
| **CH341** | 沁恒 USB-to-Serial/I2C/SPI/GPIO 桥芯片, VID:PID 常见 `1a86:5512` (**非固定**,参见 [Ch341DeviceMatcher.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341DeviceMatcher.kt)) |
| **requestId** | 客户端为每次 `signChallenge` 生成的 UUID, 重试时保持不变, **服务端幂等键** |
| **chipMutex** | 全局锁, 保护所有会碰 MFi 芯片的操作 (sign, certificate 读取) |
| **waitDeadline** | 请求等 chipMutex 的最长时间, 超时返回 503 |
| **诊断页** | `GET /debug/usb`,HTML(浏览器可读) + JSON(`Accept: application/json`) |

---

## 6. 版本与变更日志入口

- 本文档族版本: **v5.3** (2026-09-24 更新)
- 版本迭代:
  - **v5** — 精简版基线(移除 singleflight / maxQueue / 证书缓存 / stateMutex / 独立 debug JSON 端点)
  - **v5.1** — reset 改为 no-op(多头单场景避免误清幂等缓存)+ 诊断页 Recent Requests
  - **v5.2** — 修正健康度定义(去 `busy` 状态, 去"30s 时间戳"启发式);明确锁层级契约;完善诊断页 401 UX;`note=cached` 语义收敛到 sign;冷启动指标拆分;时区改为容器本地时区带 offset
  - **v5.3** — **多形态多架构交付**:公开 GitHub 仓库 + GHCR multi-arch 镜像(amd64/arm64) + GitHub Releases 宿主机 binary(4 变体: amd64/arm64 × glibc/musl);libusb 动态链接;宿主机形态**仅交付 binary**,用户自行装依赖 / 配 udev / 起 systemd
- 变更矩阵: 见 [04-runbook.md#变更矩阵](./04-runbook.md#5-变更矩阵)
- 验收基准: 见 [05-acceptance-checklist.md](./05-acceptance-checklist.md)
- 架构骨架: 见 [03-architecture.md](./03-architecture.md)
- 遗留问题: 见 [06-open-questions.md](./06-open-questions.md)
