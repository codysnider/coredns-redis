package redis

import (
	"testing"

	"github.com/coredns/caddy"
)

func TestRedisParse(t *testing.T) {
	tests := []struct {
		desc             string
		input            string
		shouldErr        bool
		expectedAddress  string
		expectedPassword string
		expectedTtl      uint32
		expectedPrefix   string
		expectedSuffix   string
	}{
		{
			desc: "block without zone arg",
			input: `redis {
    address localhost:6380
    password secret
    ttl 600
    prefix dns_
    suffix _cache
}`,
			expectedAddress:  "localhost:6380",
			expectedPassword: "secret",
			expectedTtl:      600,
			expectedPrefix:   "dns_",
			expectedSuffix:   "_cache",
		},
		{
			desc: "block with zone arg (actual config format)",
			input: `redis example.com {
    address localhost:6379
    password foobared
    ttl 360
    prefix _dns:
}`,
			expectedAddress:  "localhost:6379",
			expectedPassword: "foobared",
			expectedTtl:      360,
			expectedPrefix:   "_dns:",
		},
		{
			desc:             "no block uses defaults",
			input:            `redis`,
			expectedAddress:  "",
			expectedPassword: "",
			expectedTtl:      300,
		},
		{
			desc: "unknown property returns error",
			input: `redis {
    bogus value
}`,
			shouldErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			c := caddy.NewTestController("dns", tc.input)
			r, err := redisParse(c)

			if tc.shouldErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if r.redisAddress != tc.expectedAddress {
				t.Errorf("address: got %q, want %q", r.redisAddress, tc.expectedAddress)
			}
			if r.redisPassword != tc.expectedPassword {
				t.Errorf("password: got %q, want %q", r.redisPassword, tc.expectedPassword)
			}
			if r.Ttl != tc.expectedTtl {
				t.Errorf("ttl: got %d, want %d", r.Ttl, tc.expectedTtl)
			}
			if r.keyPrefix != tc.expectedPrefix {
				t.Errorf("prefix: got %q, want %q", r.keyPrefix, tc.expectedPrefix)
			}
			if r.keySuffix != tc.expectedSuffix {
				t.Errorf("suffix: got %q, want %q", r.keySuffix, tc.expectedSuffix)
			}
		})
	}
}
