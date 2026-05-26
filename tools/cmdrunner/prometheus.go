package cmdrunner

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PrometheusText 直接輸出 Prometheus exposition format。
// namespace 建議像 "cmdrunner" 或 "myservice"。
// labels 請盡量只放固定值，例如 service, env, instance，避免高 cardinality。
func (s *Supervisor) PrometheusText(namespace string, labels map[string]string) string {
	var b strings.Builder
	_ = s.WritePrometheus(&b, namespace, labels)
	return b.String()
}

// WritePrometheus 將 metrics 寫到 io.Writer。
func (s *Supervisor) WritePrometheus(w io.Writer, namespace string, labels map[string]string) error {
	if s == nil {
		return nil
	}

	ns := sanitizeMetricNamespace(namespace)
	if ns == "" {
		ns = "cmdrunner"
	}

	m := s.SnapshotMetrics()

	baseLabels := cloneLabels(labels)

	writeMetricHelpType(w, ns+"_supervisor_running", "Whether the supervisor currently has a running process", "gauge")
	writeMetricValue(w, ns+"_supervisor_running", baseLabels, boolToFloat(m.Running))

	writeMetricHelpType(w, ns+"_supervisor_starts_total", "Total number of process starts", "counter")
	writeMetricValue(w, ns+"_supervisor_starts_total", baseLabels, float64(m.Starts))

	writeMetricHelpType(w, ns+"_supervisor_stops_total", "Total number of process stops", "counter")
	writeMetricValue(w, ns+"_supervisor_stops_total", baseLabels, float64(m.Stops))

	writeMetricHelpType(w, ns+"_supervisor_restarts_total", "Total number of process restarts", "counter")
	writeMetricValue(w, ns+"_supervisor_restarts_total", baseLabels, float64(m.Restarts))

	writeMetricHelpType(w, ns+"_supervisor_crashes_total", "Total number of process crashes", "counter")
	writeMetricValue(w, ns+"_supervisor_crashes_total", baseLabels, float64(m.Crashes))

	writeMetricHelpType(w, ns+"_supervisor_health_checks_total", "Total number of health checks", "counter")
	writeMetricValue(w, ns+"_supervisor_health_checks_total", baseLabels, float64(m.HealthChecks))

	writeMetricHelpType(w, ns+"_supervisor_health_check_successes_total", "Total number of successful health checks", "counter")
	writeMetricValue(w, ns+"_supervisor_health_check_successes_total", baseLabels, float64(m.HealthCheckSuccesses))

	writeMetricHelpType(w, ns+"_supervisor_health_check_failures_total", "Total number of failed health checks", "counter")
	writeMetricValue(w, ns+"_supervisor_health_check_failures_total", baseLabels, float64(m.HealthCheckFailures))

	writeMetricHelpType(w, ns+"_supervisor_consecutive_restarts", "Current consecutive restart count", "gauge")
	writeMetricValue(w, ns+"_supervisor_consecutive_restarts", baseLabels, float64(m.ConsecutiveRestarts))

	writeMetricHelpType(w, ns+"_supervisor_consecutive_health_check_failures", "Current consecutive health check failure count", "gauge")
	writeMetricValue(w, ns+"_supervisor_consecutive_health_check_failures", baseLabels, float64(m.ConsecutiveHCFailures))

	writeMetricHelpType(w, ns+"_supervisor_pid", "Current process PID, 0 if not running", "gauge")
	writeMetricValue(w, ns+"_supervisor_pid", baseLabels, float64(m.CurrentPID))

	writeMetricHelpType(w, ns+"_supervisor_current_uptime_seconds", "Current uptime in seconds", "gauge")
	writeMetricValue(w, ns+"_supervisor_current_uptime_seconds", baseLabels, m.CurrentUptime.Seconds())

	writeMetricHelpType(w, ns+"_supervisor_total_uptime_seconds", "Accumulated uptime in seconds", "counter")
	writeMetricValue(w, ns+"_supervisor_total_uptime_seconds", baseLabels, m.TotalUptime.Seconds())

	writeMetricHelpType(w, ns+"_supervisor_last_exit_code", "Last process exit code", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_exit_code", baseLabels, float64(m.LastExitCode))

	writeMetricHelpType(w, ns+"_supervisor_last_start_time_seconds", "Unix timestamp of the last process start time", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_start_time_seconds", baseLabels, timeToUnixFloat(m.LastStartTime))

	writeMetricHelpType(w, ns+"_supervisor_last_stop_time_seconds", "Unix timestamp of the last process stop time", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_stop_time_seconds", baseLabels, timeToUnixFloat(m.LastStopTime))

	// 低 cardinality 狀態型 metrics
	writeMetricHelpType(w, ns+"_supervisor_last_error", "Whether the last process ended with an error", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_error", baseLabels, boolToFloat(m.LastError != ""))

	writeMetricHelpType(w, ns+"_supervisor_last_health_error", "Whether the last health check status is unhealthy", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_health_error", baseLabels, boolToFloat(m.LastHealthError != ""))

	// 用 info 風格 gauge 呈現 restart reason
	infoLabels := cloneLabels(baseLabels)
	infoLabels["reason"] = emptyToNA(m.LastRestartReason)
	writeMetricHelpType(w, ns+"_supervisor_last_restart_reason_info", "Last restart reason encoded as an info-style gauge", "gauge")
	writeMetricValue(w, ns+"_supervisor_last_restart_reason_info", infoLabels, 1)

	return nil
}

// PrometheusHandler 可直接掛到 http mux。
func (s *Supervisor) PrometheusHandler(namespace string, labels map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = s.WritePrometheus(w, namespace, labels)
	}
}

func writeMetricHelpType(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, escapeHelp(help))
	fmt.Fprintf(w, "# TYPE %s %s\n", name, typ)
}

func writeMetricValue(w io.Writer, name string, labels map[string]string, value float64) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %v\n", name, value)
		return
	}
	fmt.Fprintf(w, "%s{%s} %v\n", name, formatLabels(labels), value)
}

func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, sanitizeLabelName(k), escapeLabelValue(labels[k])))
	}
	return strings.Join(parts, ",")
}

func sanitizeMetricNamespace(ns string) string {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return ""
	}

	var b strings.Builder
	for i, r := range ns {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == ':'
		if !ok {
			r = '_'
		}
		if i == 0 {
			startOK := (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				r == '_' || r == ':'
			if !startOK {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeLabelName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "label"
	}

	var b strings.Builder
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_'
		if !ok {
			r = '_'
		}
		if i == 0 {
			startOK := (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				r == '_'
			if !startOK {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func timeToUnixFloat(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())
}

func emptyToNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "n/a"
	}
	return s
}
