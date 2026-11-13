package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	defaultSonioxURL   = "wss://stt-rt.soniox.com/transcribe-websocket"
	defaultSonioxModel = "stt-rt-v6"
	sonioxSampleRate   = 16000
)

type sonioxProvider struct {
	settings SonioxSettings
	dialer   *websocket.Dialer
}

func newSonioxProvider(settings SonioxSettings) (*sonioxProvider, error) {
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	if settings.APIKey == "" {
		return nil, errors.New("soniox voice provider requires SONIOX_API_KEY")
	}
	settings.URL = firstNonEmpty(settings.URL, defaultSonioxURL)
	endpoint, err := url.Parse(settings.URL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		(endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
		return nil, errors.New("invalid soniox realtime url")
	}
	settings.Model = firstNonEmpty(settings.Model, defaultSonioxModel)
	settings.Languages = sonioxLanguageHints(settings.Languages)
	return &sonioxProvider{settings: settings, dialer: websocket.DefaultDialer}, nil
}

func sonioxLanguageHints(languages []string) []string {
	var hints []string
	seen := map[string]bool{}
	for _, raw := range languages {
		language := strings.ToLower(strings.TrimSpace(raw))
		// Soniox uses ISO language codes, rather than locale tags such as zh-CN.
		language, _, _ = strings.Cut(strings.ReplaceAll(language, "_", "-"), "-")
		if language != "" && !seen[language] {
			seen[language] = true
			hints = append(hints, language)
		}
	}
	return hints
}

func (p *sonioxProvider) Name() string { return ProviderSoniox }

func (p *sonioxProvider) SampleRate() int { return sonioxSampleRate }

func (p *sonioxProvider) Open(ctx context.Context, opts Options) (Stream, error) {
	opts = normalizeOptions(opts)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+p.settings.APIKey)
	conn, resp, err := p.dialer.DialContext(ctx, p.settings.URL, header)
	if err != nil {
		return nil, p.redactError(dialError(ProviderSoniox, resp, err))
	}
	stream := &sonioxStream{
		conn:   conn,
		pipe:   newSnapshotPipe(),
		apiKey: p.settings.APIKey,
	}
	_ = conn.SetWriteDeadline(time.Now().Add(providerWriteTimeout))
	if err := conn.WriteJSON(p.sessionConfig(opts)); err != nil {
		_ = conn.Close()
		return nil, p.redactError(fmt.Errorf("soniox: send session config: %w", err))
	}
	go stream.readLoop()
	return stream, nil
}

func (p *sonioxProvider) redactError(err error) error {
	return redactSonioxError(err, p.settings.APIKey)
}

func redactSonioxError(err error, apiKey string) error {
	if err == nil || apiKey == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), apiKey, "[redacted]"))
}

type sonioxConfig struct {
	Model                   string         `json:"model"`
	AudioFormat             string         `json:"audio_format"`
	SampleRate              int            `json:"sample_rate"`
	NumChannels             int            `json:"num_channels"`
	LanguageHints           []string       `json:"language_hints,omitempty"`
	EnableEndpointDetection bool           `json:"enable_endpoint_detection"`
	Context                 *sonioxContext `json:"context,omitempty"`
}

type sonioxContext struct {
	Terms []string `json:"terms,omitempty"`
	Text  string   `json:"text,omitempty"`
}

func (p *sonioxProvider) sessionConfig(opts Options) sonioxConfig {
	config := sonioxConfig{
		Model:                   p.settings.Model,
		AudioFormat:             Encoding,
		SampleRate:              sonioxSampleRate,
		NumChannels:             1,
		LanguageHints:           p.settings.Languages,
		EnableEndpointDetection: true,
	}
	if len(opts.Keywords) > 0 || len(opts.Context) > 0 {
		config.Context = &sonioxContext{
			Terms: opts.Keywords,
			Text:  strings.Join(opts.Context, "\n"),
		}
	}
	return config
}

type sonioxToken struct {
	Text    string `json:"text"`
	IsFinal bool   `json:"is_final"`
}

type sonioxEvent struct {
	Tokens       []sonioxToken `json:"tokens"`
	Finished     bool          `json:"finished"`
	ErrorCode    int           `json:"error_code"`
	ErrorType    string        `json:"error_type"`
	ErrorMessage string        `json:"error_message"`
}

type sonioxStream struct {
	conn      *websocket.Conn
	pipe      *snapshotPipe
	apiKey    string
	final     strings.Builder
	published int
	finishing bool
	closeOnce sync.Once
}

func (s *sonioxStream) SendAudio(pcm []byte) error {
	if s.finishing {
		return errStreamFinished
	}
	if len(pcm) == 0 {
		return nil
	}
	return s.writeMessage(websocket.BinaryMessage, pcm)
}

func (s *sonioxStream) Finish() error {
	if s.finishing {
		return nil
	}
	s.finishing = true
	// Only an empty text frame ends audio input; an empty binary frame does not.
	return s.writeMessage(websocket.TextMessage, nil)
}

func (s *sonioxStream) writeMessage(kind int, data []byte) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(providerWriteTimeout))
	return redactSonioxError(s.conn.WriteMessage(kind, data), s.apiKey)
}

func (s *sonioxStream) Snapshots() <-chan Snapshot { return s.pipe.ch }

func (s *sonioxStream) Err() error { return s.pipe.err }

func (s *sonioxStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.conn.Close() })
	return err
}

func (s *sonioxStream) readLoop() {
	defer func() { _ = s.Close() }()
	for {
		var event sonioxEvent
		if err := s.conn.ReadJSON(&event); err != nil {
			s.pipe.finish(redactSonioxError(fmt.Errorf("soniox: %w", err), s.apiKey))
			return
		}
		done, err := s.handle(event)
		if err != nil || done {
			s.pipe.finish(err)
			return
		}
	}
}

func (s *sonioxStream) handle(event sonioxEvent) (bool, error) {
	if event.ErrorCode != 0 || event.ErrorType != "" || event.ErrorMessage != "" {
		message := firstNonEmpty(event.ErrorMessage, event.ErrorType, "speech recognition failed")
		err := fmt.Errorf(
			"soniox: %s (type %s, code %d)",
			message,
			event.ErrorType,
			event.ErrorCode,
		)
		return false, redactSonioxError(err, s.apiKey)
	}
	var partial strings.Builder
	segmentEnded := false
	for _, token := range event.Tokens {
		if token.Text == "<end>" || token.Text == "<fin>" {
			segmentEnded = segmentEnded || token.IsFinal
			continue
		}
		if token.IsFinal {
			s.final.WriteString(token.Text)
		} else {
			partial.WriteString(token.Text)
		}
	}
	if event.Tokens != nil || event.Finished {
		s.pipe.publish(s.snapshot(partial.String(), segmentEnded || event.Finished))
	}
	return event.Finished, nil
}

func (s *sonioxStream) snapshot(partial string, complete bool) Snapshot {
	final := s.final.String()
	cut := len(final)
	last, _ := utf8.DecodeLastRuneInString(final)
	first, _ := utf8.DecodeRuneInString(partial)
	if !complete && needsSpace(last, 'a') && (partial == "" || needsSpace('a', first)) {
		// The browser inserts spaces between final/partial Latin words. Keep
		// finalized subwords in the mutable tail until their boundary is known.
		for cut > s.published && final[cut-1] < utf8.RuneSelf &&
			!unicode.IsSpace(rune(final[cut-1])) {
			cut--
		}
	}
	s.published = cut
	return Snapshot{Final: final[:cut], Partial: final[cut:] + partial}
}
