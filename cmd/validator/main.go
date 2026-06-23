// Command validator cross-checks the Oracle metrics an OpenTelemetry collector
// emits against ground truth from the receiver's own SQL, and (optionally) against
// what landed in NRDB via NRQL.
//
// Exit codes (one-shot): 0 = clean, 1 = a MISMATCH/INGEST_MISMATCH, 2 = setup error.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/newrelic-forks/otel-metric-validator/internal/compare"
	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/dbprobe"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingest"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingestcheck"
	"github.com/newrelic-forks/otel-metric-validator/internal/report"
)

type options struct {
	envFile     string
	watch       bool
	checkIngest bool
	jsonOut     bool
	metric      string
	failOnly    bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	fs := flag.NewFlagSet("validator", flag.ContinueOnError)
	var opt options
	fs.StringVar(&opt.envFile, "env-file", ".env", "path to a .env file to preload")
	fs.BoolVar(&opt.watch, "watch", false, "loop forever on the watch interval")
	fs.BoolVar(&opt.checkIngest, "check-ingest", false, "also verify NRDB ingest (delta) via NRQL")
	fs.BoolVar(&opt.jsonOut, "json", false, "machine-readable JSON output")
	fs.StringVar(&opt.metric, "metric", "", "only report metrics whose name contains this substring")
	fs.BoolVar(&opt.failOnly, "fail-only", false, "hide OK rows in the tables")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	if err := config.LoadDotenv(opt.envFile); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}
	if opt.checkIngest {
		if err := cfg.RequireNR(); err != nil {
			fmt.Fprintf(os.Stderr, "config error: %v\n", err)
			return 2
		}
	}

	if !opt.watch {
		results, ic, err := runOnce(cfg, opt)
		if err != nil {
			if errors.Is(err, fs2ErrNotExist) {
				fmt.Fprintf(os.Stderr, "ingest file not found: %s\n", cfg.IngestPath)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			return 2
		}
		emit(results, ic, opt)
		if failed(results, ic) {
			return 1
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "watching every %.0fs (ctrl-c to stop)…\n", cfg.WatchInterval)
	for {
		results, ic, err := runOnce(cfg, opt)
		if err != nil {
			if errors.Is(err, fs2ErrNotExist) {
				fmt.Fprintf(os.Stderr, "ingest file not found yet: %s\n", cfg.IngestPath)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
		} else {
			f := failed(results, ic)
			ts := time.Now().Format("2006-01-02 15:04:05")
			dbSum := report.SummaryLine(compare.Summarize(results))
			icSum := ""
			if ic != nil {
				icSum = "  ingest[" + report.SummaryLine(ingestcheck.Summarize(ic)) + "]"
			}
			state := "pass"
			if f {
				state = "FAIL"
			}
			fmt.Printf("[%s] %s  db[%s]%s\n", ts, state, dbSum, icSum)
			if f || opt.jsonOut {
				emit(results, ic, opt)
			}
		}
		time.Sleep(time.Duration(cfg.WatchInterval * float64(time.Second)))
	}
}

// fs2ErrNotExist lets the caller detect a missing ingest file via errors.Is.
var fs2ErrNotExist = fs.ErrNotExist

func runOnce(cfg config.Config, opt options) ([]compare.Result, []ingestcheck.Result, error) {
	emitted, err := ingest.Read(cfg.IngestPath, cfg.IngestFormat)
	if err != nil {
		return nil, nil, err
	}
	probe, err := dbprobe.Probe(cfg)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range probe.Errors {
		fmt.Fprintf(os.Stderr, "[db] %s\n", e)
	}
	fmt.Fprintf(os.Stderr, "[info] read %d series from %s; %d expected from DB\n",
		len(emitted), cfg.IngestPath, len(probe.Expected))
	if len(emitted) == 0 {
		fmt.Fprintf(os.Stderr, "[info] no metrics parsed from the ingest file — check the collector "+
			"is writing OTLP JSON to that path (format=%s, scope must contain oracledbreceiver)\n", cfg.IngestFormat)
	}

	results := compare.Compare(probe.Expected, emitted, cfg)
	results = filterDB(results, opt.metric)

	var ic []ingestcheck.Result
	if opt.checkIngest {
		if cfg.IngestFormat != "otlp-json" {
			fmt.Fprintln(os.Stderr, "[ingest] --check-ingest requires VALIDATOR_INGEST_FORMAT=otlp-json; skipping")
		} else {
			series, err := ingest.ReadOTLPSeries(cfg.IngestPath)
			if err != nil {
				return nil, nil, err
			}
			ic = filterIngest(ingestcheck.Check(series, cfg), opt.metric)
		}
	}
	return results, ic, nil
}

func filterDB(results []compare.Result, sub string) []compare.Result {
	if sub == "" {
		return results
	}
	var out []compare.Result
	for _, r := range results {
		if strings.Contains(r.Metric, sub) {
			out = append(out, r)
		}
	}
	return out
}

func filterIngest(results []ingestcheck.Result, sub string) []ingestcheck.Result {
	if sub == "" {
		return results
	}
	var out []ingestcheck.Result
	for _, r := range results {
		if strings.Contains(r.Metric, sub) {
			out = append(out, r)
		}
	}
	return out
}

func emit(results []compare.Result, ic []ingestcheck.Result, opt options) {
	if opt.jsonOut {
		out := map[string]any{"db_check": rawJSON(report.RenderJSON(results)), "ingest_check": nil}
		if ic != nil {
			out["ingest_check"] = rawJSON(report.RenderIngestJSON(ic))
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Println("== DB check (collector vs database) ==")
	fmt.Println(report.RenderTable(results, !opt.failOnly))
	fmt.Println()
	fmt.Println("summary:", report.SummaryLine(compare.Summarize(results)))
	if ic != nil {
		fmt.Println()
		fmt.Println("== Ingest check (collector vs NRDB via NRQL) ==")
		fmt.Println(report.RenderIngestTable(ic, !opt.failOnly))
		fmt.Println()
		fmt.Println("summary:", report.SummaryLine(ingestcheck.Summarize(ic)))
	}
}

func failed(results []compare.Result, ic []ingestcheck.Result) bool {
	return compare.HasFailures(results) || (ic != nil && ingestcheck.HasFailures(ic))
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }
