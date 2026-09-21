package config

import (
	"strings"
	"testing"
)

// The second engine may be granted by the shared RunPod key or by one scoped to
// its endpoint, and the app must not care which. RunPod refuses a key that has
// not been granted an endpoint with a 403 while that same key keeps working for
// the other endpoint — so the two cases have to be expressible in configuration
// rather than assumed.
func TestYue2KeyFallsBackToTheSharedRunPodKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantKey string
		wantOn  bool
		wantOwn bool
	}{
		{
			name:    "no endpoint, no engine — regardless of keys",
			cfg:     Config{RunPodAPIKey: "shared"},
			wantKey: "shared", wantOn: false,
		},
		{
			name:    "endpoint with the shared key",
			cfg:     Config{Yue2Endpoint: "https://api.runpod.ai/v2/abc", RunPodAPIKey: "shared"},
			wantKey: "shared", wantOn: true, wantOwn: false,
		},
		{
			name: "endpoint with a key of its own",
			cfg: Config{
				Yue2Endpoint: "https://api.runpod.ai/v2/abc",
				RunPodAPIKey: "shared", Yue2APIKey: "scoped",
			},
			wantKey: "scoped", wantOn: true, wantOwn: true,
		},
		{
			// An endpoint with no key at all would queue jobs that could never
			// be submitted, so it is not offered.
			name:    "endpoint with no key anywhere",
			cfg:     Config{Yue2Endpoint: "https://api.runpod.ai/v2/abc"},
			wantKey: "", wantOn: false,
		},
		{
			// The scoped key alone is enough: a deployment that never sets the
			// shared one still runs the second engine.
			name:    "scoped key without the shared one",
			cfg:     Config{Yue2Endpoint: "https://api.runpod.ai/v2/abc", Yue2APIKey: "scoped"},
			wantKey: "scoped", wantOn: true, wantOwn: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Yue2Key(); got != tc.wantKey {
				t.Errorf("Yue2Key() = %q, want %q", got, tc.wantKey)
			}
			if got := tc.cfg.Yue2Enabled(); got != tc.wantOn {
				t.Errorf("Yue2Enabled() = %v, want %v", got, tc.wantOn)
			}
		})
	}
}

// The presence of a separate key is reported, never the key. It reaches a log
// line read by whoever is diagnosing a deploy.
func TestSummaryReportsKeyPresenceNotValue(t *testing.T) {
	c := &Config{
		Addr: ":8080", WebDir: "/app/web", DBPath: "/data/mm3.db", AudioDir: "/data/audio",
		RunPodEndpoint: "https://api.runpod.ai/v2/abc", RunPodAPIKey: "shared-secret-value",
		Yue2Endpoint: "https://api.runpod.ai/v2/def", Yue2APIKey: "scoped-secret-value",
	}
	s := c.Summary()

	for _, secret := range []string{"shared-secret-value", "scoped-secret-value"} {
		if strings.Contains(s, secret) {
			t.Errorf("Summary leaked a key value: %s", s)
		}
	}
	for _, want := range []string{"yue2_endpoint=set", "yue2_enabled=true", "yue2_own_key=true"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary missing %q: %s", want, s)
		}
	}
}
