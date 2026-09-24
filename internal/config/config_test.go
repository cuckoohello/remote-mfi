package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.HTTPAddr != ":8080" ||
		config.I2CAddress != 0x11 ||
		config.I2CSpeedKHz != 100 ||
		config.LogFormat != "json" ||
		config.Location.String() != "Asia/Shanghai" {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if len(config.USBIDs) != 1 || config.USBIDs[0].String() != "1a86:5512" {
		t.Fatalf("unexpected USB IDs: %+v", config.USBIDs)
	}
}

func TestLoadCustomValues(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("MFI_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("MFI_BEARER_TOKEN", "secret")
	t.Setenv("MFI_CH341_USB_IDS", "1a86:5512,1a86:5523")
	t.Setenv("MFI_MFI_I2C_ADDRESS", "0x10")
	t.Setenv("MFI_CH341_I2C_SPEED_KHZ", "400")
	t.Setenv("MFI_LOG_LEVEL", "debug")
	t.Setenv("MFI_LOG_FORMAT", "text")
	t.Setenv("TZ", "UTC")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.HTTPAddr != "127.0.0.1:9090" ||
		config.BearerToken != "secret" ||
		config.I2CAddress != 0x10 ||
		config.I2CSpeedKHz != 400 ||
		config.LogFormat != "text" ||
		config.Location.String() != "UTC" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if len(config.USBIDs) != 2 {
		t.Fatalf("USB ID count = %d, want 2", len(config.USBIDs))
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "address", key: "MFI_HTTP_ADDR", value: "8080"},
		{name: "USB ID", key: "MFI_CH341_USB_IDS", value: "not-an-id"},
		{name: "I2C address", key: "MFI_MFI_I2C_ADDRESS", value: "0x80"},
		{name: "speed", key: "MFI_CH341_I2C_SPEED_KHZ", value: "200"},
		{name: "log level", key: "MFI_LOG_LEVEL", value: "trace"},
		{name: "log format", key: "MFI_LOG_FORMAT", value: "xml"},
		{name: "timezone", key: "TZ", value: "Not/AZone"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MFI_HTTP_ADDR",
		"MFI_BEARER_TOKEN",
		"MFI_CH341_USB_IDS",
		"MFI_MFI_I2C_ADDRESS",
		"MFI_CH341_I2C_SPEED_KHZ",
		"MFI_LOG_LEVEL",
		"MFI_LOG_FORMAT",
		"TZ",
	} {
		t.Setenv(key, "")
	}
}
