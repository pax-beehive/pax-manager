package voice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// International (Singapore) Model Studio endpoint. API keys are regional,
	// so a mainland key needs VOICE_QWEN_URL pointed at dashscope.aliyuncs.com.
	defaultQwenURL   = "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime"
	defaultQwenModel = "qwen3-asr-flash-realtime"

	qwenSampleRate = 16000
	// The documented "balanced" server VAD preset.
	qwenVADThreshold       = 0.2
	qwenVADSilenceDuration = 800
)

type qwenProvider struct {
	settings QwenSettings
	endpoint string
	dialer   *websocket.Dialer
}

func newQwenProvider(settings QwenSettings) (*qwenProvider, error) {
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	if settings.APIKey == "" {
		return nil, errors.New("qwen voice provider requires DASHSCOPE_API_KEY")
	}
	settings.Model = firstNonEmpty(settings.Model, defaultQwenModel)
	settings.Language = strings.TrimSpace(settings.Language)
	endpoint, err := qwenEndpoint(firstNonEmpty(settings.URL, defaultQwenURL), settings.Model)
	if err != nil {
		return nil, err
	}
	return &qwenProvider{
		settings: settings,
		endpoint: endpoint,
		dialer:   websocket.DefaultDialer,
	}, nil
}

// qwenEndpoint adds the model query parameter unless the URL already has one.
func qwenEndpoint(raw, model string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Host == "" {
		return "", fmt.Errorf("invalid qwen realtime url %q", raw)
	}
	query := endpoint.Query()
	if query.Get("model") == "" {
		query.Set("model", model)
		endpoint.RawQuery = query.Encode()
	}
	return endpoint.String(), nil
}

func (p *qwenProvider) Name() string { return ProviderQwen }

func (p *qwenProvider) SampleRate() int { return qwenSampleRate }

func (p *qwenProvider) Open(ctx context.Context, opts Options) (Stream, error) {
	opts = normalizeOptions(opts)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+p.settings.APIKey)
	header.Set("OpenAI-Beta", "realtime=v1")
	conn, resp, err := p.dialer.DialContext(ctx, p.endpoint, header)
	if err != nil {
		return nil, dialError("qwen", resp, err)
	}
	stream := &qwenStream{conn: conn, pipe: newSnapshotPipe(), items: newTranscriptItems()}
	// Events on one connection are processed in order, so audio sent after
	// this update uses the session configuration below.
	if err := stream.writeJSON(p.sessionUpdate(opts)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("qwen: send session update: %w", err)
	}
	go stream.readLoop()
	return stream, nil
}

type qwenSessionUpdate struct {
	EventID string      `json:"event_id"`
	Type    string      `json:"type"`
	Session qwenSession `json:"session"`
}

type qwenSession struct {
	Modalities              []string           `json:"modalities"`
	InputAudioFormat        string             `json:"input_audio_format"`
	SampleRate              int                `json:"sample_rate"`
	InputAudioTranscription *qwenTranscription `json:"input_audio_transcription,omitempty"`
	// Server VAD finalizes each utterance at a pause, so long dictation keeps
	// producing final text instead of one result after stop.
	TurnDetection qwenTurnDetection `json:"turn_detection"`
}

type qwenTranscription struct {
	Language string      `json:"language,omitempty"`
	Corpus   *qwenCorpus `json:"corpus,omitempty"`
}

type qwenCorpus struct {
	// Text biases recognition toward terms and context it mentions.
	Text string `json:"text"`
}

type qwenTurnDetection struct {
	Type              string  `json:"type"`
	Threshold         float64 `json:"threshold"`
	SilenceDurationMS int     `json:"silence_duration_ms"`
}

func (p *qwenProvider) sessionUpdate(opts Options) qwenSessionUpdate {
	session := qwenSession{
		Modalities:       []string{"text"},
		InputAudioFormat: "pcm",
		SampleRate:       qwenSampleRate,
		TurnDetection: qwenTurnDetection{
			Type:              "server_vad",
			Threshold:         qwenVADThreshold,
			SilenceDurationMS: qwenVADSilenceDuration,
		},
	}
	transcription := qwenTranscription{Language: p.settings.Language}
	if corpus := qwenCorpusText(opts); corpus != "" {
		transcription.Corpus = &qwenCorpus{Text: corpus}
	}
	if transcription.Language != "" || transcription.Corpus != nil {
		session.InputAudioTranscription = &transcription
	}
	return qwenSessionUpdate{EventID: newEventID(), Type: "session.update", Session: session}
}

func qwenCorpusText(opts Options) string {
	var parts []string
	if len(opts.Keywords) > 0 {
		parts = append(parts, "Keywords: "+strings.Join(opts.Keywords, ", "))
	}
	if len(opts.Context) > 0 {
		parts = append(parts, "Recent conversation:\n"+strings.Join(opts.Context, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

type qwenEvent struct {
	Type       string         `json:"type"`
	ItemID     string         `json:"item_id"`
	Text       string         `json:"text"`
	Stash      string         `json:"stash"`
	Transcript string         `json:"transcript"`
	Error      *realtimeError `json:"error"`
}

type qwenStream struct {
	conn      *websocket.Conn
	pipe      *snapshotPipe
	items     *transcriptItems
	finishing bool
	closeOnce sync.Once
}

func (s *qwenStream) SendAudio(pcm []byte) error {
	if s.finishing {
		return errStreamFinished
	}
	if len(pcm) == 0 {
		return nil
	}
	return s.writeJSON(map[string]string{
		"event_id": newEventID(),
		"type":     "input_audio_buffer.append",
		"audio":    base64.StdEncoding.EncodeToString(pcm),
	})
}

// Finish asks the service to finalize pending speech. It replies with the
// last transcription and then session.finished.
func (s *qwenStream) Finish() error {
	if s.finishing {
		return nil
	}
	s.finishing = true
	return s.writeJSON(map[string]string{"event_id": newEventID(), "type": "session.finish"})
}

func (s *qwenStream) Snapshots() <-chan Snapshot { return s.pipe.ch }

func (s *qwenStream) Err() error { return s.pipe.err }

func (s *qwenStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.conn.Close() })
	return err
}

func (s *qwenStream) writeJSON(v any) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(providerWriteTimeout))
	return s.conn.WriteJSON(v)
}

func (s *qwenStream) readLoop() {
	defer func() { _ = s.Close() }()
	for {
		var event qwenEvent
		if err := s.conn.ReadJSON(&event); err != nil {
			s.pipe.finish(fmt.Errorf("qwen: %w", err))
			return
		}
		done, err := s.handle(event)
		if err != nil {
			s.pipe.finish(err)
			return
		}
		if done {
			s.pipe.finish(nil)
			return
		}
	}
}

func (s *qwenStream) handle(event qwenEvent) (bool, error) {
	switch event.Type {
	case "conversation.item.input_audio_transcription.text":
		// text is the settled prefix of the utterance and stash the draft
		// tail; together they are the current utterance.
		item := s.items.get(event.ItemID)
		if !item.final {
			item.text = event.Text + event.Stash
			s.pipe.publish(s.items.snapshot())
		}
	case "conversation.item.input_audio_transcription.completed":
		item := s.items.get(event.ItemID)
		item.text = event.Transcript
		item.final = true
		s.pipe.publish(s.items.snapshot())
	case "conversation.item.input_audio_transcription.failed":
		return false, fmt.Errorf("qwen: transcription failed: %s", event.Error.message())
	case "error":
		return false, fmt.Errorf("qwen: %s", event.Error.message())
	case "session.finished":
		return true, nil
	}
	return false, nil
}
