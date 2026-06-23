package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadEnvFile(t *testing.T, body string, clear ...string) {
	t.Helper()
	for _, k := range clear {
		os.Unsetenv(k)
	}
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotenv(p); err != nil {
		t.Fatal(err)
	}
}

func TestStripsInlineComment(t *testing.T) {
	loadEnvFile(t, "VALIDATOR_TOLERANCE_GAUGE=0.02      # 2% tolerance\n", "VALIDATOR_TOLERANCE_GAUGE")
	if got := os.Getenv("VALIDATOR_TOLERANCE_GAUGE"); got != "0.02" {
		t.Fatalf("got %q", got)
	}
}

func TestKeepsHashWithoutLeadingSpace(t *testing.T) {
	loadEnvFile(t,
		"ORACLE_MONITORING_PASSWORD=FakePassw0rd##\nORACLE_MONITORING_USER=C##DB_MONITOR\n",
		"ORACLE_MONITORING_PASSWORD", "ORACLE_MONITORING_USER")
	if got := os.Getenv("ORACLE_MONITORING_PASSWORD"); got != "FakePassw0rd##" {
		t.Fatalf("password mangled: %q", got)
	}
	if got := os.Getenv("ORACLE_MONITORING_USER"); got != "C##DB_MONITOR" {
		t.Fatalf("user mangled: %q", got)
	}
}

func TestQuotedValueVerbatim(t *testing.T) {
	loadEnvFile(t, "NEW_RELIC_API_KEY=\"abc # not-a-comment\"\n", "NEW_RELIC_API_KEY")
	if got := os.Getenv("NEW_RELIC_API_KEY"); got != "abc # not-a-comment" {
		t.Fatalf("got %q", got)
	}
}

func TestExistingEnvNotOverwritten(t *testing.T) {
	os.Setenv("ORACLE_HOST", "real-host")
	loadEnvFile(t, "ORACLE_HOST=from-file\n")
	if got := os.Getenv("ORACLE_HOST"); got != "real-host" {
		t.Fatalf("should not overwrite, got %q", got)
	}
	os.Unsetenv("ORACLE_HOST")
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	for _, k := range []string{"VALIDATOR_INGEST_FORMAT", "VALIDATOR_CONTAINER_MODE", "VALIDATOR_TOLERANCE_GAUGE", "ORACLE_PORT"} {
		os.Unsetenv(k)
	}
	os.Setenv("ORACLE_HOST", "h")
	os.Setenv("ORACLE_SERVICE", "s")
	os.Setenv("ORACLE_MONITORING_USER", "u")
	os.Setenv("ORACLE_MONITORING_PASSWORD", "p")
	os.Setenv("VALIDATOR_INGEST_PATH", "x")
	defer func() {
		for _, k := range []string{"ORACLE_HOST", "ORACLE_SERVICE", "ORACLE_MONITORING_USER", "ORACLE_MONITORING_PASSWORD", "VALIDATOR_INGEST_PATH"} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != 1521 || cfg.IngestFormat != "otlp-json" || cfg.ContainerMode != "pdb" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if cfg.TolGauge != 0.02 || cfg.TolCounter != 0.05 {
		t.Fatalf("tolerance defaults wrong: %+v", cfg)
	}
}

func TestRequireNR(t *testing.T) {
	if err := (Config{}).RequireNR(); err == nil {
		t.Fatal("empty NR config should error")
	}
	if err := (Config{NRAPIKey: "k", NRAccountID: "1"}).RequireNR(); err != nil {
		t.Fatalf("complete NR config should pass: %v", err)
	}
}
