// Package config loads validator configuration from the environment, optionally
// pre-loading a .env file. Names mirror the Python validator and the receiver's
// own ORACLE_* conventions.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Config holds all validator settings.
type Config struct {
	// Oracle connection
	Host     string
	Port     int
	Service  string
	User     string
	Password string

	// Ingest source
	IngestPath   string
	IngestFormat string // "otlp-json" | "debug-log"

	// Behaviour
	ContainerMode string // "pdb" | "cdb"
	TolGauge      float64
	TolCounter    float64
	AbsEpsilon    float64
	WatchInterval float64

	// New Relic NRQL ingest check (optional; required only with --check-ingest)
	NRAPIKey       string
	NRAccountID    string
	NRNerdGraphURL string
}

// IsCDB reports whether the receiver connects to the CDB root.
func (c Config) IsCDB() bool { return c.ContainerMode == "cdb" }

// RequireNR validates NR settings; call only when the ingest check is requested.
func (c Config) RequireNR() error {
	var missing []string
	if c.NRAPIKey == "" {
		missing = append(missing, "NEW_RELIC_API_KEY")
	}
	if c.NRAccountID == "" {
		missing = append(missing, "NEW_RELIC_ACCOUNT_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("--check-ingest needs: %s  (see .env.example)", strings.Join(missing, ", "))
	}
	return nil
}

// An inline comment starts at the first '#' that follows whitespace (dotenv
// convention). A '#' with no preceding space stays in the value (e.g. passwords
// ending in '##', or a user like 'C##DB_MONITOR').
var inlineComment = regexp.MustCompile(`\s#`)

// LoadDotenv loads KEY=VALUE lines from path into the environment without
// overwriting variables already set. Missing files are ignored.
func LoadDotenv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
			quote := value[0]
			if end := strings.IndexByte(value[1:], quote); end != -1 {
				value = value[1 : 1+end]
			} else {
				value = value[1:]
			}
		} else if loc := inlineComment.FindStringIndex(value); loc != nil {
			value = strings.TrimSpace(value[:loc[0]])
		}
		if key != "" {
			if _, ok := os.LookupEnv(key); !ok {
				os.Setenv(key, value)
			}
		}
	}
	return nil
}

var (
	validFormats = map[string]bool{"otlp-json": true, "debug-log": true}
	validModes   = map[string]bool{"pdb": true, "cdb": true}
)

// Load builds a Config from the environment, failing fast on errors.
func Load() (Config, error) {
	required := []string{"ORACLE_HOST", "ORACLE_SERVICE", "ORACLE_MONITORING_USER", "ORACLE_MONITORING_PASSWORD"}
	var missing []string
	for _, k := range required {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s  (see .env.example)", strings.Join(missing, ", "))
	}

	fmtv := strings.ToLower(strings.TrimSpace(envOr("VALIDATOR_INGEST_FORMAT", "otlp-json")))
	if !validFormats[fmtv] {
		return Config{}, fmt.Errorf("VALIDATOR_INGEST_FORMAT must be otlp-json or debug-log, got %q", fmtv)
	}
	mode := strings.ToLower(strings.TrimSpace(envOr("VALIDATOR_CONTAINER_MODE", "pdb")))
	if !validModes[mode] {
		return Config{}, fmt.Errorf("VALIDATOR_CONTAINER_MODE must be pdb or cdb, got %q", mode)
	}
	ingestPath := strings.TrimSpace(os.Getenv("VALIDATOR_INGEST_PATH"))
	if ingestPath == "" {
		return Config{}, fmt.Errorf("VALIDATOR_INGEST_PATH is required (path to the collector's metric output)")
	}

	port, err := intOr("ORACLE_PORT", 1521)
	if err != nil {
		return Config{}, err
	}
	tolGauge, err := floatOr("VALIDATOR_TOLERANCE_GAUGE", 0.02)
	if err != nil {
		return Config{}, err
	}
	tolCounter, err := floatOr("VALIDATOR_TOLERANCE_COUNTER", 0.05)
	if err != nil {
		return Config{}, err
	}
	absEps, err := floatOr("VALIDATOR_ABS_EPSILON", 1.0)
	if err != nil {
		return Config{}, err
	}
	watch, err := floatOr("VALIDATOR_WATCH_INTERVAL", 30.0)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Host:           strings.TrimSpace(os.Getenv("ORACLE_HOST")),
		Port:           port,
		Service:        strings.TrimSpace(os.Getenv("ORACLE_SERVICE")),
		User:           strings.TrimSpace(os.Getenv("ORACLE_MONITORING_USER")),
		Password:       os.Getenv("ORACLE_MONITORING_PASSWORD"),
		IngestPath:     ingestPath,
		IngestFormat:   fmtv,
		ContainerMode:  mode,
		TolGauge:       tolGauge,
		TolCounter:     tolCounter,
		AbsEpsilon:     absEps,
		WatchInterval:  watch,
		NRAPIKey:       strings.TrimSpace(os.Getenv("NEW_RELIC_API_KEY")),
		NRAccountID:    strings.TrimSpace(os.Getenv("NEW_RELIC_ACCOUNT_ID")),
		NRNerdGraphURL: strings.TrimSpace(envOr("NEW_RELIC_NERDGRAPH_URL", "https://api.newrelic.com/graphql")),
	}, nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func floatOr(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", key, v)
	}
	return f, nil
}

func intOr(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, v)
	}
	return n, nil
}
