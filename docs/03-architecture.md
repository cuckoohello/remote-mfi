# 03 · 架构 (Architecture)

> 版本: v5.3
> 定位: 从**第一性原理**出发,把 [01-requirements.md](./01-requirements.md) 的约束翻译成 Go 项目结构。
> **本文档描述"如何实现",不描述"实现细节"**。真正的实现在代码里,不在这份 Markdown 里。

---

## 1. 分层模型

服务是**一个物理串行资源(MFi 芯片)前面挂个 HTTP 适配器**。围绕这个本质,分 4 层:

```
┌────────────────────────────────────────────────────────────────┐
│  Layer 4: HTTP                                                 │
│  - net/http mux                                                │
│  - Bearer auth middleware (可选)                                │
│  - Recent Requests 记录 middleware                              │
│  - handler: certificate / sign / reset / debug / healthz       │
└─────────────────────────┬──────────────────────────────────────┘
                          │ 调用 biz 接口, 完全不感知芯片细节
┌─────────────────────────▼──────────────────────────────────────┐
│  Layer 3: biz (业务逻辑, chipService)                            │
│  - chipMutex (sync.Mutex, waitDeadline 8s)                     │
│  - cacheStore (sync.RWMutex, 60s TTL, requestId 幂等)           │
│  - 双重检查锁模式                                                │
│  - 决定何时触发 chip 层                                          │
└─────────────────────────┬──────────────────────────────────────┘
                          │ 调用 chip 层, 不感知 USB/寄存器
┌─────────────────────────▼──────────────────────────────────────┐
│  Layer 2: chip (MFi 寄存器协议, chipDriver)                      │
│  - 寄存器映射: 0x02/0x05/0x10/0x11/0x12/0x20/0x21/0x30/0x31+    │
│  - 时序常量: 10ms initial, 10ms poll, 3s timeout                │
│  - 证书分页读 (128B window)                                     │
└─────────────────────────┬──────────────────────────────────────┘
                          │ 调用 transport 接口, 不感知 CH341
┌─────────────────────────▼──────────────────────────────────────┐
│  Layer 1: transport (I2cTransport)                             │
│  - 唯一接口: Transaction(addr7, wr, readLen) ([]byte, error)   │
│  - 实现 A: CH341+libusb (gousb)                                │
│  - 实现 B: fakeTransport (仅测试用)                              │
└────────────────────────────────────────────────────────────────┘
                          │
                          ▼
                    物理 CH341 USB-I2C 桥 + MFi 芯片
```

**层次间的依赖方向**: 上层 → 下层,**下层永远不 import 上层**。这是让"用 fakeTransport 跑单测"成立的唯一前提。

---

## 2. Go 项目目录结构

```
remote-mfi/
├── cmd/
│   └── remote-mfi/
│       └── main.go            # 极薄, 只做 wire-up: 读 env → 建 layers → ListenAndServe
├── internal/                  # 全部内部包, 禁止外部 import
│   ├── config/
│   │   └── config.go          # 环境变量解析 + 校验
│   ├── httpapi/
│   │   ├── router.go          # net/http mux + middleware chain
│   │   ├── auth.go            # Bearer token middleware (常量时间比较)
│   │   ├── recent.go          # Recent Requests 环形缓冲 (recentMutex)
│   │   ├── errors.go          # {"detail":"..."} 统一响应
│   │   ├── handler_certificate.go
│   │   ├── handler_sign.go
│   │   ├── handler_reset.go   # no-op, 仅日志
│   │   ├── handler_debug.go   # HTML/JSON 内容协商, 401 引导页
│   │   ├── handler_healthz.go # 无鉴权 3 态
│   │   └── templates/         # go:embed 静态模板
│   │       ├── debug.html.tmpl
│   │       └── unauthorized.html.tmpl
│   ├── biz/
│   │   ├── service.go         # chipService: 对外暴露 Certificate/Sign/Reset
│   │   ├── cache.go           # requestId → signature 幂等缓存 (cacheMutex)
│   │   ├── lock.go            # chipMutex + waitDeadline 语义
│   │   └── health.go          # chip.status 计算 (基于 transport 的 Enumerate/Handle 状态)
│   ├── chip/
│   │   ├── driver.go          # MFi 寄存器序列 (protocolMajor/certificate/signChallenge)
│   │   └── registers.go       # 常量表 (0x02 等), 与文档 01-req#3.1 对齐
│   ├── transport/
│   │   ├── transport.go       # I2cTransport 接口定义
│   │   ├── ch341.go           # gousb 实现: stream encoder + bulk R/W
│   │   ├── ch341_encoder.go   # I2C stream 编码 (对应 xcertplay Ch341I2cStreamEncoder.kt)
│   │   └── fake.go            # 测试用 fakeTransport
│   ├── obs/
│   │   ├── log.go             # slog JSON 单行, 固定字段
│   │   └── uptime.go          # 启动时间戳, 供 /debug/usb 用
│   └── ver/
│       └── ver.go             # 编译期注入的 version / commit / buildDate
├── docs/                      # 本文档族
├── .github/
│   └── workflows/             # (第三轮落地时再写)
├── go.mod
├── go.sum
├── LICENSE
└── README.md                  # 面向 GitHub 首页, 引导到 docs/
```

**几个关键约束**:
- `internal/` 保证包不被外部 import,清晰的对内契约边界
- 每层的**接口**定义在该层自己的包里,**实现**也在该层;上层通过接口消费,不 import 具体类型
- `cmd/remote-mfi/main.go` **纯 wire-up**,零业务逻辑;有些人主张再拆一个 `app.go`,我认为对本项目规模是过度设计
- **不建 `pkg/` 目录**:这个项目的所有代码都是"服务内部",没有可复用到外部的公共库

---

## 3. 关键接口(仅签名,实现留代码)

### 3.1 `transport.I2cTransport`

```go
package transport

type I2cTransport interface {
    // Transaction 执行一笔完整的 I2C 事务:
    //   1. 若 writeData 非空: 起始条件 + 写地址(W) + 写 writeData + STOP
    //   2. 若 readLen > 0: (如上一步已 STOP) 空闲 ≥5ms + 起始条件 + 写地址(R) + 读 readLen 字节 + STOP
    //
    // 语义与 xcertplay 客户端 I2cTransport.kt 完全一致。
    //
    // 返回的 []byte 长度必须严格等于 readLen; 否则返 Protocol error。
    // 错误必须是 *TransportError 之一 (NACK / DeviceUnavailable / Protocol / Timeout / InvalidRequest).
    Transaction(addr7 uint8, writeData []byte, readLen int) ([]byte, error)

    // Close 释放 USB session (仅 CH341 实现需要;fake 可空实现)
    Close() error
}
```

### 3.2 `chip.Driver`

```go
package chip

type Driver interface {
    // ProtocolMajor 读寄存器 0x02
    ProtocolMajor(ctx context.Context) (uint8, error)

    // ReadCertificate 从 0x30(长度)→ 0x31+(数据窗口) 分页读取整份证书
    ReadCertificate(ctx context.Context) ([]byte, error)

    // SignChallenge 完整签名序列:
    //   1. 写 0x20 = challenge 长度 (2B BE)
    //   2. 写 0x21 = challenge 数据
    //   3. 写 0x10 = 0x01 (触发)
    //   4. sleep 10ms, 轮询 0x10 直到 == 0x10 (成功) 或 3s 超时
    //   5. 读 0x11 = signature 长度
    //   6. 读 0x12+ = signature 数据
    // 若超时/失败, best-effort 读 0x05 错误码填入 err。
    SignChallenge(ctx context.Context, challenge []byte) (signature []byte, err error)
}
```

### 3.3 `biz.ChipService`

```go
package biz

type ChipService interface {
    // Certificate 每次调用现读芯片 (v5 决策: 服务端不缓存)
    Certificate(ctx context.Context) (protocolMajor uint8, certificate []byte, err error)

    // Sign 走完整幂等流程:
    //   1. cacheMutex.RLock -> lookup(requestId) -> unlock
    //   2. 命中 & challenge 一致 -> return signature, note=idempotent-hit
    //   3. 命中 & challenge 不一致 -> return ErrRequestIdReuse
    //   4. chipMutex.LockWithDeadline(8s) -> 超时 return ErrChipBusy
    //   5. 双重检查缓存
    //   6. driver.SignChallenge
    //   7. 写 cache, 释放 chipMutex
    Sign(ctx context.Context, requestId string, challenge []byte) (signature []byte, cached bool, err error)

    // Reset v5.1 no-op: 仅记日志, 什么都不清
    Reset(ctx context.Context) error

    // Health 供 handler_healthz / handler_debug 共用
    Health(ctx context.Context) HealthStatus
}

type HealthStatus struct {
    Chip    string // "ready" / "missing" / "error"
    Reason  string // 空或人类可读原因
    VidPid  string // "1a86:5512", ready 时才有意义
}
```

### 3.4 sentinel errors(错误层级)

```go
package biz

var (
    ErrChipBusy         = errors.New("chip busy, retry")               // -> 503
    ErrChipMissing      = errors.New("chip missing")                   // -> 503
    ErrRequestIdReuse   = errors.New("requestId reuse with different challenge") // -> 400
    ErrChallengeSize    = errors.New("challenge must be 1..128 bytes") // -> 400
    ErrRequestIdFormat  = errors.New("requestId must be a UUID")       // -> 400
)
```

HTTP handler 用 `errors.Is` 判定,不 sniff error 字符串。**内部错误(chip I/O)wrapped**,handler 层判断 `errors.Is(err, ErrChipMissing)` 决定 5xx vs 500;失败响应 `detail` 直接取 `err.Error()`,**不含 stacktrace**。

---

## 4. 依赖矩阵

### 4.1 Go 模块

| 依赖 | 用途 | 备选 & 拒绝理由 |
| --- | --- | --- |
| **stdlib**: `net/http`, `log/slog`, `sync`, `context`, `time`, `crypto/subtle`, `encoding/base64`, `encoding/hex`, `crypto/sha256`, `html/template`, `embed` | HTTP 服务 / 并发原语 / 常量时间比较 / 编解码 / 模板 | — |
| `github.com/google/gousb` | libusb-1.0 的 Go 绑定, cgo | 拒绝 `github.com/gotmc/libusb`: 纯 cgo binding 更薄, 社区活跃度低; gousb 是 Google 官方封装, 与客户端 [Ch341I2cTransport.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt) 概念对齐 |
| `github.com/google/uuid` | UUID v4 校验(仅 Parse, 不生成) | stdlib 无 UUID 类型; regex 校验能替代但语义模糊 |

**不引入**:
- ❌ 任何 HTTP 框架 (chi/gin/echo) — stdlib mux 够用, 一个 middleware 链手写就是几十行
- ❌ 任何 JSON schema 库 — 请求体字段少, 手写解析 + 明确错误更清晰
- ❌ 任何 metrics/tracing 库 — slog 已经覆盖了本服务规模需要的观测
- ❌ singleflight — v5 已推理砍掉
- ❌ 任何 DI 框架 — 服务体量不需要

### 4.2 系统依赖

| 依赖 | 目标 | 提供方 |
| --- | --- | --- |
| `libusb-1.0.so.0` | gousb 底层 | Docker: `apk add libusb`;宿主机: 用户自装 |
| `libc` | glibc 或 musl,与 binary 变体匹配 | 宿主机 |
| udev rules | CH341 设备节点权限 | Runbook §1.2 |

---

## 5. 并发模型与锁层级

严格遵循 [01-requirements.md §5.5 锁层级契约](./01-requirements.md#55-锁层级契约v52-新增)。

### 5.1 goroutine 分类

| goroutine | 数量 | 用途 |
| --- | --- | --- |
| main | 1 | 起 HTTP server, 阻塞 `srv.ListenAndServe` |
| HTTP handler | N (net/http per-request) | 一次请求一个 |
| cache GC | 1 | 每 15s 遍历 cache 清理 TTL 过期 entry |
| signal handler | 1 | SIGTERM/SIGINT → `srv.Shutdown(ctx)` |

**不起额外后台线程**: 无 `libusb hotplug callback` 独立 goroutine — hotplug 检测只在 handler 或 healthz 里同步做一次 enumerate,不引入订阅式复杂度。

### 5.2 chipMutex 的 `LockWithDeadline` 实现要点

Go 的 `sync.Mutex` 没有带 deadline 的 `TryLock`。实现思路(不落代码,只讲设计):

```
buffered chan(1) 作为信号量:
  lock:   ch <- struct{}{}
  unlock: <-ch

带 deadline 的 lock:
  select {
    case ch <- struct{}{}: 拿到锁
    case <-time.After(8s): return ErrChipBusy
    case <-ctx.Done():     return ctx.Err()   // HTTP 请求已取消
  }
```

**为什么用 chan 而不是 sync.Mutex + timer**: 前者是 Go idiomatic 的可取消原语,后者要 goroutine leak 处理更复杂。

### 5.3 cacheMutex 的临界区形状

**关键约束**: 临界区里**只做 map lookup / insert**,**不做**任何芯片调用,不做任何日志/网络/文件。

```
cache.Get(requestId):
  cacheMutex.RLock()
  defer cacheMutex.RUnlock()
  entry, ok := m[requestId]
  return entry, ok    // 严格微秒级
```

TTL 清理走**独立 GC goroutine**,间隔 15s(TTL 的 1/4);不在 hot path 做 lazy delete,避免拉长 lookup。

### 5.4 recentMutex 的临界区形状

- 环形缓冲用**固定长度切片 + 下一个写入 index**,append 操作 O(1)
- HTTP `/debug/usb` 读取时**拷贝整个切片**再释放锁,避免长临界区

---

## 6. 生命周期

```
main:
  1. config.Load()  ← 解析 & 校验环境变量; 任何缺失/非法直接 fatal exit
  2. transport.NewCh341(cfg)  ← 只做参数校验, 不立即 open USB
  3. chip.NewDriver(transport, i2cAddr)
  4. biz.NewChipService(driver, cfg)
  5. srv := httpapi.NewServer(service, cfg)
  6. probe (best-effort): 尝试读一次 protocolMajor, 打 info/warn 日志; 失败不 exit
  7. go handleSignal(srv)   ← SIGTERM/SIGINT
  8. go cacheGC(service)    ← 15s 遍历清 TTL
  9. srv.ListenAndServe()   ← 阻塞
 10. 收到 signal → srv.Shutdown(ctx=10s)  → 等 in-flight handler 完成 → transport.Close()
```

**故障不 exit 原则**(见 [01-req §7.2](./01-requirements.md#72-启动-probe可选不影响-healthz-定义)):
- USB 枚举不到芯片 → 不 exit
- probe 读寄存器失败 → 不 exit
- libusb 短暂 IO 错 → 不 exit,下次请求自动 retry
- **只有** config 非法 / 端口占用 / OOM 等系统级错才 exit

---

## 7. USB 会话 & handle 生命周期

CH341 USB handle 需要独占持有 → 服务启动后一直持有到 process 退出。但**芯片可能中途被拔**:

- **策略**: **lazy re-open** — 每次事务前判 `handle == nil`,若是则尝试 `libusb_open`。失败返 `ErrChipMissing`,handle 保持 nil,下次再试。
- **不做**: hotplug callback。让 healthz / handler 自然驱动 handle 重建,代码路径唯一,状态机简单。

### 7.1 handle 状态转换

```
     ┌──────────────┐  Transaction()  ┌──────────────┐
     │ handle=nil   │────────────────>│ try open     │
     │  (init/lost) │<────fail────────│              │
     └──────────────┘                 └──────┬───────┘
             ▲                               │ success
             │                               ▼
             │           I/O error   ┌──────────────┐
             └───────────────────────│ handle=open  │
                                     └──────────────┘
```

- **I/O error 触发 handle 释放**: 只要遇到 `LIBUSB_ERROR_NO_DEVICE` / `LIBUSB_ERROR_IO`,立即 `handle.Close()` 并置 nil,让下次 lazy re-open 重建。
- **handle mutex**: `handleMutex` 保护 `handle *gousb.Device` 的读写,与 chipMutex 独立;但**顺序上** chipMutex 外层,handleMutex 内层(嵌套持有仅在 transport 内部允许,不跨层)。

⚠️ 这条特例更新了 [01-req §5.5 锁层级契约](./01-requirements.md#55-锁层级契约v52-新增) 隐含的"三锁不嵌套"表述——严格说 transport 内部有一把额外的 `handleMutex`,它仅存在于 chipMutex 临界区内。**这是 v5.3 遗漏的补充**,记入 [06-open-questions.md](./06-open-questions.md#o1-锁层级契约的-transport-例外)。

---

## 8. 测试策略

分三层,各自要求不同环境:

| 层 | 测试对象 | 依赖 | 频率 |
| --- | --- | --- | --- |
| **单元测试** | `biz/*`, `chip/*`, `httpapi/*`(用 `fakeTransport`) | 纯 Go, 无外设 | 每次 `go test ./...` |
| **集成测试** | `transport/ch341*` | 真 CH341 + 真 MFi 芯片 | 手动触发, 有硬件时跑 |
| **端到端测试** | 完整服务 + xcertplay 客户端 | 真硬件 + 真 iPhone / CarPlay | 手动, release 前 |

**关键设计**:
- `fakeTransport` 支持**脚本化**:预设一串 (writeExpected, readReturn) tuple,按顺序回放。用于测试寄存器序列正确性(如 sign 完整流程 6 步)。
- **race 强制开启**: `go test -race ./...` 是 CI 门槛,任何 race 报告 fail。
- **不做** mock 生成器(mockery/gomock) — 3 个接口手写 fake 更清晰。

---

## 9. 构建 & 发布流水线概览

**不落 CI yaml,只讲设计**。真正的 workflow 文件在第三轮再落。

```
GitHub Actions (workflow: release.yml, 由 tag v* 触发)
├── job: docker-multi-arch
│    - setup-buildx
│    - docker/login-action → GHCR
│    - buildx build --platform=linux/amd64,linux/arm64 --push
│         → ghcr.io/cuckoohello/remote-mfi:$TAG + :latest
├── job: host-binary (matrix: amd64/arm64 × glibc/musl)
│    - docker run --platform=... golang:...-{bookworm|alpine}
│    - cgo + libusb-dev, go build
│    - upload-artifact: remote-mfi_<tag>_linux_<arch>_<libc>.tar.gz + .sha256
└── job: release
     - needs: [docker-multi-arch, host-binary]
     - download-artifact all
     - softprops/action-gh-release: 创建 GitHub Release, 附 4 个 tarball + sha256
```

**PR 门槛**(workflow: ci.yml, 由 push/PR 触发):
- `go vet ./...`
- `go test -race ./...`
- `go build` 单架构(冒烟)
- `golangci-lint run`
- 可选: markdown lint(检查 docs/ 链接有效性)

**签名策略**(暂缓,记入开放问题):
- cosign 对镜像签名?
- Release 附件是否 gpg 签名?
- v0.1.0 先不做,记 [06-open-questions.md#o3-supply-chain-signing](./06-open-questions.md#o3-supply-chain-signing) 里。

---

## 10. 关键取舍与决策记录

| 取舍点 | 选择 | 备选 | 决策理由 |
| --- | --- | --- | --- |
| HTTP 框架 | stdlib `net/http` | chi / echo / gin | 5 个 endpoint 手写 mux 更简单; 引入框架增加依赖与心智负担 |
| USB 库 | `google/gousb` (cgo) | 纯 Go `github.com/gotmc/libusb`, 或直接调 kernel usbfs ioctl | gousb 是 Google 官方封装, 稳定活跃; 纯 Go 方案生态小; 直接 ioctl 需要重写 CH341 stream 协议 |
| I2C stream 编码 | 从 xcertplay Kotlin 版翻成 Go | 参考其他开源 CH341 库(Python `pyCH341`) | 直翻已验证过的 Kotlin 版避免踩坑;引用同一注释和常量便于对照 |
| 幂等实现 | 60s TTL map + 双重检查锁 | singleflight / Redis | singleflight 语义不完全匹配(它在飞时合并,但过后立刻失效); Redis 是外部依赖过度 |
| 健康度 | 3 态,基于当前 libusb 枚举 | 时间启发式,或 fold `busy` 进 unhealthy | v5.2 已推理排除, 见 [01-req §7.1](./01-requirements.md#71-判定规则仅-3-态与忙碌度正交) |
| reset 语义 | 纯 no-op | 清幂等缓存 / GPIO 复位 | v5.1 已推理排除, 见 [01-req §5.4](./01-requirements.md#54-mfireset-语义v51-修订) |
| 日志库 | stdlib `log/slog` | zap / zerolog | slog 是 Go 1.21+ 官方方案, 性能足够, 无第三方依赖 |
| 时间戳格式 | 本地时区 + ISO 8601 offset | UTC / Unix ms | v5.2 决策, 便于运维现场直读 |
| 部署形态 | Docker(multi-arch) + host binary(4 变体) | 只 Docker / 只 binary / 加 deb 包 | v5.3 决策 |
| CI 平台 | GitHub Actions | GitLab CI / Drone / 自建 | 与 GitHub Releases 深度集成, 支持 buildx + QEMU |

---

## 11. 与文档族其他文件的关系

- **[00-overview.md](./00-overview.md)** — 顶层定位与目标
- **[01-requirements.md](./01-requirements.md)** — 硬约束来源(客户端/芯片/并发/健康度)
- **[02-api-contract.md](./02-api-contract.md)** — HTTP 契约,`httpapi/` 包严格按此实现
- **[04-runbook.md](./04-runbook.md)** — 部署运维,`cmd/remote-mfi/main.go` 读的环境变量都定义在这
- **[05-acceptance-checklist.md](./05-acceptance-checklist.md)** — 逐项打勾式验收,`internal/*` 每个包都对应几条 checklist
- **[06-open-questions.md](./06-open-questions.md)** — 本文档中所有"暂缓 / 未定义"决策的集中记录

**本文档职责边界**: 架构骨架 + 关键接口 + 取舍记录。**不写**具体函数签名、字段名、struct 布局 — 那些属于代码。
