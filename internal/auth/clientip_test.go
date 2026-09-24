package auth

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	r, err := NewClientIPResolver("172.16.0.0/12, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		remote string
		cf     string
		xff    string
		want   string
	}{
		{"untrusted peer ignores headers", "203.0.113.9:1234", "198.51.100.1", "198.51.100.2", "203.0.113.9"},
		{"CF-Connecting-IP is never trusted", "172.16.15.1:5555", "198.51.100.1", "198.51.100.3", "198.51.100.3"},
		{"trusted peer walks XFF from the right", "127.0.0.1:5555", "", "6.6.6.6, 198.51.100.7, 172.16.0.2", "198.51.100.7"},
		{"trusted peer without headers", "127.0.0.1:5555", "", "", "127.0.0.1"},
		{"garbage XFF hop stops the walk", "127.0.0.1:5555", "", "198.51.100.7, junk", "127.0.0.1"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tc.remote
		if tc.cf != "" {
			req.Header.Set("CF-Connecting-IP", tc.cf)
		}
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := r.ClientIP(req); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	if _, err := NewClientIPResolver("not-an-ip"); err == nil {
		t.Error("expected error for invalid proxy entry")
	}
}

func TestRateKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":          "203.0.113.9",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"::ffff:203.0.113.9":   "::ffff:203.0.113.9",
		"not-an-ip":            "not-an-ip",
	} {
		if got := RateKey(in); got != want {
			t.Errorf("RateKey(%q) = %q, want %q", in, got, want)
		}
	}
}
