# 02 · 配置与运维

适用 Linux `amd64/arm64/arm`。部署前确认架构、libc、CH341 实测 VID:PID、运行身份和目标版本；保存原启动参数、二进制或镜像 digest，便于回滚。一片 CH341 同时只由一个服务进程占用。首次成功后证书会在进程内缓存，更换 CH341/MFi 设备时必须重启服务，否则会继续返回旧证书。

## 配置

程序只读取命令行 flags，未传入的参数使用内建默认值；不会读取环境变量作为应用配置。定义见 [config.go](../internal/config/config.go)，修改参数后需重启进程或重建容器。

| Flag | 默认值 | 含义 |
| --- | --- | --- |
| `--http-addr` | `:8972` | HTTP 监听地址 |
| `--bearer-token` | 空 | 共享 token；空值关闭业务及诊断接口鉴权 |
| `--usb-ids` | `1a86:5512` | 逗号分隔的十六进制 `vid:pid` 候选列表 |
| `--i2c-address` | `0x11` | MFi 7-bit I2C 地址，可用十六进制或十进制 |
| `--i2c-speed-khz` | `100` | 可选 `20/100/400/750` |
| `--log-level` | `info` | `debug/info/warn/error` |
| `--log-format` | `json` | `json` 或 `text` |
| `--version` | - | 输出版本后退出 |
| `--help` / `-h` | - | 输出帮助后退出 |

诊断页和日志使用系统本地时区。程序不解析 `TZ`，也不加载指定名称的时区。完整参数及当前默认值以 `remote-mfi-for-xcertplay --help` 为准。`--bearer-token` 会出现在进程命令行及容器配置中，仅应部署在访问受控的主机上。

## USB 权限

先用 `lsusb` 确认设备确实处于 CH341 I2C 模式，再记录节点路径：

```sh
lsusb
# 将下面路径替换为实际 Bus/Device 编号
ls -l /dev/bus/usb/001/007
udevadm info -q property -n /dev/bus/usb/001/007
```

访问设备需同时满足节点可见、device cgroup 放行（如有）和读写权限。Linux usbfs 的字符设备主设备号是 `189`，上面的 `udevadm` 输出可核对 `MAJOR=189`；拔插后次设备号可能变化。

普通用户需要节点读写权限。以下使用 `plugdev` 组；若已有合适权限，可跳过：

```sh
sudo groupadd -f plugdev
```

在 `/etc/udev/rules.d/50-mfi-ch341.rules` 写入规则，将 VID:PID 换成实测值：

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="1a86", ATTR{idProduct}=="5512", MODE="0660", GROUP="plugdev", TAG+="uaccess"
```

```sh
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=usb
```

重新插拔后，用 `ls -l` 确认节点组为 `plugdev` 且组可读写。宿主机普通用户还需 `sudo usermod -aG plugdev "$USER"` 并重新登录。

宿主机 root 通常无需 udev 权限规则。默认 Docker 镜像以容器 root 运行，rootful、未启用用户命名空间且保留默认能力时通常也可省去 udev 规则；仍需 USB 挂载和 device cgroup 放行。rootless、用户命名空间和 seccomp/apparmor 收紧策略需另行验证。

## Docker

镜像以容器 root 运行，简化 USB 权限。宿主机需允许 rootful Docker 且未启用用户命名空间：

```sh
MFI_IMAGE='ghcr.io/cuckoohello/remote-mfi-for-xcertplay:vX.Y.Z'
docker pull "$MFI_IMAGE"
docker run -d \
  --name remote-mfi-for-xcertplay \
  --restart unless-stopped \
  -p 8972:8972 \
  --device-cgroup-rule='c 189:* rmw' \
  -v /dev/bus/usb:/dev/bus/usb \
  "$MFI_IMAGE" \
  --bearer-token='替换为足够长的随机字符串'
```

VID:PID 非默认值时在镜像名后补充 `--usb-ids='实测值'`。整个 USB bus 挂载和 `189:*` 放行让新设备节点可见；通配符不能按 VID:PID 过滤。更换 CH341/MFi 设备后必须重启容器，证书才会重新读取。应用能否在拔插同一设备后恢复仍需实测。

仓库内 `docker-compose.yml` 使用 `.env` 做 Compose 模板插值，再通过 `command` 将结果作为 flags 传给程序；容器内不设置 `MFI_*` 应用环境变量。复制 `.env.example` 为 `.env`、填写 token 后运行 `docker compose up -d`。

核对身份、设备节点和健康检查：

```sh
docker exec remote-mfi-for-xcertplay id
docker exec remote-mfi-for-xcertplay sh -c 'ls -l /dev/bus/usb/*/*'
docker inspect --format '{{.State.Health.Status}}' remote-mfi-for-xcertplay
docker logs --tail 100 remote-mfi-for-xcertplay
```

`HEALTHCHECK` 检查 `/healthz` 的 `ok`，Docker 的 `unhealthy` 标记本身不会触发 `--restart`。不设 token 时，将端口映射改为 `-p 127.0.0.1:8972:8972` 或限制在隔离网络；HTTPS 由外部代理提供。

## 宿主机二进制

用 `uname -m` 和 `ldd --version` 确认 CPU/libc。glibc 产物基于 Ubuntu 22.04，要求 glibc ≥ 2.35；musl 产物基于 Alpine 3.20，不承诺更旧版本兼容。若目标机器 glibc 版本较旧（例如 Asuswrt-Merlin 388.x 使用 Buildroot glibc 2.26），使用专门的 `linux_arm_merlin` 产物；虽然 RT-AX86U 等机型内核是 aarch64，但用户空间是 armv7l。该产物由官方 [am-toolchains](https://github.com/RMerl/am-toolchains) 的 armv7 glibc 2.26 交叉工具链构建，动态链接固件自带的 `libusb-1.0.so.0`。安装 `libusb-1.0` 的方法见 [README](../README.zh-CN.md#快速开始)。

以下以 `amd64/glibc` 为例，替换版本和平台后下载：

```sh
VERSION=vX.Y.Z
ARCH=amd64
LIBC=glibc
ARCHIVE="remote-mfi-for-xcertplay_${VERSION}_linux_${ARCH}_${LIBC}.tar.gz"
RELEASE_URL="https://github.com/cuckoohello/remote-mfi-for-xcertplay/releases/download/${VERSION}"
curl -fLO "${RELEASE_URL}/${ARCHIVE}"
curl -fLO "${RELEASE_URL}/${ARCHIVE}.sha256"
sha256sum -c "${ARCHIVE}.sha256"
tar xzf "$ARCHIVE"
ldd ./remote-mfi-for-xcertplay
./remote-mfi-for-xcertplay --version
```

确认 `libusb-1.0.so.0` 可解析、版本正确后前台启动：

```sh
./remote-mfi-for-xcertplay \
  --bearer-token='替换为足够长的随机字符串'
```

长期运行可交给系统的进程管理器。systemd 示例：先创建专用 `mfi` 用户（若不存在），并确认 `plugdev` 及其设备权限已配置：

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin mfi
sudo install -m 0755 remote-mfi-for-xcertplay /usr/local/bin/remote-mfi-for-xcertplay
```

将 token 按 `MFI_BEARER_TOKEN=value` 写入 `/etc/remote-mfi-for-xcertplay.env`，文件由 root 所有、权限 `0600`。该文件仅供 systemd 展开启动命令，程序本身不读取它。创建 `/etc/systemd/system/remote-mfi-for-xcertplay.service`：

```ini
[Unit]
Description=Remote MFi for xcertplay
After=network.target

[Service]
User=mfi
Group=plugdev
EnvironmentFile=/etc/remote-mfi-for-xcertplay.env
ExecStart=/usr/local/bin/remote-mfi-for-xcertplay --bearer-token ${MFI_BEARER_TOKEN}
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now remote-mfi-for-xcertplay
sudo journalctl -u remote-mfi-for-xcertplay -f
```

## Asuswrt-Merlin 路由器

以 RT-AX86U (HND 5.02L：内核 aarch64，用户空间 armv7l，Buildroot glibc 2.26，`/usr/lib/libusb-1.0.so.0` 已随固件提供) 为例。根分区只读，只有 `/jffs` 可持久化：

```sh
mkdir -p /jffs/opt/remote-mfi
cd /jffs/opt/remote-mfi
VERSION=vX.Y.Z
ARCHIVE="remote-mfi-for-xcertplay_${VERSION}_linux_arm_merlin.tar.gz"
RELEASE_URL="https://github.com/cuckoohello/remote-mfi-for-xcertplay/releases/download/${VERSION}"
curl -fLO "${RELEASE_URL}/${ARCHIVE}"
curl -fLO "${RELEASE_URL}/${ARCHIVE}.sha256"
# Merlin 固件默认没有 sha256sum，改用 openssl 校验：
expected=$(cut -d" " -f1 "${ARCHIVE}.sha256")
actual=$(openssl dgst -sha256 "${ARCHIVE}" | awk "{print \$2}")
test "$expected" = "$actual"
tar xzf "$ARCHIVE"
./remote-mfi-for-xcertplay --version
ldd ./remote-mfi-for-xcertplay | grep libusb-1.0.so.0
```

Merlin 上通常以 `admin` (uid 0) 运行，无需额外 udev 规则；路由器内核已导出 `/dev/bus/usb/*`。诊断时间直接跟随 Merlin 系统本地时区。

### 常驻脚本

Merlin 没有 systemd。用 `/jffs/scripts/services-start` 作为开机钩子，`cru` 做保活兜底；服务进程 `nohup` 到后台，日志追加到 `/jffs/opt/remote-mfi/logs/service.log`。先在 Web UI `Administration → System → Enable JFFS custom scripts and configs = Yes`，否则钩子不执行。

`/jffs/opt/remote-mfi/config`（普通 shell 变量；token 一次性生成，`chmod 600`，避免落 shell 历史）：

```sh
umask 077
cat >/jffs/opt/remote-mfi/config <<EOF
HTTP_ADDR=:8972
BEARER_TOKEN=$(openssl rand -hex 32)
USB_IDS=1a86:5512
I2C_ADDRESS=0x11
I2C_SPEED_KHZ=100
LOG_LEVEL=info
LOG_FORMAT=json
EOF
chmod 600 /jffs/opt/remote-mfi/config
```

`/jffs/opt/remote-mfi/run.sh`（`chmod +x`）：

```sh
#!/bin/sh
BASE=/jffs/opt/remote-mfi
BIN=$BASE/remote-mfi-for-xcertplay
PIDFILE=$BASE/run.pid
LOG=$BASE/logs/service.log
mkdir -p "$BASE/logs"

is_alive() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

case "$1" in
  start)
    is_alive && exit 0
    . "$BASE/config"
    nohup "$BIN" \
      --http-addr="$HTTP_ADDR" \
      --bearer-token="$BEARER_TOKEN" \
      --usb-ids="$USB_IDS" \
      --i2c-address="$I2C_ADDRESS" \
      --i2c-speed-khz="$I2C_SPEED_KHZ" \
      --log-level="$LOG_LEVEL" \
      --log-format="$LOG_FORMAT" \
      >>"$LOG" 2>&1 &
    echo $! >"$PIDFILE"
    ;;
  stop)
    is_alive && kill "$(cat "$PIDFILE")" 2>/dev/null
    rm -f "$PIDFILE"
    ;;
  status)
    is_alive && echo "running $(cat "$PIDFILE")" || { echo stopped; exit 1; }
    ;;
  restart) "$0" stop; sleep 1; "$0" start ;;
  *) echo "usage: $0 {start|stop|status|restart}" >&2; exit 2 ;;
esac
```

`/jffs/scripts/services-start`（追加两行，脚本已存在时勿覆盖）：

```sh
#!/bin/sh
/jffs/opt/remote-mfi/run.sh start
cru a remote_mfi '*/1 * * * * /jffs/opt/remote-mfi/run.sh start'
```

`/jffs/scripts/services-stop` 追加：

```sh
#!/bin/sh
cru d remote_mfi
/jffs/opt/remote-mfi/run.sh stop
```

`chmod +x` 两个 `services-*` 脚本；首次不重启也可以 `sh /jffs/scripts/services-start` 手动触发。重启路由器后自动拉起，异常退出后一分钟内被 `cru` 恢复；`run.sh start` 内建 pid 存活检测，重复调用幂等。

### 验证

```sh
/jffs/opt/remote-mfi/run.sh status
BASE_URL=http://127.0.0.1:8972
. /jffs/opt/remote-mfi/config
curl -fsS "$BASE_URL/healthz"
curl -fsS -H "Authorization: Bearer $BEARER_TOKEN" \
  -H 'Accept: application/json' "$BASE_URL/debug/usb" | head -c 400
```

浏览器打开 `http://<router>:8972/debug/usb?token=<token>`，token 从 `/jffs/opt/remote-mfi/config` 里取。

## 验证与诊断

在有 token 的部署上执行：

```sh
BASE_URL=http://127.0.0.1:8972
TOKEN='部署时配置的 token'
curl -fsS "$BASE_URL/healthz"
curl -fsS -H "Authorization: Bearer $TOKEN" \
  -H 'Accept: application/json' "$BASE_URL/debug/usb"
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/mfi/certificate"
curl -fsS -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{}' "$BASE_URL/mfi/reset"
```

`/healthz` 的 HTTP 200 只表示响应成功，需检查 `ok`。浏览器可打开 `http://HOST:8972/debug/usb?token=TOKEN`，查看 USB、锁状态和最近请求；共享截图或日志前移除 URL 中的 token。

| 现象 | 检查方向 |
| --- | --- |
| `missing` / 无候选 USB | 对照宿主 `lsusb`、容器挂载及 `--usb-ids` |
| `error` / permission denied | 对照设备节点权限、进程 `id`、cgroup 和用户映射 |
| USB device busy | 排查占用同一设备的进程或内核驱动；保留 `dmesg`、`lsusb -t` 结果 |
| `ready` 但读证书或签名失败 | 检查 MFi 供电、接线、I2C 地址与错误响应；ready 只检查 USB/会话 |
| `chip busy` 增多 / `lockHeldMs` 长期增长 | 保存当前锁持有者、最近请求及服务日志；8 秒只限制等待者，不会终止持锁操作 |
| 拔插后仍失败 | 保存拔插前后 Bus/Device 和日志，再停止流量并重启验证；勿把 ready 当作恢复证据 |
| 更换 CH341/MFi 设备但证书未变 | 证书是进程级缓存，必须重启服务才能重新读取 |

默认日志是 stdout 单行 JSON，请求日志 `msg` 为 `http_request`，包含 `method/path/status/duration_ms/note/request_id/chip_wait_ms/chip_duration_ms`。错误请求的芯片耗时字段可能为零。当前没有寄存器 hex 日志或 pprof 端点。

出现异常时先停止相关测试，保存命令、响应、时间、版本和日志，再决定修复或回滚。完整验收见[测试清单](03-acceptance-checklist.md)，至少完成一次真实证书读取、签名及 xcertplay `AA05` 验证。

## 升级与回滚

1. 保存旧镜像 digest 或二进制、启动参数及 udev 文件备份。
2. 拉取/下载新版本，校验 SHA-256、架构和 `--version`，暂停客户端认证流量。
3. Docker：停止并移除旧容器，用原参数和新镜像重建。宿主机：停止服务，替换二进制后启动。避免两个进程同时占用 CH341。
4. 逐项验证 `/healthz`、USB 诊断、证书哈希、reset、真实签名及客户端认证。
5. 失败时先保存证据，停止新进程，以旧镜像/二进制和原配置重新启动，再重复上述验证。

若修改了 `/etc/udev/rules.d/50-mfi-ch341.rules`，回滚时恢复备份；只有本次新建的文件才删除。随后 reload、trigger 并重新插拔，核对节点权限。重启会清空证书缓存和签名缓存，旧 requestId 的重试会重新触发芯片；更换 CH341/MFi 设备后也必须重启，否则证书缓存仍为旧值。

## 构建与发布

开发依赖和 `make build` 参数见 [README](../README.zh-CN.md#开发)。本机架构镜像可运行 `docker build -t remote-mfi-for-xcertplay:dev .`，构建细节由 [Dockerfile](../Dockerfile) 维护。

[CI](../.github/workflows/ci.yml) 验证测试、race、vet、格式及构建；[release](../.github/workflows/release.yml) 在 `v*` tag 推送时执行发布。Docker、glibc、musl 均使用目标 CPU 的原生 runner。发布结果为 `linux/amd64`、`linux/arm64` 镜像，以及四种 CPU/libc 组合和一种 Merlin 专用 tarball 及配套 SHA-256 文件。
