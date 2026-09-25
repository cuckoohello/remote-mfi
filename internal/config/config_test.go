package config

import (
	"bytes"
	"errors"
	"flag"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	config, showVersion, err := Parse(nil, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if showVersion {
		t.Fatal("showVersion = true, want false")
	}
	if config.HTTPAddr != ":8972" ||
		config.BearerToken != "" ||
		config.I2CAddress != 0x11 ||
		config.I2CSpeedKHz != 100 ||
		config.LogLevel != slog.LevelInfo ||
		config.LogFormat != "json" ||
		config.Location != time.Local ||
		config.RecentCapacity != 20 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if len(config.USBIDs) != 1 || config.USBIDs[0].String() != "1a86:5512" {
		t.Fatalf("unexpected USB IDs: %+v", config.USBIDs)
	}
}

func TestParseFlags(t *testing.T) {
	config, showVersion, err := Parse([]string{
		"--http-addr=127.0.0.1:9090",
		"--bearer-token=secret",
		"--usb-ids=1a86:5512,1a86:5523",
		"--i2c-address=0x10",
		"--i2c-speed-khz=400",
		"--log-level=debug",
		"--log-format=text",
	}, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if showVersion {
		t.Fatal("showVersion = true, want false")
	}
	if config.HTTPAddr != "127.0.0.1:9090" ||
		config.BearerToken != "secret" ||
		config.I2CAddress != 0x10 ||
		config.I2CSpeedKHz != 400 ||
		config.LogLevel != slog.LevelDebug ||
		config.LogFormat != "text" ||
		config.Location != time.Local {
		t.Fatalf("unexpected config: %+v", config)
	}
	if len(config.USBIDs) != 2 {
		t.Fatalf("USB ID count = %d, want 2", len(config.USBIDs))
	}
}

func TestParseRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "address", args: []string{"--http-addr=8972"}, wantErr: "--http-addr"},
		{name: "USB ID", args: []string{"--usb-ids=not-an-id"}, wantErr: "--usb-ids"},
		{name: "I2C address", args: []string{"--i2c-address=0x80"}, wantErr: "--i2c-address"},
		{name: "speed", args: []string{"--i2c-speed-khz=200"}, wantErr: "--i2c-speed-khz"},
		{name: "log level", args: []string{"--log-level=trace"}, wantErr: "--log-level"},
		{name: "log format", args: []string{"--log-format=xml"}, wantErr: "--log-format"},
		{name: "unknown flag", args: []string{"--unknown"}, wantErr: "flag provided but not defined"},
		{name: "positional argument", args: []string{"extra"}, wantErr: "unexpected positional arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Parse(test.args, nil)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Parse error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	for _, helpFlag := range []string{"--help", "-h"} {
		t.Run(helpFlag, func(t *testing.T) {
			var output bytes.Buffer
			_, _, err := Parse([]string{helpFlag}, &output)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("Parse error = %v, want flag.ErrHelp", err)
			}
			for _, text := range []string{
				"Usage: remote-mfi-for-xcertplay [options]",
				"http-addr",
				"bearer-token",
				"usb-ids",
				"i2c-address",
				"i2c-speed-khz",
				"log-level",
				"log-format",
				"version",
			} {
				if !strings.Contains(output.String(), text) {
					t.Errorf("help output missing %q:\n%s", text, output.String())
				}
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	_, showVersion, err := Parse([]string{"--version"}, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !showVersion {
		t.Fatal("showVersion = false, want true")
	}
}

func TestParseIgnoresEnvironment(t *testing.T) {
	for key, value := range map[string]string{
		"MFI_HTTP_ADDR":           "invalid",
		"MFI_BEARER_TOKEN":        "from-environment",
		"MFI_CH341_USB_IDS":       "invalid",
		"MFI_MFI_I2C_ADDRESS":     "invalid",
		"MFI_CH341_I2C_SPEED_KHZ": "invalid",
		"MFI_LOG_LEVEL":           "invalid",
		"MFI_LOG_FORMAT":          "invalid",
		"TZ":                      "Not/AZone",
	} {
		t.Setenv(key, value)
	}

	config, _, err := Parse(nil, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.HTTPAddr != defaultHTTPAddr || config.BearerToken != "" ||
		config.USBIDs[0].String() != defaultUSBIDs ||
		config.I2CAddress != 0x11 ||
		config.I2CSpeedKHz != defaultI2CSpeedKHz ||
		config.LogLevel != slog.LevelInfo ||
		config.LogFormat != defaultLogFormat ||
		config.Location != time.Local {
		t.Fatalf("environment affected config: %+v", config)
	}
}
