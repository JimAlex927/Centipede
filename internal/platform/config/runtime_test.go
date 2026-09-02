package config

import (
	"strings"
	"testing"
)

func TestRuntimeAppliesValidSnapshotAndRejectsInvalidOrStaticUpdate(t *testing.T) {
	initial, err := buildConfig([]byte(validConfigYAML), "dev")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	runtime.current.Store(&initial)
	var hookCalls int
	runtime.OnChange(func(Config) { hookCalls++ })

	updated := strings.Replace(validConfigYAML, `access_token_ttl: "15m"`, `access_token_ttl: "20m"`, 1)
	runtime.applyContent([]byte(updated))
	if got := runtime.Current().Auth.AccessTokenTTL.String(); got != "20m0s" || hookCalls != 1 {
		t.Fatalf("runtime update ttl=%q hooks=%d", got, hookCalls)
	}

	invalid := strings.Replace(updated, `access_token_ttl: "20m"`, `access_token_ttl: "invalid"`, 1)
	runtime.applyContent([]byte(invalid))
	if got := runtime.Current().Auth.AccessTokenTTL.String(); got != "20m0s" || hookCalls != 1 {
		t.Fatalf("invalid update changed runtime ttl=%q hooks=%d", got, hookCalls)
	}

	static := strings.Replace(updated, `read_timeout: "20s"`, `read_timeout: "30s"`, 1)
	runtime.applyContent([]byte(static))
	if got := runtime.Current().Server.ReadTimeout.String(); got != "20s" || hookCalls != 1 {
		t.Fatalf("static update changed runtime timeout=%q hooks=%d", got, hookCalls)
	}
}
