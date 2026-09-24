# remote-mfi

[English](README.md) | [中文](README.zh-CN.md)

`remote-mfi` exposes a physical MFi authentication coprocessor connected through a CH341 USB-I2C bridge as the HTTP API consumed by [shilapi/xcertplay](https://github.com/shilapi/xcertplay).

The service runs on Linux `amd64` and `arm64`, either as a multi-architecture Docker image or as a dynamically linked host binary.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/mfi/certificate` | Read protocol major and certificate from the MFi chip |
| `POST` | `/mfi/sign` | Sign a base64 challenge; retries are idempotent by `requestId` for 60 seconds |
| `POST` | `/mfi/reset` | Compatibility no-op used when xcertplay starts a remote session |
| `GET` | `/debug/usb` | Read-only USB, runtime, and recent-request diagnostics (HTML or JSON) |
| `GET` | `/healthz` | Unauthenticated three-state health probe (`ready`, `missing`, `error`) |

All chip operations are globally serialized. Concurrent xcertplay clients cannot interleave register sequences on the same physical coprocessor.

The frozen wire contract is documented in [docs/02-api-contract.md](docs/02-api-contract.md).

## Requirements

- Linux `amd64` or `arm64`
- CH341 USB-I2C bridge connected to an MFi authentication coprocessor
- CH341 available through `/dev/bus/usb`
- `libusb-1.0` (included in the Docker image; required separately for host binaries)
- A udev rule granting the runtime user access to the selected CH341 VID:PID

The default USB identity is `1a86:5512`. Verify the actual value with `lsusb`; it is configurable.

```udev
# /etc/udev/rules.d/50-mfi-ch341.rules
SUBSYSTEM=="usb", ATTR{idVendor}=="1a86", ATTR{idProduct}=="5512", MODE="0660", GROUP="plugdev", TAG+="uaccess"
```

Reload the rule and reconnect the device:

```sh
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=usb
```

## Docker

The public multi-architecture image is published to GHCR:

```sh
docker pull ghcr.io/cuckoohello/remote-mfi:v0.1.1
```

For hotplug support, bind the USB bus and allow USB character-device major `189`. Pass the host `plugdev` numeric GID to the non-root container process:

```sh
USB_GID="$(getent group plugdev | cut -d: -f3)"

docker run -d \
  --name remote-mfi \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MFI_BEARER_TOKEN='replace-with-a-long-random-token' \
  -e MFI_CH341_USB_IDS='1a86:5512' \
  --device-cgroup-rule='c 189:* rmw' \
  --group-add "$USB_GID" \
  -v /dev/bus/usb:/dev/bus/usb \
  ghcr.io/cuckoohello/remote-mfi:v0.1.1
```

`MFI_BEARER_TOKEN` is optional. When omitted, `/mfi/*` and `/debug/usb` are unauthenticated; use that mode only on an isolated network or loopback interface. `/healthz` is always unauthenticated.

Open `http://HOST:8080/debug/usb?token=TOKEN` to inspect USB visibility, chip status, lock state, idempotency cache size, and the latest 20 business requests.

## Host Binary

Choose the GitHub Release archive matching both CPU architecture and libc:

- `linux_amd64_glibc`
- `linux_amd64_musl`
- `linux_arm64_glibc`
- `linux_arm64_musl`

The glibc artifacts are built on Debian 12 and require glibc 2.36 or newer.

Install the runtime dependency:

```sh
# Debian / Ubuntu
sudo apt install libusb-1.0-0

# RHEL / Rocky / CentOS
sudo dnf install libusbx

# Alpine
sudo apk add libusb
```

Then run:

```sh
export MFI_BEARER_TOKEN='replace-with-a-long-random-token'
export MFI_CH341_USB_IDS='1a86:5512'
export MFI_MFI_I2C_ADDRESS='0x11'
export MFI_CH341_I2C_SPEED_KHZ='100'
remote-mfi
```

Host binaries are dynamically linked. Use `ldd ./remote-mfi` to confirm that the selected artifact matches the host libc and resolves `libusb-1.0.so.0`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `MFI_HTTP_ADDR` | `:8080` | HTTP listen address |
| `MFI_BEARER_TOKEN` | empty | Optional shared Bearer token |
| `MFI_CH341_USB_IDS` | `1a86:5512` | Comma-separated lowercase hexadecimal `vid:pid` candidates |
| `MFI_MFI_I2C_ADDRESS` | `0x11` | MFi coprocessor 7-bit I2C address |
| `MFI_CH341_I2C_SPEED_KHZ` | `100` | One of `20`, `100`, `400`, `750` |
| `MFI_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MFI_LOG_FORMAT` | `json` | `json` or `text` |
| `TZ` | `Asia/Shanghai` | Time zone used for logs and diagnostics |

## Development

Install Go 1.23+, a C toolchain, `pkg-config`, and libusb development headers.

```sh
make check
make build
./remote-mfi --version
```

`make check` runs unit tests, the race detector, and `go vet`. Tests use scripted transports and do not require USB hardware. CH341 integration and real CarPlay `AA05 AuthenticationSucceeded` acceptance still require physical hardware.

## Documentation

- [Overview](docs/00-overview.md)
- [Requirements](docs/01-requirements.md)
- [API contract](docs/02-api-contract.md)
- [Architecture](docs/03-architecture.md)
- [Operations runbook](docs/04-runbook.md)
- [Acceptance checklist](docs/05-acceptance-checklist.md)
- [Open questions](docs/06-open-questions.md)

## License

GPL-3.0-only. The CH341 and MFi protocol implementation follows the GPL-licensed xcertplay reference at commit [`3ac55e3`](https://github.com/shilapi/xcertplay/tree/3ac55e3).
