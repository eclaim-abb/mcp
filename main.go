package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type config struct {
	addr, apiURL, clientID, token string
	timeout                       time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		addr:     strings.TrimSpace(os.Getenv("ECLAIM_MCP_LISTEN_ADDR")),
		apiURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("ECLAIM_API_BASE_URL")), "/"),
		clientID: strings.TrimSpace(os.Getenv("ECLAIM_API_CLIENT_ID")),
		token:    strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN")),
		timeout:  10 * time.Second,
	}
	if cfg.addr == "" {
		cfg.addr = "127.0.0.1:8091"
	}
	apiURL, err := url.Parse(cfg.apiURL)
	if err != nil || (apiURL.Scheme != "http" && apiURL.Scheme != "https") ||
		apiURL.Hostname() == "" || apiURL.User != nil || apiURL.ForceQuery || apiURL.RawQuery != "" || apiURL.Fragment != "" {
		return config{}, errors.New("ECLAIM_API_BASE_URL must be an HTTP(S) base URL")
	}
	clientID, err := strconv.ParseUint(cfg.clientID, 10, 64)
	if err != nil || clientID == 0 {
		return config{}, errors.New("ECLAIM_API_CLIENT_ID must be a positive eclaim-api client number")
	}
	if len(cfg.token) < 32 || strings.ContainsAny(cfg.token, " \t\r\n") {
		return config{}, errors.New("MCP_AUTH_TOKEN must be a service token of at least 32 characters without whitespace")
	}
	if value := os.Getenv("ECLAIM_API_TIMEOUT"); value != "" {
		cfg.timeout, err = time.ParseDuration(value)
		if err != nil || cfg.timeout <= 0 {
			return config{}, errors.New("ECLAIM_API_TIMEOUT must be a positive Go duration")
		}
	}
	return cfg, nil
}

func newHandler(cfg config) http.Handler {
	checker := claimChecker{
		apiURL:   cfg.apiURL,
		clientID: cfg.clientID,
		timeout:  cfg.timeout,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		attempts: attemptLimiter{entries: make(map[string][]time.Time)},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "eclaim-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_claim_status",
		Description: "Get the current status of a vehicle claim after verifying the last four digits of the registered phone.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, checker.check)
	stream := http.NewCrossOriginProtection().Handler(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 16 << 10},
	))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
	})
	expected := []byte("Bearer " + cfg.token)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		stream.ServeHTTP(w, r)
	})
	return mux
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           newHandler(cfg),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.timeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("eclaim-mcp listening on %s", listener.Addr())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
