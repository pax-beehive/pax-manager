package manager

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestMobileControlHTTPUsesAuthenticatedNodeTunnelWithoutTruncatingScreenshot(t *testing.T) {
	const owner = "mobile-owner@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	t.Cleanup(closeServer)
	t.Cleanup(func() { _ = ws.Close() })
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/daemon/mobile"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
		`{"operation":"screenshot","payload":{"sessionId":"device-explicit"}}`,
	))
	setJSON(req)
	req.Header.Set("X-User-Email", owner)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		response <- rec
	}()

	require.NoError(t, ws.SetReadDeadline(time.Now().Add(10*time.Second)))
	messageType, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var frame struct {
		Kind      string `json:"kind"`
		RequestID string `json:"request_id"`
		Query     struct {
			Type          string `json:"type"`
			MobileControl struct {
				Operation string          `json:"operation"`
				Payload   json.RawMessage `json:"payload"`
			} `json:"mobile_control"`
		} `json:"query"`
	}
	require.NoError(t, json.Unmarshal(payload, &frame))
	require.Equal(t, "query", frame.Kind)
	require.NotEmpty(t, frame.RequestID)
	require.Equal(t, "mobile.control", frame.Query.Type)
	require.Equal(t, "screenshot", frame.Query.MobileControl.Operation)
	require.JSONEq(t, `{"sessionId":"device-explicit"}`, string(frame.Query.MobileControl.Payload))

	image := strings.Repeat("A", 1200000)
	result, err := json.Marshal(map[string]any{
		"kind": "response", "request_id": frame.RequestID,
		"query_result": map[string]any{
			"type": "mobile.control", "mobile_control": map[string]any{
				"mime": "image/png", "data": image, "width": 720, "height": 1280,
				"frameId": "single-use-frame",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, result))

	select {
	case rec := <-response:
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Data struct {
				MobileControl struct {
					Data    string `json:"data"`
					FrameID string `json:"frameId"`
				} `json:"mobile_control"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "single-use-frame", body.Data.MobileControl.FrameID)
		require.Len(t, body.Data.MobileControl.Data, len(image))
		require.Equal(t, sha256.Sum256([]byte(image)),
			sha256.Sum256([]byte(body.Data.MobileControl.Data)))
	case <-time.After(10 * time.Second):
		t.Fatal("mobile HTTP response did not receive the node tunnel result")
	}
}

func TestMobileControlHTTPRejectsNonOwnerAndLiveSignaling(t *testing.T) {
	srv, registered := testNodeControlServer(t, "mobile-owner@example.com")
	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/daemon/mobile"
	for _, candidate := range []struct {
		email string
		body  string
	}{
		{"other-owner@example.com", `{"operation":"state","payload":{}}`},
		{"mobile-owner@example.com", `{"operation":"live","payload":{"offer":"forbidden"}}`},
		{"mobile-owner@example.com", `{"operation":"state","payload":{},"url":"http://host"}`},
		{"mobile-owner@example.com", `{"operation":"state","payload":{}} {}`},
		{"mobile-owner@example.com", `{"operation":"state","payload":{}}` + strings.Repeat(" ", 32768)},
	} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(candidate.body))
		setJSON(req)
		req.Header.Set("X-User-Email", candidate.email)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.GreaterOrEqual(t, rec.Code, http.StatusBadRequest)
		require.Less(t, rec.Code, http.StatusInternalServerError)
	}
}

func TestMobileControlHTTPRejectsSimpleCrossSiteContentTypes(t *testing.T) {
	srv, registered := testNodeControlServer(t, "mobile-owner@example.com")
	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/daemon/mobile"
	for _, contentType := range []string{
		"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
			`{"operation":"destroy","payload":{"sessionId":"device-explicit"}}`,
		))
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-User-Email", "mobile-owner@example.com")
		req.Header.Set("Origin", "https://attacker.example")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	}
}
