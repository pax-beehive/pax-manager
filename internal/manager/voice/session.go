package voice

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultMaxDuration = 5 * time.Minute

	startTimeout       = 10 * time.Second
	finishTimeout      = 15 * time.Second
	clientWriteTimeout = 10 * time.Second
)

const (
	ErrorCodeBadRequest          = "bad_request"
	ErrorCodeProviderUnavailable = "provider_unavailable"
	ErrorCodeProviderFailed      = "provider_failed"
	ErrorCodeTimeout             = "timeout"
)

var errSessionTimeout = errors.New("voice session timed out")

// ClientConn is the browser side of a session. *websocket.Conn satisfies it.
type ClientConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	Close() error
}

type Limits struct {
	// MaxDuration bounds the audio of one session. Audio past the limit stops
	// the session as if the client had sent stop.
	MaxDuration time.Duration
}

type clientMessage struct {
	Type     string   `json:"type"`
	Keywords []string `json:"keywords"`
	Context  []string `json:"context"`
}

type readyMessage struct {
	Type       string `json:"type"`
	Provider   string `json:"provider"`
	SampleRate int    `json:"sample_rate"`
	Encoding   string `json:"encoding"`
}

// transcriptMessage always carries the whole transcript: clients replace
// their rendered text instead of appending deltas.
type transcriptMessage struct {
	Type    string `json:"type"`
	Final   string `json:"final"`
	Partial string `json:"partial"`
}

type errorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Serve runs one dictation session over conn.
//
// Client to server: a JSON start message, binary PCM frames in Encoding at
// the provider sample rate, then a JSON stop (or cancel) message.
// Server to client: ready, transcript snapshots, then done or error, after
// which the server closes the connection.
//
// Serve returns provider failures and timeouts; a client that cancels or
// disconnects is not an error.
func Serve(
	ctx context.Context,
	conn ClientConn,
	provider Provider,
	limits Limits,
	userRef string,
) error {
	start, err := readStart(conn)
	if err != nil {
		writeClientError(conn, ErrorCodeBadRequest, err.Error())
		return nil
	}
	maxDuration := limits.MaxDuration
	if maxDuration <= 0 {
		maxDuration = DefaultMaxDuration
	}
	sessionCtx, cancel := context.WithTimeout(ctx, maxDuration+finishTimeout)
	defer cancel()
	stream, err := provider.Open(sessionCtx, Options{
		Keywords: start.Keywords,
		Context:  start.Context,
		UserRef:  userRef,
	})
	if err != nil {
		writeClientError(conn, ErrorCodeProviderUnavailable, "speech recognition is unavailable")
		return err
	}
	defer func() { _ = stream.Close() }()
	if err := writeClientJSON(conn, readyMessage{
		Type:       "ready",
		Provider:   provider.Name(),
		SampleRate: provider.SampleRate(),
		Encoding:   Encoding,
	}); err != nil {
		return nil
	}
	s := &session{
		conn:     conn,
		stream:   stream,
		maxBytes: int(int64(maxDuration) * int64(provider.SampleRate()) * 2 / int64(time.Second)),
	}
	forwarded := make(chan error, 1)
	go func() { forwarded <- s.forward() }()
	stopWatch := context.AfterFunc(sessionCtx, func() {
		if errors.Is(sessionCtx.Err(), context.DeadlineExceeded) {
			s.timedOut.Store(true)
		} else {
			s.cancelled.Store(true)
		}
		_ = stream.Close()
	})
	defer stopWatch()
	if s.pump() == pumpClientLeft {
		select {
		case err := <-forwarded:
			return err
		default:
		}
		s.cancelled.Store(true)
	}
	_ = stream.Close()
	return <-forwarded
}

func readStart(conn ClientConn) (clientMessage, error) {
	_ = conn.SetReadDeadline(time.Now().Add(startTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		return clientMessage{}, errors.New("expected a start message")
	}
	var message clientMessage
	if messageType != websocket.TextMessage || json.Unmarshal(data, &message) != nil ||
		message.Type != "start" {
		return clientMessage{}, errors.New("the first message must be a JSON start message")
	}
	return message, nil
}

type session struct {
	conn       ClientConn
	stream     Stream
	maxBytes   int
	audioBytes int
	finished   bool
	finishTime *time.Timer
	timedOut   atomic.Bool
	cancelled  atomic.Bool
}

type pumpResult int

const (
	// pumpClientLeft means the client cancelled, disconnected, or forward
	// closed the connection after the final result.
	pumpClientLeft pumpResult = iota
	// pumpStreamFailed means the provider stopped accepting audio; forward
	// still reports the failure to the client.
	pumpStreamFailed
)

func (s *session) pump() pumpResult {
	defer func() {
		if s.finishTime != nil {
			s.finishTime.Stop()
		}
	}()
	for {
		messageType, data, err := s.conn.ReadMessage()
		if err != nil {
			return pumpClientLeft
		}
		switch messageType {
		case websocket.BinaryMessage:
			if !s.audio(data) {
				return pumpStreamFailed
			}
		case websocket.TextMessage:
			var message clientMessage
			if json.Unmarshal(data, &message) != nil {
				continue
			}
			switch message.Type {
			case "stop":
				s.finish()
			case "cancel":
				return pumpClientLeft
			}
		}
	}
}

func (s *session) audio(pcm []byte) bool {
	if s.finished {
		return true
	}
	if s.audioBytes+len(pcm) > s.maxBytes {
		s.finish()
		return true
	}
	s.audioBytes += len(pcm)
	return s.stream.SendAudio(pcm) == nil
}

func (s *session) finish() {
	if s.finished {
		return
	}
	s.finished = true
	if err := s.stream.Finish(); err != nil {
		_ = s.stream.Close()
		return
	}
	s.finishTime = time.AfterFunc(finishTimeout, func() {
		s.timedOut.Store(true)
		_ = s.stream.Close()
	})
}

// forward is the only writer to the client once the session is ready.
func (s *session) forward() error {
	var last Snapshot
	for snapshot := range s.stream.Snapshots() {
		last = snapshot
		if s.cancelled.Load() {
			continue
		}
		_ = writeClientJSON(s.conn, transcriptMessage{
			Type:    "transcript",
			Final:   snapshot.Final,
			Partial: snapshot.Partial,
		})
	}
	defer func() { _ = s.conn.Close() }()
	if s.cancelled.Load() {
		return nil
	}
	var result error
	switch {
	case s.timedOut.Load():
		result = errSessionTimeout
		writeClientError(s.conn, ErrorCodeTimeout, "speech recognition did not finish in time")
	case s.stream.Err() != nil:
		result = s.stream.Err()
		writeClientError(s.conn, ErrorCodeProviderFailed, "speech recognition failed")
	default:
		_ = writeClientJSON(s.conn, transcriptMessage{Type: "done", Final: last.Text()})
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	_ = s.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	return result
}

func writeClientJSON(conn ClientConn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}

func writeClientError(conn ClientConn, code, message string) {
	_ = writeClientJSON(conn, errorMessage{Type: "error", Code: code, Message: message})
}
