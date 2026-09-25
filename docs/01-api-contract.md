# 01 · API 契约

兼容基准：[xcertplay RemoteMfiAuthenticationClient.kt @ 3ac55e3](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt)。服务端字段和错误映射见 [handlers.go](../internal/httpapi/handlers.go)。

## 通用约定

- `/mfi/*` 使用扁平、单行 JSON，响应类型为 `application/json; charset=utf-8`，避免嵌套同名字段影响客户端解析。
- POST 请求发送 `Content-Type: application/json`；body 上限 64 KiB，只允许一个 JSON 值，sign 拒绝未知字段。
- 传入非空 `--bearer-token` 后，业务接口及 `/debug/usb` 要求 `Authorization: Bearer <token>`；留空关闭鉴权。`/healthz` 始终无需鉴权。
- 客户端响应体上限为 2 MiB；证书和签名解码后长度为 `1..65525` 字节。
- 下述接口的 JSON 错误响应为 `{"detail":"非空错误原因"}`。诊断页 HTML 鉴权失败例外；错误方法返回 405 并设置 `Allow`。

## GET /mfi/certificate

首次请求现读芯片的协议主版本和证书；成功后在进程生命周期内缓存，后续请求直接返回缓存值，不再访问芯片。失败不缓存。更换 CH341/MFi 设备后必须重启服务，否则会继续返回旧证书。

| 响应字段 | 类型 | 含义 |
| --- | --- | --- |
| `type` | string | 固定 `"mfi"` |
| `protocolMajor` | integer | 芯片协议主版本，`0..255` |
| `certificate` | string | 标准 base64 编码的证书 |
| `certificateSha256` | string | 证书解码后的 SHA-256，64 位小写十六进制 |

成功返回 200。以下仅演示编码与哈希关系，不是真实 MFi 证书：

```json
{"type":"mfi","protocolMajor":3,"certificate":"AAECAwQFBgcICQ==","certificateSha256":"1f825aa2f0020ef7cf91dfa30da4668d791c5d4824fc8e41354b89ec05795ab3"}
```

## POST /mfi/sign

| 请求字段 | 类型 | 要求 |
| --- | --- | --- |
| `challenge` | string | 必填，标准 base64，解码后 `1..128` 字节 |
| `requestId` | string | 必填 UUID；同一次签名重试时原样复用 |

请求示例：

```json
{"challenge":"AQIDBA==","requestId":"9c5b2f14-3a4d-4f6e-8b12-84cbe9a7c0d1"}
```

成功返回 200，body 为仅含 `signature` 字段的对象，值是芯片签名的标准 base64。

| 情况 | 行为 |
| --- | --- |
| 首次请求 | 串行调用芯片；成功后缓存结果 60 秒 |
| 缓存有效，同 `requestId`、同 challenge | 返回相同签名，不再调用芯片 |
| 缓存有效，同 `requestId`、不同 challenge | 400，`requestId reuse with different challenge` |
| 缓存过期或进程重启 | 作为新请求处理 |
| 同一请求并发到达 | 获得芯片访问权后再次查缓存，避免成功签名重复执行 |
| 芯片操作失败 | 不缓存错误；重试会再次尝试芯片操作 |

芯片操作在单进程内全局串行。等锁期限为 8 秒，超时返回 503；它不是整笔请求的耗时上限。等待时遵循请求取消，取得访问权后芯片序列继续完成，成功结果仍会缓存。

## POST /mfi/reset

body 必须是空 JSON 对象 `{}`。成功返回 200：

```json
{"detail":""}
```

这是客户端开始远程会话时调用的兼容 no-op：不访问芯片、不等待芯片锁、不清幂等缓存。

## GET /debug/usb

`Accept` 包含 `application/json` 时返回 JSON，否则返回每 3 秒刷新的 HTML 页面。除 Bearer header 外，此接口也接受 `?token=<token>`；未通过鉴权时分别返回 JSON 401 或 HTML 401 引导页。

| JSON 字段 | 内容 |
| --- | --- |
| `chip` | `status` 为 `ready/missing/error`，可含 `reason`、`vidPid` |
| `uptime` | `startedAt`、`seconds` |
| `usbDevices` | 可枚举的 USB 设备；每项含 `bus/device/vid/pid/manufacturer/product/speed/class/candidate` |
| `runtime` | `cacheEntries`、`lockHolder`（空闲为 null）、`lockHeldMs` |
| `recentRequests` | 最近 20 条已完成的业务请求，最新收录的在前 |

每条最近请求含 `time/method/path/status/ms/note`。`time` 为完成后收录时刻，采用系统本地时区；`ms` 为总处理耗时。列表只记录三个 `/mfi/*` 端点，重启清空。

`note` 为 `chip`、`idempotent-hit`、`noop`、`chip busy`、`unauthorized`、`bad request` 或 `error`。`/mfi/certificate` 缓存命中和 `/mfi/sign` 幂等命中都记为 `idempotent-hit`。最近请求列表不包含 requestId、请求 body、IP 或 token；`runtime.lockHolder` 在签名期间可能显示 requestId。

`cacheEntries` 是签名幂等缓存的内存条目数，已过期条目在清理前仍可能计入；证书缓存另外维护，不计入此项。页面的 USB 候选标记仅表示 VID:PID 匹配。

## GET /healthz

GET 始终返回 200，检查 body 中的 `ok`：

```json
{"ok":true,"chip":"ready"}
```

```json
{"ok":false,"chip":"missing","reason":"no matching USB device"}
```

| `chip` | 含义 |
| --- | --- |
| `ready` | 枚举到匹配设备，且 USB 会话打开/复用成功 |
| `missing` | 未枚举到匹配设备 |
| `error` | USB 检查或会话打开失败，`reason` 给出原因 |

该接口与诊断页调用同一状态检查逻辑，但每次请求独立采样。它不执行 MFi 签名，也不验证已有 USB handle 经拔插后仍有效；真实可用性需通过证书读取和签名确认。忙碌度见诊断页 `runtime`。

## 错误响应

| Status | 条件 | `detail` |
| --- | --- | --- |
| 400 | 缺少 requestId / challenge | `requestId is required` / `challenge is required` |
| 400 | UUID / base64 / 解码长度不合法 | `requestId must be a UUID` / `challenge is not valid base64` / `challenge must be 1..128 bytes` |
| 400 | requestId 换 challenge | `requestId reuse with different challenge` |
| 400 | body 解析失败、未知 sign 字段、多个 JSON 值或超限 | `invalid request body` |
| 400 | reset 是非空对象或 null | `body must be {}` |
| 401 | token 缺失或错误 | `unauthorized` |
| 503 | 芯片缺失 / 等锁超时 / 请求取消 | `chip missing` / `chip busy, retry` / `request cancelled` |
| 503 | USB 权限不足 / 被占用 | `chip unavailable: permission denied` / `chip unavailable: USB device busy` |
| 500 | USB 超时 / I2C NACK | `chip operation timed out` / `I2C NACK` |
| 500 | 认证失败 | `chip auth failed: 0xNN`，无法读取错误码时为 `chip auth timeout` |
| 500 | 返回数据非法 / 其他硬件错误 | `invalid data returned by MFi chip` / `MFi hardware operation failed` |
