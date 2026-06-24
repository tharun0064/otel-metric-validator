// Package ingest reads what the collector emitted for the oracle receiver.
//
// Two input formats:
//   - otlp-json : newline-delimited OTLP/JSON written by a `file` exporter (robust).
//   - debug-log : text produced by the `debug`/`logging` exporter (best-effort).
//
// Both return the latest emitted data point per (metric, attributes), keyed the
// same way metricmap keys its Expected values so the comparator can join them.
package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/newrelic-forks/otel-metric-validator/internal/metricmap"
)

// Emitted is one data point the collector emitted. Attrs are the data-point
// attributes (used for the DB-check join); Resource holds the OTLP resource
// attributes (host.name, oracledb.instance.name, …) used only to scope NRQL.
type Emitted struct {
	Metric       string
	Attrs        map[string]string
	Resource     map[string]string
	Value        float64
	TimeUnixNano int64
}

// Key returns the canonical (metric, attrs) join key. Resource attributes are
// deliberately excluded so emitted points still join to DB-probe expectations.
func (e Emitted) Key() string { return metricmap.Key(e.Metric, e.Attrs) }

// Series captures the first and last emitted points for a (metric, attrs) series.
// For cumulative counters, (LastValue - FirstValue) is the total delta NR should
// have stored over (FirstTS, LastTS].
type Series struct {
	Metric     string
	Attrs      map[string]string
	Resource   map[string]string
	FirstValue float64
	FirstTS    int64
	LastValue  float64
	LastTS     int64
	NPoints    int
}

// Key returns the canonical (metric, attrs) join key.
func (s Series) Key() string { return metricmap.Key(s.Metric, s.Attrs) }

// ---------------------------------------------------------------------------
// OTLP/JSON
// ---------------------------------------------------------------------------

type otlpAttr struct {
	Key   string                     `json:"key"`
	Value map[string]json.RawMessage `json:"value"`
}

type otlpPayload struct {
	ResourceMetrics []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeMetrics []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			Metrics []otlpMetric `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

type otlpMetric struct {
	Name  string   `json:"name"`
	Gauge *otlpAgg `json:"gauge"`
	Sum   *otlpAgg `json:"sum"`
}

type otlpAgg struct {
	DataPoints []otlpDP `json:"dataPoints"`
}

type otlpDP struct {
	Attributes   []otlpAttr `json:"attributes"`
	TimeUnixNano string     `json:"timeUnixNano"`
	AsDouble     *float64   `json:"asDouble"`
	AsInt        *string    `json:"asInt"`
}

func attrValue(v map[string]json.RawMessage) string {
	for _, k := range []string{"stringValue", "intValue", "doubleValue", "boolValue"} {
		if raw, ok := v[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return s
			}
			return strings.Trim(string(raw), `"`)
		}
	}
	return ""
}

func (dp otlpDP) value() (float64, bool) {
	if dp.AsDouble != nil {
		return *dp.AsDouble, true
	}
	if dp.AsInt != nil {
		if n, err := strconv.ParseInt(*dp.AsInt, 10, 64); err == nil {
			return float64(n), true
		}
	}
	return 0, false
}

func iterOTLP(line []byte, fn func(Emitted)) {
	var obj otlpPayload
	if json.Unmarshal(line, &obj) != nil {
		return
	}
	for _, rm := range obj.ResourceMetrics {
		resAttrs := make(map[string]string, len(rm.Resource.Attributes))
		for _, a := range rm.Resource.Attributes {
			resAttrs[a.Key] = attrValue(a.Value)
		}
		for _, sm := range rm.ScopeMetrics {
			if !metricmap.ScopeMatches(sm.Scope.Name) {
				continue
			}
			for _, m := range sm.Metrics {
				if m.Name == "" {
					continue
				}
				body := m.Gauge
				if body == nil {
					body = m.Sum
				}
				if body == nil {
					continue
				}
				for _, dp := range body.DataPoints {
					val, ok := dp.value()
					if !ok {
						continue
					}
					attrs := make(map[string]string, len(dp.Attributes))
					for _, a := range dp.Attributes {
						attrs[a.Key] = attrValue(a.Value)
					}
					ts, _ := strconv.ParseInt(dp.TimeUnixNano, 10, 64)
					fn(Emitted{Metric: m.Name, Attrs: attrs, Resource: resAttrs, Value: val, TimeUnixNano: ts})
				}
			}
		}
	}
}

func scanLines(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			fn([]byte(line))
		}
	}
	return sc.Err()
}

// ReadOTLPJSON returns the latest emitted point per (metric, attrs).
func ReadOTLPJSON(path string) (map[string]Emitted, error) {
	latest := map[string]Emitted{}
	err := scanLines(path, func(line []byte) {
		iterOTLP(line, func(pt Emitted) {
			k := pt.Key()
			if cur, ok := latest[k]; !ok || pt.TimeUnixNano >= cur.TimeUnixNano {
				latest[k] = pt
			}
		})
	})
	return latest, err
}

// ReadOTLPSeries collapses all OTLP-JSON points into per-series first/last
// endpoints. Points older than sinceNanos are ignored (0 = whole file); bounding
// the window makes the counter delta a partial increase rather than ≈ cumulative.
func ReadOTLPSeries(path string, sinceNanos int64) (map[string]Series, error) {
	series := map[string]Series{}
	err := scanLines(path, func(line []byte) {
		iterOTLP(line, func(pt Emitted) {
			if sinceNanos > 0 && pt.TimeUnixNano < sinceNanos {
				return
			}
			k := pt.Key()
			s, ok := series[k]
			if !ok {
				series[k] = Series{
					Metric: pt.Metric, Attrs: pt.Attrs, Resource: pt.Resource,
					FirstValue: pt.Value, FirstTS: pt.TimeUnixNano,
					LastValue: pt.Value, LastTS: pt.TimeUnixNano, NPoints: 1,
				}
				return
			}
			s.NPoints++
			if pt.TimeUnixNano <= s.FirstTS {
				s.FirstValue, s.FirstTS = pt.Value, pt.TimeUnixNano
			}
			if pt.TimeUnixNano >= s.LastTS {
				s.LastValue, s.LastTS = pt.Value, pt.TimeUnixNano
			}
			series[k] = s
		})
	})
	return series, err
}

// ---------------------------------------------------------------------------
// debug / logging exporter (detailed text) — best effort
// ---------------------------------------------------------------------------

var (
	nameRE  = regexp.MustCompile(`->\s*Name:\s*(\S+)`)
	attrRE  = regexp.MustCompile(`->\s*([\w.]+):\s*\w+\(([^)]*)\)`)
	valueRE = regexp.MustCompile(`Value:\s*([-\d.eE+]+)`)
)

// ReadDebugLog parses the detailed text block from the debug/logging exporter.
func ReadDebugLog(path string) (map[string]Emitted, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	latest := map[string]Emitted{}
	var curMetric string
	curAttrs := map[string]string{}
	inAttrs := false

	flush := func(value float64) {
		if curMetric == "" {
			return
		}
		attrs := make(map[string]string, len(curAttrs))
		for k, v := range curAttrs {
			attrs[k] = v
		}
		pt := Emitted{Metric: curMetric, Attrs: attrs, Value: value}
		latest[pt.Key()] = pt
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\n")
		if m := nameRE.FindStringSubmatch(line); m != nil {
			curMetric = m[1]
			curAttrs = map[string]string{}
			inAttrs = false
			continue
		}
		if strings.Contains(line, "Data point attributes:") {
			curAttrs = map[string]string{}
			inAttrs = true
			continue
		}
		if inAttrs {
			if am := attrRE.FindStringSubmatch(line); am != nil {
				curAttrs[am[1]] = am[2]
				continue
			}
			inAttrs = false
		}
		if vm := valueRE.FindStringSubmatch(line); vm != nil {
			if v, err := strconv.ParseFloat(vm[1], 64); err == nil {
				flush(v)
			}
			curAttrs = map[string]string{}
		}
	}
	return latest, sc.Err()
}

// Read dispatches on format.
func Read(path, format string) (map[string]Emitted, error) {
	switch format {
	case "otlp-json":
		return ReadOTLPJSON(path)
	case "debug-log":
		return ReadDebugLog(path)
	}
	return nil, &os.PathError{Op: "read", Path: path, Err: errUnknownFormat(format)}
}

type errUnknownFormat string

func (e errUnknownFormat) Error() string { return "unknown ingest format: " + string(e) }
