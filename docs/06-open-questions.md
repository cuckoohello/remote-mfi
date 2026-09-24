# 06 · 开放问题 (Open Questions)

> 版本: v5.3
> 定位: 收录**已识别但主动暂缓**的问题, 避免设计阶段被这些次要 concern 拖住节奏。
> 每条都有明确的**触发条件**决定何时回来处理。

---

## 分类

- 🔵 **架构类** — 涉及分层/接口, 改起来动多个文件
- 🟢 **运维类** — 涉及部署/监控, 不动代码架构
- 🟡 **供应链/安全类** — 涉及签名/验证/漏洞
- 🟠 **产品/需求类** — 涉及用户请求或场景扩展

---

## 已解决

### R.1 锁层级契约的 transport 例外

**结论(实现阶段)**: 明确采用单向顺序:

```
chipGate → cacheMutex
chipGate → transport.ioMu → transport.sessionMu
```

`recentMutex` 不与任何锁嵌套；transport 不反向申请上层锁。已同步到 [01-req §5.5](./01-requirements.md#55-锁层级契约v52-新增)。

### R.2 Sign 请求的 ctx 传播

**结论(实现阶段)**: 等 `chipGate` 时遵循 `r.Context()`；一旦拿到锁，用 `context.WithoutCancel` 跑完整个芯片序列。客户端中断不能让硬件停在 challenge 已写但响应未读的半状态。

---

## O.3 [🟡 供应链/安全] Supply-chain signing

**背景**: [03-arch §9](./03-architecture.md#9-构建--发布流水线概览) 提到未做:
- 镜像签名 (cosign)
- Release 附件 GPG 签名
- SBOM (syft/CycloneDX)

**触发**: 项目开始接入生产环境,或者上游用户明确要求可验证性。v0.1.0 阶段不做。

**当被触发时的行动**:
1. GitHub Actions 加 `sigstore/cosign-installer` + `cosign sign`
2. Release notes 里贴出验签命令(`cosign verify ...` + GPG public key fingerprint)
3. SBOM 用 `anchore/sbom-action` 自动生成并附到 release

---

## O.4 [🟢 运维] pprof / 观测端口

**背景**: [05-acceptance §M.1](./05-acceptance-checklist.md#m1-锁层级--并发正确性) 提到 M4 要 goroutine dump / M5 要 block profile,依赖 `net/http/pprof`。当前架构里**没有** pprof endpoint,原因是:
- 未鉴权时暴露 pprof 极危险(可下载 heap dump 泄露内存中的 token)
- 挂在 `/mfi/*` 同一 mux 上需要考虑鉴权路径

**候选方案**:
- A. **单独 admin 端口**: 增加 `MFI_ADMIN_ADDR` 环境变量, pprof + prometheus metrics 一起挂,默认只监听 localhost (`127.0.0.1:6060`)
- B. **挂在主端口 + 强制鉴权**: `/debug/pprof/*` 走 Bearer, 但 pprof 本身不 friendly
- C. **不做**: 现场压测用 `dlv attach` 或 `go tool trace` 离线分析

**默认倾向**: A(独立端口 loopback-only)。**触发条件**: 出现第一次生产 hang 需要 profile 时。

---

## O.5 [🟠 产品] 多芯片路由 / 多实例调度

**背景**: [00-overview §4](./00-overview.md#4-非目标-non-goals--明确不做) 明确"一个进程绑定一个 CH341"。但真实场景可能:
- 一台宿主机插 2 片 CH341 + 2 片 MFi 芯片,希望**同时**服务
- 或多个 remote-mfi 实例做主备(一片主 + 一片备)

**当前策略**: 简单粗暴 — 每芯片起一个 container/process,不同端口,不同 URL,由客户端配置分流。

**触发**: 用户明确报告 "n 台头单同时握手, 单芯片 200ms/次 顶不住"。

**当被触发时的候选**:
- A. **多进程 (推荐, 无代码改)**: 上游负载均衡, `remote-mfi-1:8080` `remote-mfi-2:8081` ...
- B. **单进程多 transport**: 大改架构, chipService 变 chipPool, 引入路由算法
- C. **保持当前 + 明确容量文档**: 单芯片 QPS ≈ 5 (200ms/次) 是硬上限, 超出即需 A

**默认倾向**: A + C(文档明确容量, 靠部署侧扩)。B 是过度工程,除非真的有场景不支持多进程部署。

---

## O.6 [🟢 运维] Prometheus metrics

**背景**: 当前观测靠 slog stdout,`recentRequests` 就是"运维观察窗"。 但集群化后需要:
- QPS / P50 / P95 / P99
- 幂等命中率
- chip busy 频率
- USB re-enumerate 次数

**触发**: 需要接入 Grafana / VictoriaMetrics 等聚合平台时。

**当被触发时**:
- 与 O.4 pprof 复用 admin 端口
- 用 `github.com/prometheus/client_golang`(引入第三方依赖,与 v5"不引入 metrics 库"原则冲突,需明确破例)
- 指标名规范: `remote_mfi_sign_duration_seconds` 等,遵循 Prometheus 命名

---

## O.7 [🟠 产品] Health probe 探测 I2C 地址

**背景**: [Runbook §1.3](./04-runbook.md#13-关键值一览-拷贝进人工单) 提到 MFi 芯片 I2C 7-bit 地址常见 `0x11`,但客户端 [MfiDeviceScanner](https://github.com/shilapi/xcertplay/blob/3ac55e3/shared/src/main/java/com/shilapi/xcertplay/mfi/MfiDeviceScanner.kt) 实际支持**多地址探测**。

**问题**:
- 当前服务端假设 `MFI_MFI_I2C_ADDRESS=0x11` 固定;若某批芯片是 `0x10` / `0x12`,启动 probe 会失败
- 是否应像客户端一样支持"扫描一组候选地址"?

**触发**: 用户报告 "同一 board 上 MFi 芯片地址不是 0x11"。

**候选方案**:
- A. **保持单地址**: 现在够用,不同板子改 `MFI_MFI_I2C_ADDRESS` 即可
- B. **加候选清单** `MFI_MFI_I2C_ADDRESSES=0x10,0x11,0x12`,启动 probe 时逐个试,第一个响应的记住

**默认倾向**: A(简单)。B 只在多样硬件场景出现时再做。

---

## O.8 [🟢 运维] 灰度 / 蓝绿升级

**背景**: 一片 MFi 芯片对应一个物理 accessory identity, 客户端 gets 到的证书是绑定这片芯片的。升级 remote-mfi 版本:
- Docker: `docker stop && docker run` 期间客户端会瞬时报错 → 5xx 重试成功
- 宿主机 binary: `systemctl restart` 同上

**问题**: 是否需要蓝绿 / 平滑重启(比如同时起两个进程,新旧共存)?

**分析**: **不需要**。
- 单芯片本来就是硬串行,起两个进程会争同一个 USB device → libusb claim 冲突
- 客户端有重试机制,一次 3s 重启窗口内 5xx 触发重试,业务无感

**结论**: **已确定不做**, 记录只为将来 review 时不再讨论。

---

## O.9 [🔵 架构] Recent Requests 是否需要 admin API 清空

**背景**: 诊断页展示 recentRequests 环形缓冲,20 条上限,一直重写。**没有清空接口**。

**问题**: 现场排障时是否需要 `POST /debug/reset-recent` 之类接口, 让运维手动打个"分界线"再复现问题?

**分析**: 已有替代 — 触发一次 `/mfi/sign` 就会挤掉一条旧记录,或者干脆重启容器。**目前不需要**。

**触发**: 用户明确请求。

---

## O.10 [🟠 产品] CarPlay Ultra / BAA 支持

**背景**: 上游 [xcertplay README](https://github.com/shilapi/xcertplay/blob/3ac55e3/README.md) 明确 "只测过 BAA authentication", 但本服务 v5 决策 [Non-Goals](./00-overview.md#4-非目标-non-goals--明确不做) 排除 BAA。

**长期方向**: 若上游用户强需 BAA 支持,是否本服务加一个 `type=baa` 的证书 endpoint?

**触发**: 客户端方(shilapi)明确要求兼容 BAA。

**当被触发时的候选**:
- A. 服务端支持 `MFI_CERT_TYPE=mfi|baa` 环境变量,由部署侧选择输出的证书类型
- B. 本服务保持 mfi-only, BAA 场景另建 `remote-baa` 服务

**默认倾向**: B(职责单一)。BAA 依赖 macOS `DeviceIdentity.framework`,与 Linux + CH341 完全是两个世界,合在一起是过度耦合。

---

## 总览优先级

| # | 类别 | 触发条件 | 预估工作量 |
| --- | --- | --- | --- |
| O.3 | 供应链 | v1.0 GA / 用户要求 | M — CI 改动 |
| O.4 | 运维 | 首次生产 hang | M — 新 handler + 端口 |
| O.5 | 产品 | 单芯片顶不住 | L — 架构重构 |
| O.6 | 运维 | 集群化 | M — 新依赖 + 端口 |
| O.7 | 产品 | 硬件地址不 fixed | S — 加环境变量 + 扫描逻辑 |
| O.8 | 运维 | 已确定不做 | — |
| O.9 | 架构 | 用户明确请求 | S — 一个 handler |
| O.10 | 产品 | shilapi 要求 | L — 另建服务 |

---

## 使用规则

**任何决策若不能立即做**,追加到本文档而不是塞进 [01-req](./01-requirements.md) / [02-api](./02-api-contract.md) 制造混乱。

**任何本文档中的问题一旦决策**:
1. 从 Open Questions 移到顶部“已解决”区，压缩为结论与依据
2. 在对应文档更新决策
3. 变更矩阵 [04-runbook §5](./04-runbook.md#5-变更矩阵) 追加一行
4. 验收清单 [05-acceptance](./05-acceptance-checklist.md) 追加对应回归项

**避免变成 dumping ground**: 定期(每次 review 时)检查是否有条目已经"被解决"却没清理掉。
