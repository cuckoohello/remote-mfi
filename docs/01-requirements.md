# 01 · 需求与约束 (Requirements)

> 所有约束均可追溯到具体源码行号或验证依据, 拒绝拍脑袋。

---

## 1. 契约来源

| 项 | 来源 |
| --- | --- |
| HTTP 协议描述 | [carplay/README.md](https://github.com/shilapi/xcertplay/blob/3ac55e3/README.md) `Remote MFI` 章节 |
| 客户端实现 (行为参考) | [RemoteMfiAuthenticationClient.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt) |
| 客户端测试 (联调基准) | [RemoteMfiAuthenticationClientTest.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/test/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClientTest.kt) |
| 芯片寄存器时序 (行为参考) | [MfiAuthenticationClient.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt) |
| CH341 I2C 流协议 (编码器可复用) | [Ch341I2cStreamEncoder.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cStreamEncoder.kt), [Ch341I2cTransport.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt) |

**冻结 commit**: `master @ 3ac55e3`。任何后续更新前需重新对齐。

---

## 2. 客户端硬约束 (从源码扒出的字段级要求)

### 2.1 Base URL / 头 / 鉴权
| 约束 | 值 | 依据 |
| --- | --- | --- |
| 协议 | 仅 `http://` 或 `https://`, 末尾无 `/` | [RemoteMfiAuthenticationClient.kt#L34-L43](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L34-L43) |
| `Content-Type` | `application/json; charset=utf-8` | 同上 #L281 |
| `Accept` | `application/json; charset=utf-8` | 同上 #L223 |
| Bearer Token | 可选 (客户端 token 为空则不发 `Authorization` 头) | 同上 #L35 |
| Connect timeout | 5s (默认, 可覆盖) | 同上 #L282 |
| Read timeout | 10s (默认, 可覆盖) | 同上 #L283 |
| 最大尝试次数 | 2 (仅 GET/POST-sign 遵循; reset 关闭重试) | 同上 #L284 |

### 2.2 客户端重试策略 (服务端必须理解, 才能设计幂等)

| 状态码 | 是否重试 |
| --- | --- |
| 408 Request Timeout | ✅ 重试 |
| 429 Too Many Requests | ✅ 重试 |
| 5xx | ✅ 重试 |
| 其他 | ❌ 直接抛异常 |

**依据**: [RemoteMfiAuthenticationClient.kt#L272-L275](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L272-L275)。

**关键推论**: 服务端返回 5xx 时,同一 `requestId` 会被客户端**重新发送**。若服务端不做幂等,芯片将被触发两次签名,iAP2 状态机错乱。→ 见幂等策略章节。

### 2.3 响应体解析约束 (不易察觉,踩过就废)

客户端使用**正则表达式**扫描扁平 JSON 字段 ([RemoteMfiJson](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L302-L323))。这意味着:

- ❌ **禁止嵌套同名字段** — 响应体中若同名 key 出现两次 (即使嵌套在对象/数组里),正则会命中第一个,导致无法预期。
- ❌ 响应体上限 **2 MiB** (客户端 `MAXIMUM_HTTP_BODY_BYTES`, [#L288](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L288))。
- ❌ 单字段 base64 解码后上限 **65525 bytes** (`MAXIMUM_RESPONSE_BYTES`, [#L287](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L287))。
- ✅ 响应体建议 minified,一行,不含无关字段。

### 2.4 失败体格式
客户端在 non-2xx 时,尝试从响应体解析 `detail` 字段: [#L199-L202](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L199-L202)。**服务端所有错误响应必须为 `{"detail": "<人可读原因>"}`**,detail 非空。

---

## 3. MFi 芯片寄存器与时序 (服务端要复现的行为)

依据 [MfiAuthenticationClient.kt#L44-L120](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L44-L120)。

### 3.1 寄存器映射
| 寄存器 | 方向 | 宽度 | 含义 |
| --- | --- | --- | --- |
| `0x02` | R | 1B | `protocolMajor` |
| `0x05` | R | 1B | 错误码 (best-effort, 认证失败后读取) |
| `0x10` | R / W | 1B | Auth 控制/状态; 写 `0x01` 触发, 读到 `0x10` 表示 success |
| `0x11` | R | 2B (BE) | 签名长度 |
| `0x12+` | R | 变长 | 签名数据 (逐寄存器窗口读取) |
| `0x20` | W | 2B (BE) | challenge 长度 |
| `0x21` | W | 1..128B | challenge 数据 |
| `0x30` | R | 2B (BE) | 证书总长度 |
| `0x31, 0x32, …` | R | 每个 128B | 证书数据分片 (寄存器逐个递增) |

### 3.2 时序常量 (与客户端保持一致, 便于双端复用测试)
| 常量 | 值 | 用途 | 依据 |
| --- | --- | --- | --- |
| `INITIAL_AUTH_DELAY_MILLIS` | 10ms | 触发 auth (`0x10 ← 0x01`) 后首次读之前的 sleep | [#L243](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L243) |
| `AUTH_POLL_MILLIS` | 10ms | 轮询 `0x10` 的间隔 | [#L244](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L244) |
| `AUTH_TIMEOUT_MILLIS` | 3000ms | 签名总超时 | [#L245](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L245) |
| `IO_RETRY_TIMEOUT_MILLIS` | 2000ms | I/O 层 NACK 重试总窗口 | [#L241](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L241) |
| `IO_RETRY_DELAY_MICROS` | 20000µs | I/O 层重试间隔 | [#L242](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L242) |
| `CERTIFICATE_REGISTER_WINDOW_BYTES` | 128 | 每个证书数据寄存器返回的字节数 | [#L236](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L236) |
| `SELECT_TO_READ_GAP_MILLIS` | 5ms | CH341 STOP→START 空闲时长 (2.0C revision quirk) | [Ch341I2cTransport.kt#L225](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt#L225) |

### 3.3 寄存器读事务的物理形状 (CH341 层)

**每次 register read 实际上是两笔 I2C 事务** ([MfiAuthenticationClient.kt#L142-L157](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L142-L157)):

1. **Register select (write-only)**: 主机写 `<reg>` 一个字节, STOP。
2. **纯读事务** (write=空, read=N): CH341 需要在 STOP → START 间隙 ≥ 5ms (2.0C revision NACK quirk)。

**若失败要重试整对事务** — 只重试第二步会一直读到"旧"寄存器,注释 [#L146-L149](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L146-L149) 明确了这一点。

---

## 4. 内部抽象层要求 (服务端内部,不对外)

为了单测可跑 (你无法长时间与芯片保持连接),内部**保留** `I2cTransport` 接口:

```go
// 唯一接口, 唯一实现 (CH341+libusb), 唯一 fake (测试用)
type I2cTransport interface {
    // 一笔完整 I2C 事务:先写 writeData (可空),再读 readLength 字节 (可 0)
    // 若 writeData 空且 readLength>0,需在 STOP→START 间保 5ms 空闲
    Transaction(addr7 uint8, writeData []byte, readLen int) ([]byte, error)
}
```

**接口对齐依据**: [I2cTransport.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/I2cTransport.kt)。

**这不是过度工程**: 服务端后端唯一 (CH341),但没有此层就必须**接真芯片才能跑单测**,违背"迭代式先小规模验证"原则。

---

## 5. 并发与幂等硬性要求

### 5.1 芯片串行访问
MFi 协处理器是**共享有状态资源**,并行两次 signChallenge 会破坏 `0x10` 状态机 (先写 challenge → 触发 → 别的请求又写 challenge → 触发)。→ 服务端必须**全局串行**所有触碰芯片的操作。

**部署形态说明**: 一个 remote-mfi-for-xcertplay 服务后端支持**多台 head unit 共享**,多头单并发请求是常规场景(不是异常)。因此:
- `/mfi/sign` 与 `/mfi/reset` 并发**必然发生**(头单 A 正在 sign,头单 B 开局立即 reset)
- 服务端设计必须保证任意两个 endpoint 并发调用都是安全的
- **验证方法**: reset 语义为 no-op → 与任何端点并发都无竞争

### 5.2 幂等 (sign 专属)

**幂等 key**: `requestId` (客户端 UUID v4,同一次 `signChallenge()` 调用内保持不变,重试复用;不同 sign 调用之间**永不复用**,依据 [Client L94](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L94))。

| 场景 | 期望行为 |
| --- | --- |
| 同 `requestId` 60s 内二次到达 (客户端超时重试) | 直接返回缓存的 signature, **不再触发芯片** |
| 同 `requestId` 但 `challenge` 内容变了 | 返回 400 `{"detail":"requestId reuse with different challenge"}` (防误用) |
| 60s 后同 `requestId` 再到达 | 视为新请求, 正常触发芯片 |
| `requestId` 缺失或非 UUID | 400 `{"detail":"requestId is required"}` |

**幂等缓存过期机制**: **唯一途径是 60s TTL 自然过期**。任何 endpoint(包括 `/mfi/reset`)**都不主动清理**缓存 —— 主动清会误伤别的头单正在等待重试的 requestId。

### 5.3 背压
- 请求进入等 `chipMutex` 的窗口为 **8s** (hardcoded);超时返回 503 `{"detail":"chip busy, retry"}`。
- **不设 maxQueue** — 8s 自然背压足够;真出现问题再基于数据调整。

### 5.4 `/mfi/reset` 语义(v5.1 修订)

**服务端为纯 no-op**:
- **不清幂等缓存**(会误伤别的头单)
- **不清证书缓存**(v5 起服务端就没缓存)
- **不动芯片寄存器 / GPIO**
- **不抢 chipMutex**
- 仅记一行访问日志,立即返回 `{"detail":""}`

**推理链**:
1. 客户端 [CarPlayController.kt#L423-L443](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/orchestration/CarPlayController.kt#L423-L443) 仅在会话建立时调一次 reset,紧接着 GET /certificate。
2. 客户端 `.reset()` 本身清的是**客户端**的 `certificateInfo` 缓存,服务端配合与否不影响客户端行为。
3. 服务端如清幂等缓存,可能误抹别的头单未完成重试的 requestId → 芯片被触发两次 → iAP2 状态机风险。
4. 所以最安全的实现是**什么都不做**。

### 5.5 锁层级契约(v5.2 新增)

实现包含业务层 3 把锁和 transport 内部 2 把锁。为避免死锁/优先级反转，必须遵循**单向锁顺序**:

| 锁 | 保护对象 | 临界区特征 | 允许持有时间 |
| --- | --- | --- | --- |
| `chipGate` (buffered channel) | MFi 芯片 I2C 事务 | 长(最坏 3s 级) | 一次完整芯片操作 |
| `cacheMutex` (`sync.RWMutex`) | requestId → signature 幂等 map | 极短(map lookup / insert) | ≤ 微秒级 |
| `recentMutex` (`sync.Mutex`) | Recent Requests 环形缓冲 | 极短(数组切片赋值) | ≤ 微秒级 |
| `transport.ioMu` (`sync.Mutex`) | CH341 stream configure/write/read 整体 | 一次 I2C transaction | transaction 完成 |
| `transport.sessionMu` (`sync.Mutex`) | libusb handle 的打开/失效/关闭 | 极短;首次 claim 除外 | handle 操作完成 |

**唯一允许的嵌套方向**:

```
chipGate → cacheMutex
chipGate → transport.ioMu → transport.sessionMu
```

**handler 顺序**:
```
handler
  ├─ 拿 cacheMutex (RLock) → 查缓存 → 释放
  ├─ 拿 chipGate (deadline=8s) → 拿 cacheMutex 二次检查(短暂持有) → 释放 cacheMutex
  │    → 执行 I2C 事务(transport 内部按 ioMu → sessionMu) → 拿 cacheMutex 写入 → 释放 → 释放 chipGate
  └─ 拿 recentMutex → 追加一条 → 释放
```

**明确禁止**:
- ❌ 持有 `cacheMutex` / `recentMutex` 时再申请 `chipGate`
- ❌ 持有 `chipGate` 时拿 `recentMutex`
- ❌ 持有 `cacheMutex` 时做 I2C(会长时间阻塞其他 handler 查缓存)
- ❌ transport 反向调用 biz/httpapi 或申请上层锁
- ❌ `cacheMutex` / `recentMutex` 临界区里做网络 / USB / 文件 IO
- ❌ `cacheMutex` / `recentMutex` 临界区里等待 goroutine channel

**验证方法**: `-race` build + F 组并发压测,任何 data race 直接判 fail。

---

## 6. USB 诊断要求

### 6.1 端点
| 端点 | 内容协商 | 用途 |
| --- | --- | --- |
| `GET /debug/usb` | Accept 不含 `application/json` → HTML;否则 JSON | 浏览器 / 脚本 |
| `GET /healthz` | 固定 JSON | HEALTHCHECK / k8s readiness |

### 6.2 页面必备信息
- **顶部 banner**: 芯片状态 `READY / MISSING / ERROR (+reason)`(**v5.2 去 `BUSY`**,与 /healthz 3 态对齐;忙碌度看 Runtime 区块),附启动至今时长与当前本地时间
- **USB 设备表格** (由 libusb 枚举): Bus/Device 编号、VID:PID、Manufacturer 字符串、Product 字符串、Speed、Class;匹配 CH341 候选清单的行加标签 `CANDIDATE CH341` 高亮;**若无任何 USB 设备**显示提示行 `no USB devices enumerated`
- **Runtime 区块**: `chipMutex` 当前持有者(空闲显示 `idle`)、当前锁持有时长(如占用中)、幂等缓存条目数
- **Recent Requests 区块** (v5.1 引入 / v5.2 补语义): 内存环形缓冲,最近 **20 条**,新的在上,固定 6 列:
  | 列 | 内容 |
  | --- | --- |
  | Time | 请求进入 handler 时间戳,毫秒精度,**容器本地时区带 offset**(如 `2026-09-24T18:22:03.142+08:00`) |
  | Method | `GET` / `POST` |
  | Path | 命中的路由 |
  | Status | HTTP 响应码 |
  | ms | 服务端总处理耗时(含 chipMutex 等待) |
  | note | 类别标签: `chip` / `idempotent-hit` / `noop` / `chip busy` / `unauthorized` / `bad request` / `error` |

  **note 语义细化(v5.2)**:
  - `chip` — 请求走了完整 I2C 事务
  - `idempotent-hit` — **仅 `/mfi/sign`** 命中 60s 幂等缓存, 未触碰芯片 (v5.2 更名, 前称 `cached`, 避免让读者误以为证书也有缓存)
  - `noop` — `/mfi/reset` 服务端 no-op
  - `chip busy` — 等 chipMutex 超时 (503)
  - `unauthorized` / `bad request` / `error` — 各类失败
- 页头 `<meta http-equiv="refresh" content="3">`(3s 自动刷新,不引入 JS)

**Recent Requests 收录约束**:
- 收录 `/mfi/certificate`, `/mfi/sign`, `/mfi/reset` 三个业务端点
- **不收录** `/debug/usb` 与 `/healthz` 自身访问 (否则页面刷新会挤掉有效记录)
- 不含 IP / UA / body / requestId —— 页面是运维观察窗,不是审计日志;审计需求由 slog stdout 承担
- 重启即丢,不持久化

### 6.3 诊断页 401 UX(v5.2 新增)

设了 `MFI_BEARER_TOKEN` 但客户端未提供 token 时,`/debug/usb` **HTML 分支**返回一个**人类可读的引导页**(而不是空白或 JSON):

- Status: `401`
- `Content-Type: text/html; charset=utf-8`
- 页面内容:
  1. 大字提示 `Authorization required`
  2. 说明当前服务开启了 Bearer Token
  3. 两种接入方式示例:
     - `curl -H "Authorization: Bearer <token>" http://.../debug/usb`
     - 浏览器地址栏: `http://.../debug/usb?token=<token>`
  4. **不泄露** token 值本身或提示 token 长度

JSON 分支(`Accept: application/json`)仍返扁平 `{"detail":"unauthorized"}`,给脚本用。

### 6.4 CH341 VID:PID **不硬编码**
[Ch341DeviceMatcher.kt#L7-L10](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341DeviceMatcher.kt#L7-L10) 明确说明"没有内置 VID/PID,部署侧自行识别"。→ 服务端通过环境变量 `MFI_CH341_USB_IDS` 传入候选清单 (逗号分隔 `vid:pid`),默认 `1a86:5512`。

---

## 7. 健康度要求(v5.2 修订)

### 7.1 判定规则(仅 3 态,与"忙碌度"正交)

| 状态 | 判定 | 复杂度 |
| --- | --- | --- |
| `ready` | libusb 枚举到 `MFI_CH341_USB_IDS` 匹配的设备 **且** 上一次尝试打开 handle 未失败 | 一次快速枚举 (< 1ms) |
| `missing` | libusb 枚举**无**匹配设备 | 同上 |
| `error` | 有匹配设备但**当前 handle 无法打开/claim**(权限拒绝、被其他进程占用、libusb IO 错) | 同上 |

**关键决策(v5.2)**:
- ❌ 删除 `busy` 状态 —— 忙碌 ≠ 不健康,长 sign 正在跑不应触发 Docker 自动重启。忙碌度信息挪到 `/debug/usb` Runtime 区块。
- ❌ 删除"最近 30s 内有成功操作"启发式规则 —— 存在漏洞: 芯片被拔且无请求进入时状态永远滞留 ready。
- ✅ 健康度**只**基于**当前时刻的 USB 枚举结果**,同步、物理、无歧义。
- ✅ 与诊断页 USB 表格采用同一枚举结果,两者数据永不发散。

### 7.2 启动 probe(可选,不影响 healthz 定义)
启动时做**一次** protocolMajor 读取,仅用于**产出一行 info/warn 日志**便于运维快速看到"上电时芯片是否响应"。**probe 结果不进入健康度判定**(健康度只看 USB 枚举 + handle 状态)。probe 失败**不 exit**。

---

## 8. 非功能要求(v5.2 拆分冷启动指标 / v5.3 多形态多架构)

| 项 | 目标 |
| --- | --- |
| Docker 镜像大小 | ≤ 40MB **每架构**(amd64 与 arm64 各自) |
| 宿主机 binary 大小 | ≤ 20MB(动态链接 libusb,单文件) |
| 支持架构 | linux/amd64, linux/arm64 (不支持 armv7) |
| 冷启动到 **HTTP 端口可 accept** | ≤ 2s |
| 冷启动到 **`/healthz` 返回 `chip:"ready"`**(芯片正常插入时) | ≤ 3s |
| P50 sign 延迟 (**幂等缓存命中**) | < 500µs (map lookup + JSON encode; **不是 5ms**) |
| P50 sign 延迟 (首次真实过芯片) | < 200ms (含 I2C 事务 + USB round-trip) |
| P99 sign 延迟 (等锁 + 芯片) | < 8s (等于 waitDeadline) |
| 日志格式 | slog JSON, 单行, 字段固定 `event / reqId / status / chip_wait_ms / duration_ms` |
| 日志级别 | `MFI_LOG_LEVEL` 环境变量 `debug/info/warn/error`,默认 `info` |
| Recent Requests 时间戳 | **容器/宿主本地时区 + ISO 8601 offset**(如 `2026-09-24T18:22:03.142+08:00`),便于运维现场直读,不用心里换算 UTC |
| 容器时区 | `TZ` 环境变量控制,默认 `Asia/Shanghai`(可覆盖为 `UTC` 或其他);宿主机 binary 读 `TZ` 或 `/etc/localtime` |
| libusb 链接 | 动态链接 `libusb-1.0.so.0`;Docker 镜像内已装,宿主机形态由用户装(见 [Runbook §1.1](./04-runbook.md#1-部署前置人工单)) |
