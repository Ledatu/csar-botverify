package config

import (
	"fmt"
	"testing"
)

func TestTransportModesAndRedisRequirements(t *testing.T) {
	for _, tc := range []struct {
		mode, addr    string
		active, valid bool
	}{
		{"", "", false, true},
		{"polling_standby", "", false, true},
		{"polling", "", false, false},
		{"polling", "127.0.0.1:6379", true, true},
		{"unknown", "", false, false},
	} {
		t.Run(tc.mode+tc.addr, func(t *testing.T) {
			data := fmt.Sprintf("service: {name: test, port: 8084}\ncustom:\n  redis: {addr: %q}\n  providers:\n    - name: telegram\n      bot_token: test\n      mode: %q\n      network: tcp6\n", tc.addr, tc.mode)
			cfg, err := LoadFromBytes([]byte(data))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err == nil && cfg.Custom.PollingActive != tc.active {
				t.Fatalf("active=%v, want %v", cfg.Custom.PollingActive, tc.active)
			}
		})
	}
}
