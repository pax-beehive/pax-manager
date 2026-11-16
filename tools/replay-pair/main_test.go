package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGivenUnixControlWhenJSONPostedThenResponseAndBodyPassThrough(t *testing.T) {
	dir, err := os.MkdirTemp("", "pair-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "paxd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/v1/remotes" || string(body) != `{"name":"fixture"}` || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s %s", r.Method, r.URL, body)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"ok":true}`)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	bodyFile := filepath.Join(dir, "request.json")
	if err := os.WriteFile(bodyFile, []byte(`{"name":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"control", "--socket", socket, "POST", "/v1/remotes", bodyFile}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"ok":true}` {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGivenHTTPControlWhenServerFailsThenBodyRemainsReviewable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method: %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"drop":true}` {
			t.Errorf("body: %s", body)
		}
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"busy"}`)
	}))
	defer server.Close()
	var out bytes.Buffer
	err := run(context.Background(), []string{"http", "POST", server.URL, "-"}, strings.NewReader(`{"drop":true}`), &out)
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(out.String(), "busy") {
		t.Fatalf("error=%v output=%s", err, out.String())
	}
}

func TestGivenMockACPWhenNewIsGatedThenCatalogPrecedesReplyAndPromptRunsOnce(t *testing.T) {
	dir := t.TempDir()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"mock-acp", "--fixture-dir", dir}, inReader, outWriter)
		outWriter.Close()
	}()
	responses := make(chan rpcMessage)
	go func() {
		defer close(responses)
		scanner := bufio.NewScanner(outReader)
		for scanner.Scan() {
			var message rpcMessage
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				return
			}
			responses <- message
		}
	}()
	write := func(line string) {
		t.Helper()
		if _, err := fmt.Fprintln(inWriter, line); err != nil {
			t.Fatal(err)
		}
	}
	read := func() rpcMessage {
		t.Helper()
		select {
		case message := <-responses:
			return message
		case <-time.After(3 * time.Second):
			t.Fatal("worker response timed out")
			return rpcMessage{}
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	init := read()
	if string(init.ID) != "1" || !bytes.Contains(init.Result, []byte(`"protocolVersion":1`)) {
		t.Fatalf("initialize: %+v", init)
	}
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	write(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`)
	catalog := read()
	if catalog.Method != "session/update" || !bytes.Contains(catalog.Params, []byte("available_commands_update")) {
		t.Fatalf("early notification: %+v", catalog)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "new-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing new-started marker")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case message := <-responses:
		t.Fatalf("reply before gate released: %+v", message)
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "release-new"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	created := read()
	if string(created.ID) != "2" || !bytes.Contains(created.Result, []byte("fixture-native-1")) {
		t.Fatalf("new result: %+v", created)
	}
	write(`{"jsonrpc":"2.0","id":"prompt-1","method":"session/prompt","params":{"sessionId":"fixture-native-1","prompt":[{"type":"text","text":"hello"}]}}`)
	chunk, complete := read(), read()
	if !bytes.Contains(chunk.Params, []byte("fixture reply")) || !bytes.Contains(complete.Result, []byte(`"stopReason":"end_turn"`)) {
		t.Fatalf("prompt responses: %+v %+v", chunk, complete)
	}
	write(`{"jsonrpc":"2.0","method":"session/cancel","params":{}}`)
	write(`{"jsonrpc":"2.0","id":4,"method":"missing"}`)
	if !bytes.Contains(read().Error, []byte("-32601")) {
		t.Fatal("missing method error")
	}
	inWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range bytes.Split(bytes.TrimSpace(events), []byte("\n")) {
		var event workerEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "request" {
			counts[event.Method]++
		}
	}
	if counts["session/new"] != 1 || counts["session/prompt"] != 1 {
		t.Fatalf("unexpected requests: %v", counts)
	}
}

func TestGivenProxyWhenWebsocketDroppedThenOnlyHijackedConnectionsClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" {
			io.WriteString(w, "upstream:"+r.URL.RequestURI())
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		io.Copy(io.Discard, conn)
	}))
	defer upstream.Close()
	handler, tracker, err := newProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ConnState: tracker.connState}
	go server.Serve(&trackingListener{Listener: listener, tracker: tracker})
	t.Cleanup(func() { tracker.drop(); server.Close() })
	base := "http://" + listener.Addr().String()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	fmt.Fprint(client, "GET /socket HTTP/1.1\r\nHost: fixture\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("websocket handshake: %v %+v", err, response)
	}
	get := func(path string) []byte {
		t.Helper()
		response, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var before proxyStatus
	if err := json.Unmarshal(get("/_fixture/status"), &before); err != nil {
		t.Fatal(err)
	}
	if before.ActiveWebsockets != 1 || before.TotalWSConnections != 1 {
		t.Fatalf("before: %+v", before)
	}
	if string(get("/ordinary?q=value")) != "upstream:/ordinary?q=value" {
		t.Fatal("ordinary HTTP forwarding failed")
	}
	drop, err := http.Post(base+"/_fixture/drop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(drop.Body)
	drop.Body.Close()
	if !bytes.Contains(body, []byte(`"dropped":1`)) {
		t.Fatalf("drop body: %s", body)
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("websocket remained open")
	}
	var after proxyStatus
	if err := json.Unmarshal(get("/_fixture/status"), &after); err != nil {
		t.Fatal(err)
	}
	if after.ActiveWebsockets != 0 || after.Drops != 1 {
		t.Fatalf("after: %+v", after)
	}
	if string(get("/still-alive")) != "upstream:/still-alive" {
		t.Fatal("proxy stopped serving")
	}
}

func TestGivenInvalidCLIWhenInvokedThenErrorsAreExplicit(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"control"}, {"control", "GET", "relative"}, {"http", "GET", "unix:///x"}, {"mock-acp", "--missing"}, {"proxy"}, {"proxy", "--upstream", ":bad"}} {
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}
