package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr      = ":8972"
	defaultUSBIDs        = "1a86:5512"
	defaultI2CAddress    = "0x11"
	defaultI2CSpeedKHz   = 100
	defaultLogLevel      = "info"
	defaultLogFormat     = "json"
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

// Parse parses command-line flags into the runtime configuration.
func Parse(args []string, output io.Writer) (Config, bool, error) {
	if output == nil {
		output = io.Discard
	}

	flags := flag.NewFlagSet("remote-mfi-for-xcertplay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var (
		httpAddr    string
		bearerToken string
		usbIDList   string
		i2cAddress  string
		speed       int
		logLevel    string
		logFormat   string
		showVersion bool
	)
	flags.StringVar(&httpAddr, "http-addr", defaultHTTPAddr, "HTTP listen address")
	flags.StringVar(&bearerToken, "bearer-token", "", "Bearer token; empty disables authentication")
	flags.StringVar(&usbIDList, "usb-ids", defaultUSBIDs, "comma-separated CH341 USB IDs as vid:pid")
	flags.StringVar(&i2cAddress, "i2c-address", defaultI2CAddress, "MFi 7-bit I2C address")
	flags.IntVar(&speed, "i2c-speed-khz", defaultI2CSpeedKHz, "CH341 I2C speed: 20, 100, 400, or 750")
	flags.StringVar(&logLevel, "log-level", defaultLogLevel, "log level: debug, info, warn, or error")
	flags.StringVar(&logFormat, "log-format", defaultLogFormat, "log format: json or text")
	flags.BoolVar(&showVersion, "version", false, "print version and exit")
	flags.Usage = func() {
		fmt.Fprintf(output, "Usage: %s [options]\n\nOptions:\n", flags.Name())
		flags.SetOutput(output)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}

	if err := flags.Parse(args); err != nil {
		return Config{}, false, err
	}
	if flags.NArg() != 0 {
		return Config{}, false, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if showVersion {
		return Config{}, true, nil
	}

	if _, _, err := net.SplitHostPort(httpAddr); err != nil {
		return Config{}, false, fmt.Errorf("--http-addr: %w", err)
	}

	usbIDs, err := parseUSBIDs(usbIDList)
	if err != nil {
		return Config{}, false, fmt.Errorf("--usb-ids: %w", err)
	}

	parsedI2CAddress, err := parseInteger(i2cAddress, 7)
	if err != nil {
		return Config{}, false, fmt.Errorf("--i2c-address: %w", err)
	}

	if !validSpeed(speed) {
		return Config{}, false, fmt.Errorf("--i2c-speed-khz must be one of 20, 100, 400, 750")
	}

	parsedLogLevel, err := parseLogLevel(logLevel)
	if err != nil {
		return Config{}, false, fmt.Errorf("--log-level: %w", err)
	}
	logFormat = strings.ToLower(logFormat)
	if logFormat != "json" && logFormat != "text" {
		return Config{}, false, fmt.Errorf("--log-format must be json or text")
	}

	return Config{
		HTTPAddr:       httpAddr,
		BearerToken:    bearerToken,
		USBIDs:         usbIDs,
		I2CAddress:     uint8(parsedI2CAddress),
		I2CSpeedKHz:    speed,
		LogLevel:       parsedLogLevel,
		LogFormat:      logFormat,
		Location:       time.Local,
		RecentCapacity: defaultRecentEntries,
	}, false, nil
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
		return 0, fmt.Errorf("must be debug, info, warn, or error")
	}
}

func validSpeed(speed int) bool {
	return speed == 20 || speed == 100 || speed == 400 || speed == 750
}
