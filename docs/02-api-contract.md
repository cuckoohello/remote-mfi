# 02 · API 契约 (Frozen Contract)

> 端点数 (v5.4, API 契约沿用 v5.2): **5** — `/mfi/certificate`, `/mfi/sign`, `/mfi/reset`, `/debug/usb`, `/healthz`
> 冻结基准: [carplay/README.md](https://github.com/shilapi/xcertplay/blob/3ac55e3/README.md) `Remote MFI` 章节 @ `master 3ac55e3`。
> **任何字段修改都视为破坏性变更**,需要客户端同步升级。

---

## 通用约定

| 项 | 值 |
| --- | --- |
| 请求 `Accept` | `application/json; charset=utf-8` |
| 请求 `Content-Type` (有 body 时) | `application/json; charset=utf-8` |
| 响应 `Content-Type` | `application/json; charset=utf-8` (诊断页 HTML 例外) |
| 鉴权 (可选) | `Authorization: Bearer <MFI_BEARER_TOKEN>` — token 未设/空时跳过校验 |
| 响应体格式 | **扁平 JSON,一行 minified,禁止嵌套同名字段** (客户端使用正则解析) |
| 失败响应体 | `{"detail":"<非空人可读原因>"}` |
| 响应体上限 | 2 MiB (客户端硬约束) |

---

## E1. `GET /mfi/certificate`

**语义**: 获取 MFi 协处理器的 `protocolMajor` 和 `certificate`。**服务端不缓存**,每次现读芯片 (v5 决策)。

### 请求
- Method: `GET`
- Path: `/mfi/certificate`
- Body: 无
- Headers: `Accept`, `Authorization` (可选)

### 成功响应
- Status: `200`
- Body 字段:

| 字段 | 类型 | 必需 | 说明 |
| --- | --- | --- | --- |
| `protocolMajor` | integer `0..255` | ✅ | 从寄存器 `0x02` 读 |
| `certificate` | string (base64) | ✅ | 从 `0x30`→`0x31..` 读的完整证书 |
| `certificateSha256` | string (64 hex, **小写**) | ✅ | 上面 `certificate` 解码后的 SHA-256 |
| `type` | string `"mfi"` | (可选) | 缺省即 `"mfi"`;本服务**只发送 `"mfi"` 或省略** |

### 客户端校验 (服务端必须满足)
1. `certificate` base64 解码后 size ∈ `1..65525`
2. `certificateSha256` 必须与 base64 解码后的 SHA-256 逐字节相等 ([RemoteMfiAuthenticationClient.kt#L134-L138](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L134-L138))

### 失败响应
| Status | 场景 | Body |
| --- | --- | --- |
| 401 | Bearer token 校验失败 | `{"detail":"unauthorized"}` |
| 503 | 芯片 missing / 打开失败 / 等锁超时 | `{"detail":"chip missing"}` / `{"detail":"chip busy, retry"}` |
| 500 | 寄存器读取失败 / 长度非法 | `{"detail":"failed to read certificate: <cause>"}` |

### 示例
```json
{"protocolMajor":3,"certificate":"AAECAwQFBgcICQ==","certificateSha256":"3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea"}
```

---

## E2. `POST /mfi/sign`

**语义**: 用 MFi 协处理器对 `challenge` 签名。**幂等键 = `requestId`**,60s TTL。

### 请求
- Method: `POST`
- Path: `/mfi/sign`
- Body:

| 字段 | 类型 | 必需 | 说明 |
| --- | --- | --- | --- |
| `challenge` | string (base64, 1..128 bytes) | ✅ | 待签数据 |
| `requestId` | string (UUID, 客户端生成) | ✅ | **幂等键** |

### 成功响应
- Status: `200`
- Body:

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `signature` | string (base64, 1..65525 bytes) | 从寄存器 `0x11`→`0x12+` 读取 |

### 幂等语义 (v5 关键决策)

| 状况 | 服务端行为 |
| --- | --- |
| 首次 `requestId` | 走 chipMutex → 触发芯片 → 缓存 `(reqId, challenge_sha256, signature)` → 返回 |
| 60s 内同 `requestId` + 同 challenge (SHA256 一致) | **直接返 缓存值,不排队,不进 chipMutex** |
| 60s 内同 `requestId` + 不同 challenge | 400 `{"detail":"requestId reuse with different challenge"}` |
| 60s 后同 `requestId` 再到 | 视为新请求 |
| 并发同 `requestId` 到达 (客户端不会做,但要防御) | 二次到达者拿 chipMutex 时**再查一次缓存** (双重检查),命中即返 |

### 失败响应
| Status | 场景 | Body |
| --- | --- | --- |
| 400 | 参数缺失/格式错/challenge 越界 | `{"detail":"challenge must be 1..128 bytes"}` 等 |
| 401 | Bearer 校验失败 | `{"detail":"unauthorized"}` |
| 503 | 等锁超时 (8s) | `{"detail":"chip busy, retry"}` |
| 500 | 芯片认证失败 / I2C 错 | `{"detail":"chip auth failed: 0x<hex>"}` (含 `0x05` 读到的错误码, best-effort) |

### 示例
```
POST /mfi/sign
Authorization: Bearer xxxx
Content-Type: application/json

{"challenge":"AQIDBA==","requestId":"9c5b2f14-3a4d-4f6e-8b12-84cbe9a7c0d1"}
```
响应:
```json
{"signature":"MEUCIQC..."}
```

---

## E3. `POST /mfi/reset`

**语义 (v5.1)**: **服务端为纯 no-op**。仅记一行访问日志、返回固定 body。**不接触芯片、不清任何缓存、不抢锁**。

### 请求
- Method: `POST`
- Path: `/mfi/reset`
- Body: **必须是 `{}`** ([Client L55](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L55))
- Headers: `Authorization` (可选)

### 成功响应
- Status: `200`
- Body: `{"detail":""}` (**detail 字段为空字符串**, 与客户端行为对齐)

### 失败响应
| Status | 场景 | Body |
| --- | --- | --- |
| 401 | Bearer token 校验失败 | `{"detail":"unauthorized"}` |
| 400 | body 非 `{}` | `{"detail":"body must be {}"}` |

### 关键行为约定 (v5.1)
1. **不抢 chipMutex**: 立即执行,与任意 in-flight 端点无竞争。
2. **不重试** (客户端侧关闭重试, [Client L57](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L57))。
3. **不清幂等缓存**: 多头单场景下,清缓存可能抹掉别的头单正在等待重试的 requestId,导致芯片被触发两次。见 [01-requirements.md#54](./01-requirements.md#54-mfireset-语义v51-修订)。
4. **并发安全**: 与 sign / certificate / debug 任意端点并发调用均安全 —— 因为不触碰任何共享状态。多头单共享部署下 reset 与 sign 并发是**常规路径**,不是异常。

### 客户端调用点(依据)

生产上唯一调用位置: [CarPlayController.kt#L423-L443](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/orchestration/CarPlayController.kt#L423-L443) — 每次 CarPlay 会话首次建立时,`new client → reset() → protocolMajor()`。客户端 `.reset()` 的实质效果是清**客户端本地**的证书缓存;紧接着的 GET /certificate 会重新拉取。

---

## E4. `GET /debug/usb`

**语义**: USB 拓扑诊断入口。内容协商决定 HTML 或 JSON。

### 请求
- Method: `GET`
- Path: `/debug/usb`
- Headers: `Authorization` (可选,与 `/mfi/*` 一致);浏览器可用 `?token=<xxx>` query 传递
- 内容协商: `Accept` 头包含 `application/json` → JSON;否则 HTML

### HTML 响应
- Status: `200`
- `Content-Type: text/html; charset=utf-8`
- 包含:
  1. `<meta http-equiv="refresh" content="3">` — 3s 自动刷新
  2. 顶部 banner: `READY / MISSING / ERROR` (v5.2 去 `BUSY` 状态, 与 healthz 3 态对齐), 附 uptime 与容器本地时间
  3. USB 设备表格 (Bus, Device, VID:PID, Manufacturer, Product, Speed, Class);候选 CH341 行加 `class="candidate"` 高亮;空表时显示 `no USB devices enumerated`
  4. Runtime 区块 (chipMutex 持有者、锁持有时长、幂等缓存条目数)
  5. **Recent Requests 表格** — 最近 20 条,6 列 (Time / Method / Path / Status / ms / note);仅收录业务端点

### HTML 401 引导页(v5.2 新增)
设了 `MFI_BEARER_TOKEN` 但请求未提供 token 时,HTML 分支返回**人类可读引导页**(而非空白):
- Status: `401`
- Body: HTML,含两种正确接入方式示例(`Authorization: Bearer <token>` header 与 `?token=<token>` query)
- **不泄露** token 值 / 长度 / 环境变量名之外的任何信息

### JSON 响应

Status: `200`,示例:
```json
{
  "chip": {"status": "ready", "vidPid": "1a86:5512"},
  "uptime": {"startedAt": "2026-09-24T18:00:00+08:00", "seconds": 8123},
  "usbDevices": [
    {"bus": 1, "device": 7, "vid": "1a86", "pid": "5512", "manufacturer": "wch.cn", "product": "USB-I2C", "speed": "full", "class": 255, "candidate": true},
    {"bus": 1, "device": 3, "vid": "05ac", "pid": "12a8", "manufacturer": "Apple Inc.", "product": "iPhone", "speed": "high", "class": 0, "candidate": false}
  ],
  "runtime": {
    "cacheEntries": 3,
    "lockHolder": null,
    "lockHeldMs": 0
  },
  "recentRequests": [
    {"time": "2026-09-24T18:22:03.142+08:00", "method": "POST", "path": "/mfi/sign",        "status": 200, "ms":  142, "note": "chip"},
    {"time": "2026-09-24T18:22:03.050+08:00", "method": "POST", "path": "/mfi/sign",        "status": 200, "ms":    0, "note": "idempotent-hit"},
    {"time": "2026-09-24T18:22:02.911+08:00", "method": "GET",  "path": "/mfi/certificate", "status": 200, "ms":   78, "note": "chip"},
    {"time": "2026-09-24T18:22:02.902+08:00", "method": "POST", "path": "/mfi/reset",       "status": 200, "ms":    0, "note": "noop"},
    {"time": "2026-09-24T18:22:00.401+08:00", "method": "POST", "path": "/mfi/sign",        "status": 503, "ms": 8001, "note": "chip busy"}
  ]
}
```

**chip.status 枚举(v5.2)**: `ready` / `missing` / `error` — 与 `/healthz` 完全同源, 皆基于**当前时刻** libusb 枚举 + handle 打开结果, **不再有 `busy` 状态**(忙碌度看 `runtime.lockHolder` + `runtime.lockHeldMs`)。

**recentRequests 收录规则** (v5.2):
- 内存环形缓冲, 最大 **20 条**, 新的排最前
- 只收录业务端点 `/mfi/certificate`, `/mfi/sign`, `/mfi/reset`
- `/debug/usb` 与 `/healthz` 自身访问**不收录** (刷新页面会挤掉有效记录)
- `note` 枚举:
  - `chip` — 走了完整 I2C 事务
  - `idempotent-hit` — 仅 `/mfi/sign` 60s 幂等缓存命中(v5.2 更名, 前称 `cached`)
  - `noop` — `/mfi/reset` 服务端 no-op
  - `chip busy` — 等 chipMutex 超时 (503)
  - `unauthorized` (401) / `bad request` (400) / `error` (其他 4xx/5xx)
- 时间戳格式: **容器本地时区 + ISO 8601 offset**(受 `TZ` 环境变量控制,默认 `Asia/Shanghai`)
- 不含客户端 IP / UA / body / requestId; 审计需求走 stdout slog

### 失败响应
| Status | 场景 |
| --- | --- |
| 401 | Bearer 校验失败 |

---

## E5. `GET /healthz`

**语义 (v5.2)**: 高频探测。**不鉴权**、极简、便宜(一次快速 libusb 枚举 + handle 状态查询, < 1ms)。

### 请求
- Method: `GET`
- Path: `/healthz`
- Headers: 无要求

### 响应
- Status: 始终 `200`;是否健康由 body 表达(方便 HTTP 层不区分, load balancer 层再判)
- Body:

```json
{"ok":true,"chip":"ready"}
```
或:
```json
{"ok":false,"chip":"missing","reason":"no matching USB device"}
```

| `chip` | 触发条件(v5.2) |
| --- | --- |
| `ready` | libusb 当前枚举到匹配 `MFI_CH341_USB_IDS` 的设备,**且**上一次 handle 打开/claim 未失败 |
| `missing` | libusb 当前枚举**无**匹配设备 |
| `error` | 有匹配设备但**当前**无法打开/claim(权限拒绝、被别的进程 claim、libusb IO 错) |

**v5.2 关键决策**:
- ❌ 删除 `busy` — 忙碌 ≠ 不健康,不应触发 Docker 自动重启;忙碌度看 `/debug/usb` `runtime.lockHeldMs`
- ❌ 删除"最近 30s 有成功操作"启发式 — 拔芯片后无请求时会滞留 ready
- ✅ 只基于当前时刻的 USB 枚举结果,同步、物理、无歧义
- ✅ `chip.status` 与 `/debug/usb` 使用**同一次枚举**,两处显示永不发散

**Docker HEALTHCHECK 命令**:
```sh
wget -q -O- http://127.0.0.1:8080/healthz | grep -q '"ok":true' || exit 1
```

---

## 错误矩阵汇总

| 场景 | Status | detail 示例 |
| --- | --- | --- |
| 未鉴权 | 401 | `unauthorized` |
| 请求 body 缺字段 | 400 | `challenge is required` / `requestId is required` |
| challenge 越界 (0 或 >128) | 400 | `challenge must be 1..128 bytes` |
| requestId 非 UUID | 400 | `requestId must be a UUID` |
| requestId 重用换 challenge | 400 | `requestId reuse with different challenge` |
| body 非 JSON 或非 `{}` (reset) | 400 | `invalid request body` / `body must be {}` |
| 芯片不在位 | 503 | `chip missing` |
| chipMutex 等锁超时 | 503 | `chip busy, retry` |
| 芯片 I2C NACK | 500 | `i2c nack on register 0x<hex>` |
| 芯片认证超时 | 500 | `chip auth timeout` |
| 芯片认证失败 (读 0x05) | 500 | `chip auth failed: 0x<hex>` |
| 其他内部错误 | 500 | `internal error: <cause>` (**不泄露堆栈**) |

---

## 幂等/并发/背压总结 (v5.2)

```
              请求进来
                 │
    ┌────────────▼────────────┐
    │  Bearer 校验 (可选)      │
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐
    │  幂等缓存查 (sign 专属)   │─── 命中 → 直接返
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐   等 chipMutex 最长 8s
    │  chipMutex.Lock(8s)     │─── 超时 → 503 chip busy
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐   拿到锁后再查一次
    │  双重检查缓存             │─── 命中 → 直接返 (释放锁)
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐
    │  I2C 事务 (CH341 libusb) │
    └────────────┬────────────┘
                 │
    ┌────────────▼────────────┐
    │  写入幂等缓存 + 更新健康 │
    └────────────┬────────────┘
                 │
              200 返回
```
