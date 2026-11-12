package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultVolcengineURL = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async"
	defaultBytePlusURL   = "wss://voice.ap-southeast-1.bytepluses.com/api/v3/sauc/bigmodel_async"
	// Seed ASR 2.0, billed by audio duration.
	defaultSeedASRResourceID = "volc.seedasr.sauc.duration"

	seedASRSampleRate       = 16000
	seedASRHandshakeTimeout = 10 * time.Second
	// The last frame must carry audio; 100 ms of silence closes the stream.
	seedASRTrailingSilenceBytes = seedASRSampleRate / 10 * 2
)

type seedASRProvider struct {
	name     string
	settings SeedASRSettings
	dialer   *websocket.Dialer
}

func newSeedASRProvider(
	name string,
	settings SeedASRSettings,
	defaultURL string,
) (*seedASRProvider, error) {
	settings.URL = firstNonEmpty(settings.URL, defaultURL)
	settings.ResourceID = firstNonEmpty(settings.ResourceID, defaultSeedASRResourceID)
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	settings.AppKey = strings.TrimSpace(settings.AppKey)
	settings.AccessKey = strings.TrimSpace(settings.AccessKey)
	if settings.APIKey == "" && (settings.AppKey == "" || settings.AccessKey == "") {
		return nil, errors.New(
			"seed asr requires VOICE_SEEDASR_API_KEY, or VOICE_SEEDASR_APP_KEY with " +
				"VOICE_SEEDASR_ACCESS_KEY",
		)
	}
	return &seedASRProvider{name: name, settings: settings, dialer: websocket.DefaultDialer}, nil
}

func (p *seedASRProvider) Name() string { return p.name }

func (p *seedASRProvider) SampleRate() int { return seedASRSampleRate }

func (p *seedASRProvider) Open(ctx context.Context, opts Options) (Stream, error) {
	request, err := json.Marshal(seedASRRequest(normalizeOptions(opts)))
	if err != nil {
		return nil, err
	}
	conn, resp, err := p.dialer.DialContext(ctx, p.settings.URL, p.headers())
	if err != nil {
		return nil, dialError("seed asr", resp, err)
	}
	stream := &seedASRStream{conn: conn, pipe: newSnapshotPipe(), sequence: 1}
	if err := stream.writeFrame(seedMsgFullClientRequest, seedFlagSequence, 1, request); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("seed asr: send session request: %w", err)
	}
	// The service acknowledges the session request before audio may flow.
	_ = conn.SetReadDeadline(time.Now().Add(seedASRHandshakeTimeout))
	if _, err := readSeedFrame(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("seed asr: session rejected: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	go stream.readLoop()
	return stream, nil
}

func (p *seedASRProvider) headers() http.Header {
	header := http.Header{}
	header.Set("X-Api-Resource-Id", p.settings.ResourceID)
	// Current docs name the trace id X-Api-Request-Id; older Volcengine docs
	// use X-Api-Connect-Id. Sending both keeps either console working.
	requestID := newRequestID()
	header.Set("X-Api-Request-Id", requestID)
	header.Set("X-Api-Connect-Id", requestID)
	if p.settings.APIKey != "" {
		header.Set("X-Api-Key", p.settings.APIKey)
	} else {
		header.Set("X-Api-App-Key", p.settings.AppKey)
		header.Set("X-Api-Access-Key", p.settings.AccessKey)
	}
	return header
}

type seedRequest struct {
	Audio   seedAudio          `json:"audio"`
	Request seedRequestOptions `json:"request"`
}

type seedAudio struct {
	Format  string `json:"format"`
	Codec   string `json:"codec"`
	Rate    int    `json:"rate"`
	Bits    int    `json:"bits"`
	Channel int    `json:"channel"`
}

type seedRequestOptions struct {
	ModelName string `json:"model_name"`
	// EnableNonstream turns on two-pass recognition: streaming partials, then
	// a non-streaming pass that finalizes each utterance.
	EnableNonstream bool        `json:"enable_nonstream"`
	EnableITN       bool        `json:"enable_itn"`
	EnablePunc      bool        `json:"enable_punc"`
	ShowUtterances  bool        `json:"show_utterances"`
	ResultType      string      `json:"result_type"`
	Corpus          *seedCorpus `json:"corpus,omitempty"`
}

type seedCorpus struct {
	// Context is a JSON document serialized as a string.
	Context string `json:"context"`
}

type seedContext struct {
	Hotwords    []seedHotword     `json:"hotwords,omitempty"`
	ContextType string            `json:"context_type,omitempty"`
	ContextData []seedContextData `json:"context_data,omitempty"`
}

type seedHotword struct {
	Word string `json:"word"`
}

type seedContextData struct {
	Text string `json:"text"`
}

func seedASRRequest(opts Options) seedRequest {
	request := seedRequest{
		Audio: seedAudio{
			Format:  "pcm",
			Codec:   "raw",
			Rate:    seedASRSampleRate,
			Bits:    16,
			Channel: 1,
		},
		Request: seedRequestOptions{
			ModelName:       "bigmodel",
			EnableNonstream: true,
			EnableITN:       true,
			EnablePunc:      true,
			ShowUtterances:  true,
			ResultType:      "full",
		},
	}
	if context := seedASRContext(opts); context != "" {
		request.Request.Corpus = &seedCorpus{Context: context}
	}
	return request
}

func seedASRContext(opts Options) string {
	var context seedContext
	for _, keyword := range opts.Keywords {
		context.Hotwords = append(context.Hotwords, seedHotword{Word: keyword})
	}
	// Dialog context is ordered newest first.
	for i := len(opts.Context) - 1; i >= 0; i-- {
		context.ContextData = append(context.ContextData, seedContextData{Text: opts.Context[i]})
	}
	if len(context.ContextData) > 0 {
		context.ContextType = "dialog_ctx"
	}
	if len(context.Hotwords) == 0 && len(context.ContextData) == 0 {
		return ""
	}
	raw, err := json.Marshal(context)
	if err != nil {
		return ""
	}
	return string(raw)
}

type seedASRStream struct {
	conn      *websocket.Conn
	pipe      *snapshotPipe
	sequence  int32
	finished  bool
	closeOnce sync.Once
}

func (s *seedASRStream) SendAudio(pcm []byte) error {
	if s.finished {
		return errStreamFinished
	}
	if len(pcm) == 0 {
		return nil
	}
	s.sequence++
	return s.writeFrame(seedMsgAudioOnlyRequest, seedFlagSequence, s.sequence, pcm)
}

func (s *seedASRStream) Finish() error {
	if s.finished {
		return nil
	}
	s.finished = true
	s.sequence++
	return s.writeFrame(
		seedMsgAudioOnlyRequest,
		seedFlagLastSequence,
		-s.sequence,
		make([]byte, seedASRTrailingSilenceBytes),
	)
}

func (s *seedASRStream) Snapshots() <-chan Snapshot { return s.pipe.ch }

func (s *seedASRStream) Err() error { return s.pipe.err }

func (s *seedASRStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.conn.Close() })
	return err
}

func (s *seedASRStream) writeFrame(messageType, flags byte, sequence int32, payload []byte) error {
	frame, err := encodeSeedFrame(messageType, flags, sequence, payload)
	if err != nil {
		return err
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(providerWriteTimeout))
	return s.conn.WriteMessage(websocket.BinaryMessage, frame)
}

func (s *seedASRStream) readLoop() {
	defer func() { _ = s.Close() }()
	for {
		frame, err := readSeedFrame(s.conn)
		if err != nil {
			s.pipe.finish(fmt.Errorf("seed asr: %w", err))
			return
		}
		snapshot, ok, err := parseSeedResponse(frame.payload)
		if err != nil {
			s.pipe.finish(err)
			return
		}
		if ok {
			s.pipe.publish(snapshot)
		}
		if frame.last() {
			s.pipe.finish(nil)
			return
		}
	}
}

func readSeedFrame(conn *websocket.Conn) (seedFrame, error) {
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		return seedFrame{}, err
	}
	if messageType != websocket.BinaryMessage {
		return seedFrame{}, fmt.Errorf("unexpected websocket message type %d", messageType)
	}
	frame, err := decodeSeedFrame(data)
	if err != nil {
		return seedFrame{}, err
	}
	if frame.messageType == seedMsgServerError {
		return frame, fmt.Errorf(
			"service error %d: %s",
			frame.errorCode,
			truncateRunes(string(frame.payload), 300),
		)
	}
	return frame, nil
}

type seedResponse struct {
	Result json.RawMessage `json:"result"`
}

type seedResult struct {
	Text       string          `json:"text"`
	Utterances []seedUtterance `json:"utterances"`
}

type seedUtterance struct {
	Text     string `json:"text"`
	Definite bool   `json:"definite"`
}

func parseSeedResponse(payload []byte) (Snapshot, bool, error) {
	if len(payload) == 0 {
		return Snapshot{}, false, nil
	}
	var response seedResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return Snapshot{}, false, fmt.Errorf("seed asr: decode response: %w", err)
	}
	raw := []byte(strings.TrimSpace(string(response.Result)))
	if len(raw) == 0 || string(raw) == "null" {
		return Snapshot{}, false, nil
	}
	var result seedResult
	if raw[0] == '[' {
		var results []seedResult
		if err := json.Unmarshal(raw, &results); err != nil {
			return Snapshot{}, false, fmt.Errorf("seed asr: decode result: %w", err)
		}
		if len(results) == 0 {
			return Snapshot{}, false, nil
		}
		result = results[0]
	} else if err := json.Unmarshal(raw, &result); err != nil {
		return Snapshot{}, false, fmt.Errorf("seed asr: decode result: %w", err)
	}
	return seedSnapshot(result), true, nil
}

// seedSnapshot keeps transcript order: only the leading run of definite
// utterances is final, everything after the first unstable one is partial.
func seedSnapshot(result seedResult) Snapshot {
	if len(result.Utterances) == 0 {
		return Snapshot{Partial: result.Text}
	}
	var final, partial []string
	for _, utterance := range result.Utterances {
		if utterance.Definite && len(partial) == 0 {
			final = append(final, utterance.Text)
		} else {
			partial = append(partial, utterance.Text)
		}
	}
	return Snapshot{Final: joinText(final...), Partial: joinText(partial...)}
}
