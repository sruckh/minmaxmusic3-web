// Package config loads the application's environment. Secrets
// (RUNPOD_API_KEY, LLM_API_KEY) are injected at container start by the
// Infisical entrypoint; they are never given defaults and never logged —
// only their presence is reported. Missing secrets degrade loudly (the
// features that need them fail with recorded reasons), they never block
// boot.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Addr        string // listen address
	PublicURL   string
	WebDir      string // templates + static root, so no CWD dependence
	DBPath      string
	AudioDir    string
	MaxInFlight int

	RunPodEndpoint string
	RunPodAPIKey   string
	// Yue2Endpoint is the second inference engine's endpoint.
	Yue2Endpoint string
	// Yue2APIKey is optional. Both engines most likely share one RunPod
	// account, in which case RunPodAPIKey covers both and this stays empty.
	//
	// It exists because that is not guaranteed: RunPod can scope a key to
	// named endpoints, and a key that is not granted this endpoint answers
	// 403 for it while continuing to work for the other. The probe that
	// established this is recorded in stage 09. Set this only if the shared
	// key cannot be granted the endpoint; see Yue2Key.
	Yue2APIKey         string
	LLMBaseURL         string
	LLMAPIKey          string
	LLMModelID         string
	LLMThinking        string
	LLMReasoningEffort string

	// AdminUser and AdminPassword are the static administrator credentials,
	// injected by Infisical. They never touch the database and never have a
	// default — see AdminLoginEnabled.
	AdminUser     string
	AdminPassword string
}

// AdminLoginEnabled reports whether the static administrator can sign in.
// Both halves must be present: a blank half disables admin login outright
// rather than falling back to a default credential, so a misconfigured deploy
// has no administrator instead of a guessable one.
func (c *Config) AdminLoginEnabled() bool {
	return c.AdminUser != "" && c.AdminPassword != ""
}

// Yue2Key is the credential the YuE2 endpoint is called with: its own if one
// was supplied, otherwise the shared RunPod key.
func (c *Config) Yue2Key() string {
	if c.Yue2APIKey != "" {
		return c.Yue2APIKey
	}
	return c.RunPodAPIKey
}

// Yue2Enabled reports whether the second engine can be offered at all.
//
// The API key is part of the test, not just the endpoint: an endpoint with no
// usable key would queue jobs that could never be submitted. Offering an
// engine that cannot run is worse than not offering it — the selector would
// look like a feature and behave like a failure message.
//
// This cannot detect a key that is present but not *authorised* for the
// endpoint; that only surfaces on the first call, as a 403. Probing at boot
// would add a network dependency to startup to answer a question that is
// answered correctly, and legibly, per job.
func (c *Config) Yue2Enabled() bool {
	return c.Yue2Endpoint != "" && c.Yue2Key() != ""
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads the environment. Invalid values fail fast; missing secrets do
// not (see package comment).
func Load() (*Config, error) {
	c := &Config{
		Addr:               env("MM3_ADDR", ":8080"),
		PublicURL:          env("MM3_PUBLIC_URL", ""),      // optional pin for the trusted external origin
		WebDir:             env("MM3_WEB_DIR", "/app/web"), // absolute: no CWD dependence
		DBPath:             env("MM3_DB_PATH", "/data/mm3.db"),
		AudioDir:           env("MM3_AUDIO_DIR", "/data/audio"),
		MaxInFlight:        2,
		RunPodEndpoint:     os.Getenv("RUNPOD_ENDPOINT"),
		RunPodAPIKey:       os.Getenv("RUNPOD_API_KEY"),
		Yue2Endpoint:       os.Getenv("YUE2_RUNPOD_ENDPOINT"),
		Yue2APIKey:         os.Getenv("YUE2_RUNPOD_API_KEY"),
		LLMBaseURL:         os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:          os.Getenv("LLM_API_KEY"),
		LLMModelID:         os.Getenv("LLM_MODEL_ID"),
		LLMThinking:        env("LLM_THINKING", "disabled"),
		LLMReasoningEffort: env("LLM_REASONING_EFFORT", "none"),
		AdminUser:          os.Getenv("ADMIN_USER"),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
	}
	if v := os.Getenv("MM3_MAX_IN_FLIGHT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("config: MM3_MAX_IN_FLIGHT must be an integer >= 1, got %q", v)
		}
		c.MaxInFlight = n
	}
	for name, v := range map[string]string{
		"MM3_ADDR": c.Addr, "MM3_WEB_DIR": c.WebDir,
		"MM3_DB_PATH": c.DBPath, "MM3_AUDIO_DIR": c.AudioDir,
	} {
		if v == "" {
			return nil, errors.New("config: " + name + " must not be empty")
		}
	}
	return c, nil
}

// Summary returns a loggable one-line status: values for non-secrets,
// presence flags for secrets.
func (c *Config) Summary() string {
	return fmt.Sprintf("addr=%s web=%s db=%s audio=%s in_flight=%d runpod_endpoint=%s runpod_key=%t yue2_endpoint=%s yue2_enabled=%t yue2_own_key=%t llm_base=%s llm_model=%s llm_key=%t llm_thinking=%s llm_reasoning_effort=%s admin_user=%s admin_password=%t admin_login=%t",
		c.Addr, c.WebDir, c.DBPath, c.AudioDir, c.MaxInFlight,
		present(c.RunPodEndpoint), c.RunPodAPIKey != "",
		present(c.Yue2Endpoint), c.Yue2Enabled(), c.Yue2APIKey != "",
		present(c.LLMBaseURL), present(c.LLMModelID), c.LLMAPIKey != "",
		c.LLMThinking, c.LLMReasoningEffort,
		present(c.AdminUser), c.AdminPassword != "", c.AdminLoginEnabled(),
	)
}

func present(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "set"
}
