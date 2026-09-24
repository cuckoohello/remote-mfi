package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr      = ":8080"
	defaultUSBIDs        = "1a86:5512"
	defaultI2CAddress    = "0x11"
	defaultI2CSpeedKHz   = 100
	defaultLogLevel      = "info"
	defaultLogFormat     = "json"
	defaultTimezone      = "Asia/Shanghai"
	defaultRecentEntries = 20
)

type USBID struct {
	Vendor  uint16
	Product uint16
}

func (id USBID) String() string {
	return fmt.Sprintf("%04x:%04x", id.Vendor, id.Product)
}

type Config struct {
	HTTPAddr       string
	BearerToken    string
	USBIDs         []USBID
	I2CAddress     uint8
	I2CSpeedKHz    int
	LogLevel       slog.Level
	LogFormat      string
	Location       *time.Location
	RecentCapacity int
}

func Load() (Config, error) {
	httpAddr := envOrDefault("MFI_HTTP_ADDR", defaultHTTPAddr)
	if _, _, err := net.SplitHostPort(httpAddr); err != nil {
		return Config{}, fmt.Errorf("MFI_HTTP_ADDR: %w", err)
	}

	usbIDs, err := parseUSBIDs(envOrDefault("MFI_CH341_USB_IDS", defaultUSBIDs))
	if err != nil {
		return Config{}, fmt.Errorf("MFI_CH341_USB_IDS: %w", err)
	}

	i2cAddress, err := parseInteger(envOrDefault("MFI_MFI_I2C_ADDRESS", defaultI2CAddress), 7)
	if err != nil {
		return Config{}, fmt.Errorf("MFI_MFI_I2C_ADDRESS: %w", err)
	}

	speed, err := strconv.Atoi(envOrDefault("MFI_CH341_I2C_SPEED_KHZ", strconv.Itoa(defaultI2CSpeedKHz)))
	if err != nil || !validSpeed(speed) {
		return Config{}, fmt.Errorf("MFI_CH341_I2C_SPEED_KHZ must be one of 20, 100, 400, 750")
	}

	logLevel, err := parseLogLevel(envOrDefault("MFI_LOG_LEVEL", defaultLogLevel))
	if err != nil {
		return Config{}, err
	}
	logFormat := strings.ToLower(envOrDefault("MFI_LOG_FORMAT", defaultLogFormat))
	if logFormat != "json" && logFormat != "text" {
		return Config{}, fmt.Errorf("MFI_LOG_FORMAT must be json or text")
	}

	timezone := envOrDefault("TZ", defaultTimezone)
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Config{}, fmt.Errorf("TZ %q: %w", timezone, err)
	}

	return Config{
		HTTPAddr:       httpAddr,
		BearerToken:    os.Getenv("MFI_BEARER_TOKEN"),
		USBIDs:         usbIDs,
		I2CAddress:     uint8(i2cAddress),
		I2CSpeedKHz:    speed,
		LogLevel:       logLevel,
		LogFormat:      logFormat,
		Location:       location,
		RecentCapacity: defaultRecentEntries,
	}, nil
}

func envOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func parseUSBIDs(value string) ([]USBID, error) {
	parts := strings.Split(value, ",")
	ids := make([]USBID, 0, len(parts))
	seen := make(map[USBID]struct{}, len(parts))
	for _, part := range parts {
		fields := strings.Split(strings.TrimSpace(part), ":")
		if len(fields) != 2 {
			return nil, fmt.Errorf("%q must use lowercase hexadecimal vid:pid", part)
		}
		vendor, err := strconv.ParseUint(fields[0], 16, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid vendor ID %q", fields[0])
		}
		product, err := strconv.ParseUint(fields[1], 16, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid product ID %q", fields[1])
		}
		id := USBID{Vendor: uint16(vendor), Product: uint16(product)}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one vid:pid is required")
	}
	return ids, nil
}

func parseInteger(value string, bits int) (uint64, error) {
	number, err := strconv.ParseUint(value, 0, bits)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid %d-bit integer", value, bits)
	}
	return number, nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("MFI_LOG_LEVEL must be debug, info, warn, or error")
	}
}

func validSpeed(speed int) bool {
	return speed == 20 || speed == 100 || speed == 400 || speed == 750
}
