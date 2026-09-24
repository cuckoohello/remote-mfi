# 03 · 测试与硬件验收

每次验收记录 commit、产物版本、平台、硬件标识及命令/响应/日志；UI 留存截图。逐项标记通过、失败或未测，未测项不能视为通过。当前真实 MFi 签名、CarPlay `AA05`、Merlin 与群晖部署均待验证。

## 自动化检查

按 [README](../README.zh-CN.md#开发)安装开发依赖后执行：

```sh
make check
make build
./remote-mfi-for-xcertplay --version
```

现有测试使用 fake driver/脚本化 transport，不需要 USB 设备，但编译仍依赖 libusb 开发包。

| 覆盖范围 | 测试位置 |
| --- | --- |
| 配置默认值、覆盖与非法值 | [config_test.go](../internal/config/config_test.go) |
| HTTP 流程、鉴权、参数校验、reset、health 与最近请求 | [handlers_test.go](../internal/httpapi/handlers_test.go)、[recent_test.go](../internal/httpapi/recent_test.go) |
| 同 ID 去重、不同 ID 串行、reset 保留缓存、证书进程内缓存、错误不缓存、等锁期限 | [service_test.go](../internal/biz/service_test.go) |
| 证书窗口、签名寄存器顺序、完整 select/read 重试 | [driver_test.go](../internal/chip/driver_test.go) |
| CH341 编码、分段、最终 NACK、非法参数及错误分类 | [ch341_encoder_test.go](../internal/transport/ch341_encoder_test.go) |

- [ ] `make check` 全部通过，无 race/vet 报告。
- [ ] `--version` 与构建时传入的版本、commit、日期一致。
- [ ] CI 中 amd64/arm64 的 Docker 和 glibc 构建通过。

下面的完整验收还包含现有测试未覆盖的场景。错误注入、精确计数及时间控制使用测试替身或专用测试程序；普通 HTTP 访问日志不能证明某个寄存器只写过一次。

## HTTP 与并发

逐项对照 [API 契约](01-api-contract.md)，保存实际响应：

- [ ] certificate 返回 200，字段完整且 `type=mfi`；base64 解码长度 `1..65525`，SHA-256 为匹配的 64 位小写十六进制。
- [ ] 首次 certificate 调用芯片，后续调用直接返回缓存值且芯片调用计数不再增加；证书读取失败不缓存，下一次会重新调用芯片。
- [ ] sign 首次成功返回可解码签名；challenge 的 1、128 字节边界可被接受，0、129 字节被拒绝。
- [ ] 缺字段、非法 base64、非 UUID、未知字段、多个 JSON 值及超过 64 KiB 的 body 返回 400。
- [ ] 同 requestId、同 challenge 在成功后 60 秒内返回相同签名；不同 challenge 返回 400。
- [ ] 成功后等待超过 60 秒再提交同请求，重新调用芯片；失败请求不缓存。
- [ ] 20 个同 ID 请求并发：成功签名仅调用一次；20 个不同 ID 请求的芯片操作严格串行，超时按契约返回，成功结果与各自 challenge 对应。
- [ ] 用阻塞的 fake driver 验证等锁超时返回 503；取消等待请求可退出，已开始的芯片序列不因客户端断开而半途停止。
- [ ] reset 接受 `{}`，返回 `{"detail":""}`；非空对象、null、数组、空 body 和非法 JSON 均拒绝。
- [ ] sign 进行时 reset 无需等锁；成功 sign → reset → 相同请求仍命中缓存。
- [ ] token 正确时可访问业务接口；缺失/错误时 401，`/healthz` 始终无需 token。
- [ ] `/mfi/*` 不接受 query token；`/debug/usb` 支持 Bearer 或 query token。
- [ ] 错误方法返回 405；硬件缺失、权限、占用、I2C NACK、认证失败及非法返回数据均符合错误映射。

## 诊断与日志

- [ ] `/debug/usb` 默认 HTML、`Accept: application/json` 返回 JSON；未鉴权时分别显示 HTML 引导页或 JSON 401。
- [ ] 页面每 3 秒刷新，展示候选 USB、状态、uptime、锁持有者、持锁时长及缓存条目数；无设备时有明确提示。
- [ ] 发出 25 次业务请求后只保留最近完成的 20 条；刷新 debug/health 不挤占列表。
- [ ] `note` 能区分芯片调用、幂等命中、noop 和各类错误；最近请求时间是收录时刻，耗时包含等锁。
- [ ] 显式 `TZ=Asia/Shanghai` 时日志和页面为 `+08:00`；`TZ=UTC` 时为 UTC（`Z` 或 `+00:00`）。
- [ ] 默认 JSON 日志可解析，请求日志为 `msg=http_request`，含 `request_id/status/duration_ms/chip_wait_ms/chip_duration_ms/note`；正常请求不记录 token 或 body。
- [ ] health 的 HTTP 状态为 200，通过 `ok` 区分健康；分别验证 `ready/missing/error`，忙碌度由 `runtime` 表达。

## 实体 CH341/MFi

按[运维手册](02-runbook.md)启动，先记录 `lsusb`、节点权限、运行用户、USB VID:PID、I2C 地址和速度。无设备时应能启动 HTTP，probe 失败不退出。

证书检查示例（需要 curl 和 Python 3）：

```sh
BASE_URL=http://127.0.0.1:8080
curl -fsS -H "Authorization: Bearer $MFI_BEARER_TOKEN" \
  "$BASE_URL/mfi/certificate" > certificate.json
python3 -c 'import base64,hashlib,json; d=json.load(open("certificate.json")); c=base64.b64decode(d["certificate"],validate=True); assert d["type"]=="mfi" and 1<=len(c)<=65525; assert hashlib.sha256(c).hexdigest()==d["certificateSha256"]; print("certificate hash OK")'
```

签名编码检查可使用固定样例；该样例不能代替真实 CarPlay 认证：

```sh
curl -fsS -H "Authorization: Bearer $MFI_BEARER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"challenge":"AQIDBA==","requestId":"9c5b2f14-3a4d-4f6e-8b12-84cbe9a7c0d1"}' \
  "$BASE_URL/mfi/sign"
```

- [ ] 真实证书长度和哈希校验通过，真实签名成功；用客户端或证书公钥验证与 challenge 的对应关系。
- [ ] 用逻辑分析仪核对证书从 `0x30` 读长度、`0x31` 起按 128 字节窗口读取；签名写 `0x20/0x21`，写 `0x10=0x01` 后轮询状态，成功后读 `0x11/0x12`。
- [ ] 测量 select/read 间隔及认证轮询时序，对照 [driver.go](../internal/chip/driver.go) 和 [ch341.go](../internal/transport/ch341.go) 常量；记录实际值。3 秒轮询期限不等于完整签名的硬性耗时上限。
- [ ] 运行中拔掉 CH341，服务保持运行，下一次 health 显示 missing，业务返回对应错误。
- [ ] 重新插入原设备后，`/mfi/certificate` 命中缓存不再访问芯片，签名可恢复；另测拔插期间无业务请求、只刷新 health 的场景，不能仅凭 ready 判通过。
- [ ] 更换为不同的 CH341/MFi 设备并重启服务后，`/mfi/certificate` 返回新证书；不重启则继续返回旧证书，用于校验换设备必须重启的约束。
- [ ] 两个 xcertplay 客户端同时建立会话，含交错 reset，签名互不串扰。
- [ ] 完成 xcertplay `reset → certificate → sign` 流程，iPhone/iAP2 日志确认 `AA05 AuthenticationSucceeded`。
- [ ] 空闲和进行中请求两种状态下发送 SIGTERM，记录退出时间、请求结果和设备释放；新进程可重新 claim。

## 发布与部署

每个平台分别记录结果，构建通过不代表硬件验收通过：

| 产物 | 验证环境 |
| --- | --- |
| Docker `linux/amd64`、`linux/arm64` | 对应原生 Linux 宿主机 |
| glibc `amd64`、`arm64` | glibc ≥ 2.35，安装 libusb |
| musl `amd64`、`arm64` | Alpine 3.20，安装 libusb |
| merlin `arm64` | Asuswrt-Merlin HND 5.02L 路由器（RT-AX86U 388.x 等），使用固件自带的 `libusb-1.0.so.0` |

- [ ] 镜像 manifest 含两个架构；Release 含五个 tarball 和五份 SHA-256，全部校验通过。
- [ ] tarball 内容为二进制、中英文 README、LICENSE；所有产物版本/commit 对应同一 tag。
- [ ] 二进制 `ldd` 依赖可解析；容器默认以 root 运行，运行镜像不含源码和构建工具链。
- [ ] 普通宿主机用户经 udev/组权限可访问设备；Docker 的 USB 挂载和 device cgroup 放行均有效。
- [ ] 每种实际使用的产物完成上述 HTTP、诊断、实体芯片及客户端检查。
- [ ] 若采用 systemd，验证开机启动、日志、重启和设备重新 claim。
- [ ] Merlin：记录实际 CPU/libc、libusb 和持久化路径后验收；群晖：记录 Container Manager 的设备映射、组权限和热插拔结果。
- [ ] 按运维手册回滚一次旧产物及原配置，重复证书、reset、签名和客户端认证检查。

## 维护约束

改动 [service.go](../internal/biz/service.go) 时保持完整芯片序列串行、锁内二次检查幂等缓存、reset 不清缓存；等锁可取消，已开始的硬件操作继续完成。

锁顺序为 `chipGate → cache.mu` 或 `chipGate → transport.ioMu → transport.sessionMu`，不得反向获取；`recentStore.mu` 不参与嵌套。寄存器读取重试必须包含 select 和 read 两步。协议变更需同时核对上游 [`3ac55e3`](https://github.com/shilapi/xcertplay/tree/3ac55e3) 与本仓库 API 契约。
