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

	RunPodEndpoint     string
	RunPodAPIKey       string
	LLMBaseURL         string
	LLMAPIKey          string
	LLMModelID         string
	LLMThinking        string
	LLMReasoningEffort string
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
		Addr:           env("MM3_ADDR", ":8080"),
		PublicURL:      env("MM3_PUBLIC_URL", "https://mm3.gemneye.xyz"),
		WebDir:         env("MM3_WEB_DIR", "/app/web"), // absolute: no CWD dependence
		DBPath:         env("MM3_DB_PATH", "/data/mm3.db"),
		AudioDir:       env("MM3_AUDIO_DIR", "/data/audio"),
		MaxInFlight:    2,
		RunPodEndpoint:     os.Getenv("RUNPOD_ENDPOINT"),
		RunPodAPIKey:       os.Getenv("RUNPOD_API_KEY"),
		LLMBaseURL:         os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:          os.Getenv("LLM_API_KEY"),
		LLMModelID:         os.Getenv("LLM_MODEL_ID"),
		LLMThinking:        env("LLM_THINKING", "disabled"),
		LLMReasoningEffort: env("LLM_REASONING_EFFORT", "none"),
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
	return fmt.Sprintf("addr=%s web=%s db=%s audio=%s in_flight=%d runpod_endpoint=%s runpod_key=%t llm_base=%s llm_model=%s llm_key=%t llm_thinking=%s llm_reasoning_effort=%s",
		c.Addr, c.WebDir, c.DBPath, c.AudioDir, c.MaxInFlight,
		present(c.RunPodEndpoint), c.RunPodAPIKey != "",
		present(c.LLMBaseURL), present(c.LLMModelID), c.LLMAPIKey != "",
		c.LLMThinking, c.LLMReasoningEffort,
	)
}

func present(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "set"
}
