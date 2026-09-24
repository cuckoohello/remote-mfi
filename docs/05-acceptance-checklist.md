# 05 · 验收 Checklist (v5.3)

> 原则: **全面验收不抽象**,按 checklist 逐项过,不只抽查一两个。
> 每条都必须能对应到 [02-api-contract.md](./02-api-contract.md) 或 [01-requirements.md](./01-requirements.md) 的某个条款。

---

## 0. 使用方式
- 每项打勾前记录**证据链** (curl 命令 + 响应 hex / 服务日志 / 截图)。
- 证据存 [xcertplay 记忆](file:///Users/bytedance/Projects/xcertplay) 里习惯的 U 盘位置,方便脱机复盘。
- 任何一条失败, **停下写工单**, 不要继续跳过。

---

## A. API 契约 — `/mfi/certificate`

- [ ] A1. 首次请求成功返回 200,含 `protocolMajor` / `certificate` / `certificateSha256` 三字段
- [ ] A2. `certificate` base64 解码后长度 ∈ `1..65525`
- [ ] A3. `certificateSha256` 为 64 hex 全小写, 与解码后的 SHA-256 逐字节相等
- [ ] A4. 响应 body 是**扁平 JSON、单行 minified**,无嵌套同名 key (用 `jq -c 'to_entries | length'` 验)
- [ ] A5. 无 `type` 字段 或 `type == "mfi"` (本服务不发 baa)
- [ ] A6. **服务端不做证书缓存** — 连续调 2 次 `/mfi/certificate`,服务端 debug 日志应显示两次读寄存器 `0x30/0x31..`
- [ ] A7. 设了 `MFI_BEARER_TOKEN` 时,不带 header → 401 `{"detail":"unauthorized"}`
- [ ] A8. 芯片拔掉 → 503 `{"detail":"chip missing"}`
- [ ] A9. 芯片 I2C NACK → 500 `{"detail":"i2c nack on register 0x30"}`

**联调对照**: [RemoteMfiAuthenticationClientTest.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/test/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClientTest.kt) 中 `resetsLoadsAndCachesCertificate…` 与 `rejectsCertificateWhenSha256DoesNotMatch` 两条应对本服务通过。

---

## B. API 契约 — `/mfi/sign`

- [ ] B1. 首次 sign 200 返回, `signature` base64 解码后长度 ∈ `1..65525`
- [ ] B2. `challenge` 为空字符串 (0 bytes) → 400 `{"detail":"challenge must be 1..128 bytes"}`
- [ ] B3. `challenge` > 128 bytes (base64 后 172 字节+) → 400 同上
- [ ] B4. `requestId` 缺失 → 400 `{"detail":"requestId is required"}`
- [ ] B5. `requestId` 非 UUID (如 `"abc"`) → 400 `{"detail":"requestId must be a UUID"}`
- [ ] B6. **幂等命中**: 同 `requestId` + 同 challenge 60s 内二次到达,返回**相同 signature**,且服务端日志无第二次 `0x10 write`
- [ ] B7. **幂等换 challenge 拒绝**: 同 `requestId` + 不同 challenge → 400 `{"detail":"requestId reuse with different challenge"}`
- [ ] B8. **幂等过期**: 65s 后同 `requestId` 再发 → 服务端**重新触发芯片** (debug 日志)
- [ ] B9. 芯片认证超时 → 500 `{"detail":"chip auth timeout"}`
- [ ] B10. `0x05` 有值时 → 500 `{"detail":"chip auth failed: 0x<hex>"}`

---

## C. API 契约 — `/mfi/reset` (v5.1 no-op 语义)

- [ ] C1. `{}` body → 200 `{"detail":""}` (**detail 是空字符串**)
- [ ] C2. 非 `{}` body (如 `{"x":1}`) → 400 `{"detail":"body must be {}"}`
- [ ] C3. 非 JSON body → 400 `{"detail":"invalid request body"}`
- [ ] C4. **不清芯片状态**: reset 前后立即读 `0x02`,应是同一个 protocolMajor,寄存器未变
- [ ] C5. **不清幂等缓存**(v5.1 关键): reset 前发 sign A(requestId=R,拿到 signature S);reset 后 30s 内再发同 requestId=R + 同 challenge → 应**命中缓存返 同一个 S**,服务端**无芯片操作日志**
- [ ] C6. **并发安全**: 一次 sign 在飞的同时并发 reset,reset 立即 200,原 sign 正常完成并返回正确签名(证明 reset 不抢锁、不动状态)
- [ ] C7. **不重试**: 服务端故意返 500 一次 → 客户端不重试 (对应 [Client L57](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClient.kt#L57))
- [ ] C8. **访问日志**: reset 产生一行 `event=reset` 日志,不产生任何 `event=chip_*` 日志(证明 no-op)

---

## D. API 契约 — `/debug/usb`

- [ ] D1. `Accept: text/html` (默认浏览器) → HTML,`<meta http-equiv="refresh" content="3">` 存在
- [ ] D2. `Accept: application/json` → JSON,含 `chip / uptime / usbDevices / runtime / recentRequests`
- [ ] D3. 有 token 时无 `Authorization` header 且无 `?token=` **HTML 分支** → **401 引导页**(含两种 token 传法示例),**JSON 分支** → 401 `{"detail":"unauthorized"}`(v5.2 新增)
- [ ] D4. 有 token 且 `?token=<正确值>` → 200
- [ ] D5. USB 表格能列出 **所有** 可枚举设备 (不止 CH341);拔掉 CH341 3s 内页面消失该行
- [ ] D6. `1a86:5512` 或 `MFI_CH341_USB_IDS` 列出的 ID 行有 `CANDIDATE CH341` 高亮
- [ ] D7. Runtime 区块显示 `cacheEntries` 数值,与实际发过的 sign 数一致;显示 `lockHolder` / `lockHeldMs`(v5.2)
- [ ] D8. **Recent Requests 表格** 显示最近业务端点访问,新的在最上方
- [ ] D9. **Recent Requests 上限 = 20**: 连发 25 次 sign, 页面只剩最近 20 条
- [ ] D10. **`note` 语义正确**(v5.2 更名 `cached` → `idempotent-hit`): 触发一次 chip / 一次 idempotent-hit / 一次 noop / 一次 chip busy / 一次 unauthorized / 一次 bad request → 各自在页面上有正确 note 标签
- [ ] D11. **/debug/usb 与 /healthz 自身访问不出现在 Recent Requests**: 手工刷新页面 10 次后,列表内**不含** `/debug/usb` 或 `/healthz` 行
- [ ] D12. **不泄露敏感信息**: Recent Requests 里没有 Authorization/token/UA/IP/body 字段(用 view-source 或 JSON 端点确认)
- [ ] D13. **banner 3 态**(v5.2): 芯片正常 → banner `READY`;拔掉 → `MISSING`;权限拒绝 → `ERROR`;**不应出现 `BUSY`**(即使跑长 sign 时,banner 保持 `READY`,lockHeldMs 反映忙碌度)
- [ ] D14. **无 USB 设备时**: 表格显示 `no USB devices enumerated`,不是空表(v5.2)
- [ ] D15. **时间戳格式**: Recent Requests `Time` 列格式为本地时区 + ISO offset(如 `+08:00`);容器 `TZ=UTC` 时改显 `+00:00`(v5.2)

---

## E. API 契约 — `/healthz`(v5.2 修订)

- [ ] E1. 无鉴权即可访问 (即使设了 `MFI_BEARER_TOKEN`)
- [ ] E2. 芯片就绪 → `{"ok":true,"chip":"ready"}`
- [ ] E3. 拔掉 CH341 → **下次 healthz 探测立即**(而不是 30s 后)变为 `{"ok":false,"chip":"missing","reason":"..."}` (v5.2 已移除时间启发式)
- [ ] E4. Docker `docker inspect --format '{{.State.Health.Status}}' remote-mfi` 返回 `healthy`
- [ ] E5. 芯片被别的进程 claim → `{"ok":false,"chip":"error","reason":"claim failed: ..."}`
- [ ] E6. **无 `busy` 状态**(v5.2 关键回归): 人为让一个 sign 独占 chipMutex 5 秒,期间 healthz 仍返 `{"ok":true,"chip":"ready"}`,**HEALTHCHECK 不能因忙碌触发 unhealthy**
- [ ] E7. **healthz 与 /debug/usb 同源**: 拔掉 CH341 后同时 curl 两个端点,`chip.status` 值一致(v5.2)

---

## F. 并发 & 幂等压测

- [ ] F1. **同 requestId × 20 并行**: 20 个协程同时发同 requestId + 同 challenge, 只有 1 次触发芯片,20 次返回相同 signature
- [ ] F2. **异 requestId × 20 并行**: 20 个协程各持不同 requestId + 不同 challenge, 全部成功, **服务端芯片操作严格串行** (chipMutex 独占持有时间 log 之和 ≤ 总耗时)
- [ ] F3. **等锁超时**: 人为在 handler 里 sleep 一次 sign 20s,另开一个 sign → 后者 8s 后 503 `chip busy, retry`
- [ ] F4. **无 signature 错乱**: F1/F2 结束后逐对校验签名与 challenge 匹配 (用 MFi 公钥或对照客户端产物),无跨请求错乱
- [ ] F5. **在 sign 途中发 reset**: reset 立即 200, 且原 sign 不受影响 (成功返回,签名正确)
- [ ] F6. **无 goroutine 泄漏**: 压测结束 60s 后 `runtime.NumGoroutine()` (pprof `/debug/pprof/goroutine`) 回到基线 ±5
- [ ] F7. **多头单幂等隔离**(v5.1 关键回归): 模拟头单 A 发 sign(requestId=R_A)拿到 5xx 正准备重试;此时头单 B 调 `/mfi/reset`;A 重试(同 R_A + 同 challenge) → **必须命中缓存返同一 signature,芯片不被触发第二次**

---

## G. 客户端联调 (最权威)

按 [RemoteMfiAuthenticationClientTest.kt](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/test/java/com/shilapi/xcertplay/mfi/RemoteMfiAuthenticationClientTest.kt) 5 个测试反向验收本服务:

- [ ] G1. `resetsLoadsAndCachesCertificateThenSignsWithStableRequestId` — 服务端能撑住 reset → certificate → 两次同 requestId 的 sign
- [ ] G2. `rejectsCertificateWhenSha256DoesNotMatch` — 服务端返回错误的 sha256 (调试 stub 模式) → 客户端应抛 `MfiInvalidDataException`
- [ ] G3. `loadsBaaPackageAndBuildsShowcaseIap2Payload` — **N/A** (本服务只发 mfi 类型),此项验证客户端拒绝我们错发的 baa (预期我们**不发**)
- [ ] G4. `rejectsUnknownCertificateType` — 服务端如故意发 `"type":"unknown"` (调试 stub) → 客户端 400
- [ ] G5. `resetSurfacesHttp500DetailWithoutRetrying` — 服务端 reset 返 500 → 客户端只调 1 次不重试

**联调回归**: 在 xcertplay 客户端上跑一次真实 CarPlay 会话,直到 iAP2 收到 `AA05 AuthenticationSucceeded`。

---

## H. Docker & 部署

- [ ] H1. 镜像 SIZE ≤ 40 MB
- [ ] H2. 镜像 rootfs 不含 `.git / go.mod / *.go / node_modules`
- [ ] H3. 容器以非 root 用户 `mfi` 运行 (`docker exec ... id`)
- [ ] H4. HEALTHCHECK 生效 (`docker inspect` 显示 `Healthcheck` 段)
- [ ] H5. 使用 `-v /dev/bus/usb:/dev/bus/usb` + `--device-cgroup-rule='c 189:* rmw'` + 宿主 `plugdev` 数字 GID 后,容器内能枚举并 claim CH341
- [ ] H6. 拔掉 CH341 → 容器**不 exit** (启动 probe 失败不 exit 的合约)
- [ ] H7. 重插 CH341 → 无需重启容器,下一次请求自动恢复
- [ ] H8. 未设 `MFI_BEARER_TOKEN` 时,启动日志有 1 行 WARN `authentication disabled`
- [ ] H9. 设了 `MFI_BEARER_TOKEN` 后,日志中**不出现** token 明文 (log redact)
- [ ] H10. `SIGTERM` 后 10s 内进程退出,期间在飞的 sign 请求要么完成要么返回 503

---

## I. 观测性 & 日志

- [ ] I1. `MFI_LOG_FORMAT=json` 时,每行都是合法 JSON (用 `jq .` 逐行过)
- [ ] I2. 每次 `/mfi/sign` 至少产出 1 行 `event=sign` 日志,含字段 `reqId / chip_wait_ms / duration_ms / status`
- [ ] I3. `event=chip_lock` 日志能反映锁持有时长 (对应 F 组压测)
- [ ] I4. `event=chip_tx` / `event=chip_rx` 在 `MFI_LOG_LEVEL=debug` 时含 hex dump
- [ ] I5. 401/400/500 响应对应有 `event=http_error` 日志,含 status / path / detail

---

## J. 安全

- [ ] J1. 未鉴权模式下,能被任意来源访问 `/mfi/sign` — 这是文档已警告的部署侧责任
- [ ] J2. 鉴权模式下,`Authorization: Bearer <错误值>` → 401 (常量时间比较,防止 timing attack)
- [ ] J3. `Authorization` header 值不在日志中出现
- [ ] J4. `?token=` query 值不在 access log 中出现 (需要 log redact)
- [ ] J5. 响应体不泄露堆栈、内部路径、libusb 版本

---

## K. 芯片时序回归

> ⚠️ **K1~K4 需要示波器 / 逻辑分析仪**(如 Saleae Logic)才能精确到 ±2ms;仅靠 Go wall clock 无法保证。软件层可打 debug 日志观察相对顺序,精确验证走硬件。

- [ ] K1. 触发 auth 后首次读延迟 = 10ms ± 2ms ([#L243](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiAuthenticationClient.kt#L243))
- [ ] K2. auth 轮询间隔 = 10ms ± 2ms
- [ ] K3. 总超时 = 3000ms,超过即失败
- [ ] K4. register read 的 STOP→START 间隔 ≥ 5ms (对应 CH341 2.0C quirk, [Ch341I2cTransport.kt#L28-L30](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/transport/Ch341I2cTransport.kt#L28-L30))
- [ ] K5. 证书按 128 字节 window 读取 (0x31, 0x32, ...),用逻辑分析仪或 debug 日志能观察到寄存器逐个递增

---

## L. 回滚演练

至少做一次:

- [ ] L1. 上一版镜像 tag 存在
- [ ] L2. `docker stop && docker run <old tag>` 30s 内 healthy
- [ ] L3. udev 规则删除后, 重新 reload/trigger 恢复正常
- [ ] L4. 上游客户端能持续工作 (不假死)

---

## M. v5.2 新增回归

### M.1 锁层级 & 并发正确性
- [ ] M1. `go build -race` 通过, 二进制启动无 race warning
- [ ] M2. F 组 + G 组测试全部在 `-race` 模式跑一遍, 无 data race 报告
- [ ] M3. 观察 `event=chip_lock` 日志, chipMutex 持有时间与 I2C 事务耗时对齐(无异常长时间持锁)
- [ ] M4. 通过 goroutine dump 或 stack trace 抽查, **无任何 goroutine 同时持有** chipMutex + cacheMutex + recentMutex 中的两个及以上
- [ ] M5. cacheMutex 与 recentMutex 临界区在 pprof block profile 中 P99 < 100µs

### M.2 冷启动指标
- [ ] M6. 冷启动到 HTTP 端口 accept ≤ 2s (`curl -w "%{time_total}\n" http://localhost:8080/healthz` 循环探测)
- [ ] M7. 冷启动到 `/healthz` 返 `chip:"ready"` ≤ 3s(芯片已插入前提)
- [ ] M8. 芯片未插入时启动, `/healthz` 立即返 `chip:"missing"`, **服务不 exit, 端口正常 accept**

### M.3 时区
- [ ] M9. 默认部署 `TZ=Asia/Shanghai`, Recent Requests 显示 `+08:00`
- [ ] M10. `docker run -e TZ=UTC ...` 时, Recent Requests 显示 `+00:00`
- [ ] M11. slog 日志时间戳格式与 Recent Requests **一致**(便于时间线关联)

### M.4 性能微观回归
- [ ] M12. **幂等命中 P50 < 500µs**(v5.2 收紧目标): 循环打 1000 次同 requestId, P50 从 Go pprof / 直接测量
- [ ] M13. 无内存泄漏: 打 10000 次不同 requestId (间隔 65s 让 TTL 过期), 60s 后 RSS 与基线 ±10MB

---

## N. v5.3 多形态 & 多架构

### N.1 Docker 镜像 (multi-arch)
- [ ] N1. `docker buildx imagetools inspect ghcr.io/cuckoohello/remote-mfi:v0.1.0` 显示 **`linux/amd64` + `linux/arm64`** 两个 sub-image
- [ ] N2. amd64 宿主机上 `docker pull` 后 `docker image inspect` 显示 `Architecture: amd64`,arm64 宿主机上 `arm64`
- [ ] N3. 两架构镜像各自 SIZE ≤ 40MB
- [ ] N4. `docker run --pull=always ghcr.io/cuckoohello/remote-mfi:latest` **匿名可拉**(公开仓库)
- [ ] N5. amd64 + arm64 各跑一遍 A~M 组关键用例(至少 A/B/C/D/E/F1/F2/G1/G5),行为一致

### N.2 宿主机 binary (4 变体)
- [ ] N6. GitHub Releases 页面含 4 个 tarball + 4 个 `.sha256` 校验文件
- [ ] N7. **glibc/amd64**: 在 Ubuntu 22.04 上 `sha256sum -c` 通过, `ldd remote-mfi` 显示动态链接 `libusb-1.0.so.0` 与 glibc,`./remote-mfi` 启动无错
- [ ] N8. **musl/amd64**: 在 Alpine 3.20 上 `sha256sum -c` 通过, `ldd remote-mfi` 显示 musl + libusb,`./remote-mfi` 启动无错
- [ ] N9. **glibc/arm64**: 树莓派 4 或 arm64 云主机上 `./remote-mfi` 启动无错
- [ ] N10. **musl/arm64**: Alpine arm64 上 `./remote-mfi` 启动无错
- [ ] N11. **交叉污染防御**: musl binary 拷到 glibc 宿主机跑 → **合理错误提示**(不是段错误);反之亦然
- [ ] N12. binary tarball 内容仅含 `remote-mfi + README.md + LICENSE`,无 `.go` / `.git` / debug symbol 冗余

### N.3 libusb 依赖 & udev
- [ ] N13. glibc 宿主机未装 libusb 时启动 → `error while loading shared libraries: libusb-1.0.so.0` 提示清晰
- [ ] N14. 装完 libusb-1.0 后启动成功,`/healthz` 返 ready
- [ ] N15. 用户未在 plugdev 组时 → `/healthz` 返 `chip:"error"`,reason 含 "permission denied"
- [ ] N16. `sudo usermod -aG plugdev $USER` 后重新登录, 启动服务返 ready

### N.4 systemd(参考实现验收)
- [ ] N17. 按 [Runbook §3.4.4](./04-runbook.md#344-生产运行用户自己写-systemd-unit) 参考 unit 部署, `systemctl status remote-mfi` 显示 `active (running)`
- [ ] N18. `journalctl -u remote-mfi -f` 能实时看到 slog JSON 输出
- [ ] N19. `systemctl restart remote-mfi` 后进程重新起来, healthz 恢复 ready
- [ ] N20. 系统重启后 unit 自动拉起 (`WantedBy=multi-user.target` 生效)

### N.5 交付一致性
- [ ] N21. 同一 tag(如 `v0.1.0`)的 Docker 镜像 sub-image 与 tarball binary **来自同一 git commit**(Release notes 显式引用 commit SHA)
- [ ] N22. Docker 镜像的 `remote-mfi --version`(如实现)输出与 GitHub Release tag 一致
- [ ] N23. GHCR 上 `latest` tag 只指向最新稳定 release, 不指向 pre-release

---

## 通过条件

**全部 A~N 组打勾 → 视为验收通过**,写入变更矩阵备注。

有任何一条打叉 → 停下讨论,不要"抽查通过"糊过去。这符合项目规则 "全面验收不抽象"。
