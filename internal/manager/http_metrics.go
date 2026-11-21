package manager

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

const metricsDirectory = "/run/pax-metrics"

const (
	webSocketControl = "control"
	webSocketAgent   = "agent"
	webSocketClient  = "client"
)

type httpMetrics struct {
	registry   *prometheus.Registry
	requests   *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	webSockets *prometheus.GaugeVec
}

func newHTTPMetrics() *httpMetrics {
	labels := []string{"method", "route", "status"}
	m := &httpMetrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pax_http_requests_total",
			Help: "Number of completed Manager HTTP handlers.",
		}, labels),
		webSockets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pax_websocket_connections",
			Help: "Currently open Manager WebSocket connections.",
		}, []string{"kind"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pax_http_request_duration_seconds",
			Help:    "Time spent in Manager HTTP handlers, excluding successful SSE and WebSocket requests.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, labels),
	}
	m.registry.MustRegister(
		m.requests,
		m.duration,
		m.webSockets,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	for _, kind := range []string{webSocketControl, webSocketAgent, webSocketClient} {
		m.webSockets.WithLabelValues(kind).Set(0)
	}
	return m
}

func (s *Service) trackWebSocket(kind string) func() {
	if s.httpMetrics == nil {
		return func() {}
	}
	active := s.httpMetrics.webSockets.WithLabelValues(kind)
	active.Inc()
	return active.Dec
}

func (m *httpMetrics) observe(c context.Context, ctx *app.RequestContext) {
	start := time.Now()
	ctx.Next(c)

	route := ctx.FullPath()
	if route == "" {
		route = "unmatched"
	}
	method := string(ctx.Method())
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
	default:
		method = "OTHER"
	}
	status := ctx.Response.StatusCode()
	// Gorilla writes the handshake directly to the hijacked connection.
	if status == http.StatusOK && ctx.Response.GetHijackWriter() != nil &&
		strings.EqualFold(string(ctx.GetHeader("Upgrade")), "websocket") {
		status = http.StatusSwitchingProtocols
	}
	statusLabel := strconv.Itoa(status)
	m.requests.WithLabelValues(method, route, statusLabel).Inc()
	if status != http.StatusSwitchingProtocols && !strings.HasPrefix(
		string(ctx.Response.Header.ContentType()), "text/event-stream",
	) {
		m.duration.WithLabelValues(method, route, statusLabel).Observe(time.Since(start).Seconds())
	}
}

func (m *httpMetrics) handler(
	allowedPeer netip.Addr,
	token string,
	enabled func() bool,
) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	scrape := promhttp.HandlerFor(
		m.registry,
		promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 5 * time.Second},
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !enabled() {
			http.NotFound(w, r)
			return
		}
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || peer.Addr().Unmap() != allowedPeer {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		scrape.ServeHTTP(w, r)
	})
	return mux
}

func (s *Service) startMetrics(ctx context.Context) func() {
	stop, err := s.listenMetrics(ctx, ":9109", metricsDirectory)
	if err != nil {
		logging.Warn(ctx, "metrics unavailable", slog.String("error", err.Error()))
		return func() {}
	}
	return stop
}

func (s *Service) metricsEnabled() bool {
	return s.runtimeConfig.Snapshot().
		FeatureStage(runtimeconfig.FeatureMetrics) ==
		runtimeconfig.StageGA
}

func (s *Service) listenMetrics(ctx context.Context, addr, directory string) (func(), error) {
	rawPeer, err := os.ReadFile(filepath.Join(directory, "scrape-peer"))
	if errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metrics scraper address: %w", err)
	}
	peer, err := netip.ParseAddr(strings.TrimSpace(string(rawPeer)))
	if err != nil || peer.IsUnspecified() || peer.IsMulticast() || peer.Zone() != "" {
		return nil, errors.New("metrics scrape-peer must specify a single unicast address")
	}
	tokenBytes, err := os.ReadFile(filepath.Join(directory, "scrape-token"))
	if err != nil {
		return nil, fmt.Errorf("read metrics token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("metrics token must be non-empty and contain no whitespace")
	}
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(directory, "server.pem"), filepath.Join(directory, "server.key"),
	)
	if err != nil {
		return nil, fmt.Errorf("load metrics TLS certificate: %w", err)
	}

	var listenerConfig net.ListenConfig
	listener, err := listenerConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for metrics: %w", err)
	}
	s.httpMetrics = newHTTPMetrics()
	server := &http.Server{
		Handler:           s.httpMetrics.handler(peer.Unmap(), token, s.metricsEnabled),
		ReadHeaderTimeout: 3 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    8 << 10,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}
	go func() {
		if err := server.ServeTLS(listener, "", ""); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			logging.Warn(ctx, "metrics server stopped", slog.String("error", err.Error()))
		}
	}()
	return func() {
		_ = server.Close()
	}, nil
}
