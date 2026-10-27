package exporter

import (
	"io"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewHandler(registry *prometheus.Registry, timeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true, Timeout: timeout + time.Second, MaxRequestsInFlight: 2}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><head><title>CARLA Exporter</title></head><body><h1>CARLA Exporter</h1><p><a href=\"/metrics\">Metrics</a></p></body></html>\n")
	})
	return mux
}
