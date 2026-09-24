# remote-mfi-for-xcertplay

[English](README.md) | [中文](README.zh-CN.md)

Expose a physical MFi authentication coprocessor, connected through a CH341 USB-I2C bridge, as the Remote MFi HTTP API for [xcertplay](https://github.com/shilapi/xcertplay). Chip operations are serialized within the service so multiple clients can share it.

**Experimental: real MFi signing and CarPlay `AA05 AuthenticationSucceeded` have not been hardware-validated.** Asuswrt-Merlin and Synology Container Manager deployments also remain unverified.

## Quick start

Requires Linux `amd64` or `arm64`, a CH341/MFi device, and read/write access to its `/dev/bus/usb` node. Verify the VID:PID with `lsusb`; the default is `1a86:5512`.

Choose a binary from [Releases](https://github.com/cuckoohello/remote-mfi-for-xcertplay/releases) matching both CPU and libc, verify its accompanying `.sha256` file with `sha256sum -c`, and extract it. Available variants are `linux_{amd64,arm64}_{glibc,musl}` plus `linux_arm64_merlin` for Asuswrt-Merlin HND 5.02L routers (e.g. RT-AX86U 388.x, cross-built with the official `am-toolchains` glibc 2.26 crosstools). The default glibc baseline is 2.35; musl builds use Alpine 3.20.

Install the runtime dependencies for your distribution:

```sh
# Debian / Ubuntu
sudo apt install libusb-1.0-0 tzdata
# RHEL / Rocky
sudo dnf install libusbx tzdata
# Alpine
sudo apk add libusb tzdata
```

Start the extracted binary:

```sh
export MFI_BEARER_TOKEN='replace-with-a-long-random-token'
export TZ=Asia/Shanghai
./remote-mfi-for-xcertplay
```

If the USB identity differs, set `MFI_CH341_USB_IDS` to the observed VID:PID. For an ordinary user, configure device permissions as described in the [runbook](docs/02-runbook.md#usb-权限). Host root normally needs no udev permission rule.

For Docker, use `ghcr.io/cuckoohello/remote-mfi-for-xcertplay:<release-tag>` and follow the [Docker instructions](docs/02-runbook.md#docker). The image runs as container root, so you only need `-v /dev/bus/usb:/dev/bus/usb` plus `--device-cgroup-rule='c 189:* rmw'`.

The default HTTP address is `:8080`. Set `MFI_HTTP_ADDR=127.0.0.1:8080` for local access only. `MFI_BEARER_TOKEN` is optional; omitting it disables authentication on business and diagnostic endpoints. Use an isolated network or loopback in that mode. HTTPS requires an external proxy.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/mfi/certificate` | Read protocol major, certificate and SHA-256; cached process-wide after first success (restart to swap the device) |
| `POST` | `/mfi/sign` | Sign a base64 challenge; cache successful results by `requestId` for 60 seconds |
| `POST` | `/mfi/reset` | Compatibility no-op; accepts `{}` |
| `GET` | `/debug/usb` | USB and request diagnostics, HTML or JSON |
| `GET` | `/healthz` | Unauthenticated USB/session status; inspect the JSON `ok` field |

Set the xcertplay Remote MFi base URL to `http://HOST:8080` and use the same token. Open `http://HOST:8080/debug/usb?token=TOKEN` for diagnostics. A `ready` health response does not verify MFi signing.

## Development

Install Go 1.23+, Make, a C toolchain, `pkg-config`, and libusb development headers.

```sh
make check
make build
./remote-mfi-for-xcertplay --version
```

`make check` runs unit tests, the race detector, and `go vet`; it requires no USB hardware. Local builds, Docker and CI/release share the [Makefile](Makefile). Optional build variables are `OUTPUT`, `VERSION`, `COMMIT`, and `BUILD_DATE`; defaults are the project binary name, `dev`, the current short commit, and the current UTC time.

## Documentation

1. [API contract](docs/01-api-contract.md)
2. [Configuration and operations](docs/02-runbook.md)
3. [Testing and hardware acceptance](docs/03-acceptance-checklist.md)

Release archives contain the binary, both READMEs and the license. Additional documentation is available in the [repository](https://github.com/cuckoohello/remote-mfi-for-xcertplay/tree/main/docs).

## License

GPL-3.0-only. The CH341 and MFi protocol implementation follows the GPL-licensed xcertplay reference at commit [`3ac55e3`](https://github.com/shilapi/xcertplay/tree/3ac55e3).
