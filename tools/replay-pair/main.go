// pair-fixture is an isolated integration-test tool. It has no product dependencies.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "pair-fixture:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pair-fixture control|http|mock-acp|proxy [arguments]")
	}
	switch args[0] {
	case "control":
		return runHTTP(ctx, args[1:], true, in, out)
	case "http":
		return runHTTP(ctx, args[1:], false, in, out)
	case "mock-acp":
		return runMockACP(ctx, args[1:], in, out)
	case "proxy":
		return runProxy(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func commandFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func runHTTP(ctx context.Context, args []string, unix bool, in io.Reader, out io.Writer) error {
	flags := commandFlags("http")
	socket := flags.String("socket", "/root/.paxd/paxd.sock", "paxd Unix socket (control only)")
	timeout := flags.Duration("timeout", 60*time.Second, "request timeout")
	commandID := flags.String("command-id", "", "optional X-Pax-Command-Id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	pos := flags.Args()
	if len(pos) < 2 || len(pos) > 3 {
		return errors.New("usage: control [flags] METHOD PATH [BODYFILE] | http [flags] METHOD URL [BODYFILE]")
	}
	address := pos[1]
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	defer transport.CloseIdleConnections()
	if unix {
		if !strings.HasPrefix(address, "/") {
			return errors.New("control request path must start with /")
		}
		address = "http://paxd" + address
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
		}
	} else if _, err := httpTarget(address); err != nil {
		return err
	}
	var body io.Reader
	if len(pos) == 3 {
		if pos[2] == "-" {
			body = in
		} else {
			file, err := os.Open(pos[2])
			if err != nil {
				return err
			}
			defer file.Close()
			body = file
		}
	}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(pos[0]), address, body)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if *commandID != "" {
		request.Header.Set("X-Pax-Command-Id", *commandID)
	}
	response, err := (&http.Client{Transport: transport, Timeout: *timeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if _, err := io.Copy(out, response.Body); err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", response.Status)
	}
	return nil
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type workerEvent struct {
	Kind   string          `json:"kind"`
	Method string          `json:"method,omitempty"`
	ID     json.RawMessage `json:"id,omitempty"`
	PID    int             `json:"pid"`
	Time   string          `json:"time"`
}

func runMockACP(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	flags := commandFlags("mock-acp")
	dir := flags.String("fixture-dir", "/fixture", "directory containing gate and event files")
	nativeID := flags.String("native-id", "fixture-native-1", "worker session ID")
	text := flags.String("text", "fixture reply", "prompt response text")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: mock-acp [--fixture-dir PATH] [--native-id ID] [--text TEXT]")
	}
	if err := os.MkdirAll(*dir, 0755); err != nil {
		return err
	}
	events, err := os.OpenFile(filepath.Join(*dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer events.Close()
	journal := json.NewEncoder(events)
	writer := json.NewEncoder(out)
	record := func(kind string, request rpcMessage) error {
		return journal.Encode(workerEvent{Kind: kind, Method: request.Method, ID: request.ID, PID: os.Getpid(), Time: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	reply := func(request rpcMessage, result any) error {
		return writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	update := func(sessionID string, update any) error {
		return writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode ACP input: %w", err)
		}
		if err := record("request", request); err != nil {
			return err
		}
		switch request.Method {
		case "initialize":
			err = reply(request, map[string]any{
				"protocolVersion": 1,
				"agentInfo":       map[string]any{"name": "pair-fixture", "version": "1.0.0"},
				"agentCapabilities": map[string]any{
					"loadSession":         false,
					"promptCapabilities":  map[string]any{"image": false, "audio": false, "embeddedContext": false},
					"mcpCapabilities":     map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{},
				},
				"authMethods": []any{},
			})
		case "session/new":
			err = update(*nativeID, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{map[string]any{"name": "inspect", "description": "Inspect fixture state"}}})
			if err == nil {
				err = os.WriteFile(filepath.Join(*dir, "new-started"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)
			}
			if err == nil {
				err = waitForGate(ctx, filepath.Join(*dir, "release-new"))
			}
			if err == nil {
				err = reply(request, map[string]any{"sessionId": *nativeID})
			}
			if err == nil {
				err = record("new_result", request)
			}
		case "session/prompt":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if decodeErr := json.Unmarshal(request.Params, &params); decodeErr != nil {
				return decodeErr
			}
			if params.SessionID == "" {
				params.SessionID = *nativeID
			}
			err = update(params.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": *text}})
			if err == nil {
				err = reply(request, map[string]any{"stopReason": "end_turn"})
			}
			if err == nil {
				err = record("prompt_result", request)
			}
		default:
			if len(request.ID) > 0 && string(request.ID) != "null" {
				err = writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			}
		}
		if err != nil {
			return err
		}
	}
	return scanner.Err()
}

func waitForGate(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type proxyStatus struct {
	ActiveWebsockets   int    `json:"active_websockets"`
	TotalWSConnections uint64 `json:"total_ws_connections"`
	Drops              uint64 `json:"drops"`
}

type connectionTracker struct {
	mu    sync.Mutex
	conns map[*trackedConn]bool
	total uint64
	drops uint64
}

type trackedConn struct {
	net.Conn
	tracker *connectionTracker
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.tracker.mu.Lock()
	delete(c.tracker.conns, c)
	c.tracker.mu.Unlock()
	return err
}

type trackingListener struct {
	net.Listener
	tracker *connectionTracker
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, tracker: l.tracker}
	l.tracker.mu.Lock()
	l.tracker.conns[tracked] = false
	l.tracker.mu.Unlock()
	return tracked, nil
}

func (t *connectionTracker) connState(conn net.Conn, state http.ConnState) {
	tracked, ok := conn.(*trackedConn)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if state == http.StateHijacked {
		if hijacked, exists := t.conns[tracked]; exists && !hijacked {
			t.conns[tracked] = true
			t.total++
		}
	} else if state == http.StateClosed {
		delete(t.conns, tracked)
	}
}

func (t *connectionTracker) status() proxyStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := proxyStatus{TotalWSConnections: t.total, Drops: t.drops}
	for _, hijacked := range t.conns {
		if hijacked {
			status.ActiveWebsockets++
		}
	}
	return status
}

func (t *connectionTracker) drop() int {
	t.mu.Lock()
	var conns []*trackedConn
	for conn, hijacked := range t.conns {
		if hijacked {
			conns = append(conns, conn)
		}
	}
	t.drops++
	t.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
	return len(conns)
}

func httpTarget(address string) (*url.URL, error) {
	target, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, errors.New("URL must have an http(s) scheme and host")
	}
	return target, nil
}

func newProxy(upstream string) (http.Handler, *connectionTracker, error) {
	target, err := httpTarget(upstream)
	if err != nil {
		return nil, nil, err
	}
	tracker := &connectionTracker{conns: make(map[*trackedConn]bool)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "fixture upstream unavailable", http.StatusBadGateway)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_fixture/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tracker.status())
	})
	mux.HandleFunc("POST /_fixture/drop", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"dropped": tracker.drop()})
	})
	mux.Handle("/", proxy)
	return mux, tracker, nil
}

func runProxy(ctx context.Context, args []string) error {
	flags := commandFlags("proxy")
	listen := flags.String("listen", ":9880", "proxy listener address")
	upstream := flags.String("upstream", "", "Manager http(s) URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: proxy --upstream URL [--listen :9880]")
	}
	handler, tracker, err := newProxy(*upstream)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ConnState: tracker.connState, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-done:
		}
	}()
	defer tracker.drop()
	err = server.Serve(&trackingListener{Listener: listener, tracker: tracker})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
