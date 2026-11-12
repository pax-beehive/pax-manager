package voice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultOpenAIURL   = "wss://api.openai.com/v1/realtime?intent=transcription"
	defaultOpenAIModel = "gpt-live-transcribe"
	defaultOpenAIDelay = "low"

	openAISampleRate = 24000
	// Commits of less than 100 ms of audio are rejected, so shorter sessions
	// end without one.
	openAIMinCommitBytes = openAISampleRate / 10 * 2
	openAIPromptPrefix   = "Dictation for a coding agent. Recent conversation:\n"
)

type openAIProvider struct {
	settings  OpenAISettings
	languages []string
	dialer    *websocket.Dialer
}

func newOpenAIProvider(settings OpenAISettings, languages []string) (*openAIProvider, error) {
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	if settings.APIKey == "" {
		return nil, errors.New("openai voice provider requires OPENAI_API_KEY")
	}
	settings.URL = firstNonEmpty(settings.URL, defaultOpenAIURL)
	settings.Model = firstNonEmpty(settings.Model, defaultOpenAIModel)
	settings.Delay = firstNonEmpty(settings.Delay, defaultOpenAIDelay)
	var cleaned []string
	for _, language := range languages {
		if language = strings.ToLower(strings.TrimSpace(language)); language != "" {
			cleaned = append(cleaned, language)
		}
	}
	return &openAIProvider{
		settings:  settings,
		languages: cleaned,
		dialer:    websocket.DefaultDialer,
	}, nil
}

func (p *openAIProvider) Name() string { return ProviderOpenAI }

func (p *openAIProvider) SampleRate() int { return openAISampleRate }

func (p *openAIProvider) Open(ctx context.Context, opts Options) (Stream, error) {
	opts = normalizeOptions(opts)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+p.settings.APIKey)
	if opts.UserRef != "" {
		header.Set("OpenAI-Safety-Identifier", opts.UserRef)
	}
	conn, resp, err := p.dialer.DialContext(ctx, p.settings.URL, header)
	if err != nil {
		return nil, dialError("openai", resp, err)
	}
	stream := &openAIStream{conn: conn, pipe: newSnapshotPipe(), items: newTranscriptItems()}
	// Events on one connection are processed in order, so audio sent after
	// this update is transcribed with the session configuration below.
	if err := stream.writeJSON(p.sessionUpdate(opts)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("openai: send session update: %w", err)
	}
	go stream.readLoop()
	return stream, nil
}

type openAISessionUpdate struct {
	Type    string        `json:"type"`
	Session openAISession `json:"session"`
}

type openAISession struct {
	Type  string           `json:"type"`
	Audio openAIAudioInput `json:"audio"`
}

type openAIAudioInput struct {
	Input openAIAudioInputConfig `json:"input"`
}

type openAIAudioInputConfig struct {
	Format        openAIAudioFormat   `json:"format"`
	Transcription openAITranscription `json:"transcription"`
	// TurnDetection is always null: the model does not support server VAD, so
	// the stream commits audio when the client stops.
	TurnDetection *struct{} `json:"turn_detection"`
}

type openAIAudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type openAITranscription struct {
	Model     string   `json:"model"`
	Delay     string   `json:"delay,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Prompt    string   `json:"prompt,omitempty"`
	Keywords  []string `json:"keywords,omitempty"`
}

func (p *openAIProvider) sessionUpdate(opts Options) openAISessionUpdate {
	transcription := openAITranscription{
		Model:     p.settings.Model,
		Delay:     p.settings.Delay,
		Languages: p.languages,
		Keywords:  opts.Keywords,
	}
	if len(opts.Context) > 0 {
		transcription.Prompt = openAIPromptPrefix + strings.Join(opts.Context, "\n")
	}
	return openAISessionUpdate{
		Type: "session.update",
		Session: openAISession{
			Type: "transcription",
			Audio: openAIAudioInput{Input: openAIAudioInputConfig{
				Format:        openAIAudioFormat{Type: "audio/pcm", Rate: openAISampleRate},
				Transcription: transcription,
			}},
		},
	}
}

type openAIEvent struct {
	Type       string         `json:"type"`
	ItemID     string         `json:"item_id"`
	Delta      string         `json:"delta"`
	Transcript string         `json:"transcript"`
	Error      *realtimeError `json:"error"`
}

type openAIStream struct {
	conn       *websocket.Conn
	pipe       *snapshotPipe
	items      *transcriptItems
	audioBytes int
	closeOnce  sync.Once

	mu        sync.Mutex
	finishing bool
	committed bool
}

func (s *openAIStream) SendAudio(pcm []byte) error {
	s.mu.Lock()
	finishing := s.finishing
	s.mu.Unlock()
	if finishing {
		return errStreamFinished
	}
	if len(pcm) == 0 {
		return nil
	}
	s.audioBytes += len(pcm)
	return s.writeJSON(map[string]string{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(pcm),
	})
}

func (s *openAIStream) Finish() error {
	s.mu.Lock()
	if s.finishing {
		s.mu.Unlock()
		return nil
	}
	s.finishing = true
	s.committed = s.audioBytes >= openAIMinCommitBytes
	commit := s.committed
	s.mu.Unlock()
	if !commit {
		// Too little audio to transcribe; closing ends the session cleanly.
		return s.Close()
	}
	return s.writeJSON(map[string]string{"type": "input_audio_buffer.commit"})
}

func (s *openAIStream) Snapshots() <-chan Snapshot { return s.pipe.ch }

func (s *openAIStream) Err() error { return s.pipe.err }

func (s *openAIStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.conn.Close() })
	return err
}

func (s *openAIStream) writeJSON(v any) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(providerWriteTimeout))
	return s.conn.WriteJSON(v)
}

func (s *openAIStream) readLoop() {
	defer func() { _ = s.Close() }()
	for {
		var event openAIEvent
		if err := s.conn.ReadJSON(&event); err != nil {
			s.pipe.finish(s.readError(err))
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

func (s *openAIStream) readError(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishing && !s.committed {
		return nil
	}
	return fmt.Errorf("openai: %w", err)
}

func (s *openAIStream) handle(event openAIEvent) (bool, error) {
	switch event.Type {
	case "input_audio_buffer.committed":
		s.items.get(event.ItemID)
	case "conversation.item.input_audio_transcription.delta":
		item := s.items.get(event.ItemID)
		item.text += event.Delta
		s.pipe.publish(s.items.snapshot())
	case "conversation.item.input_audio_transcription.completed":
		item := s.completedItem(event.ItemID)
		item.text = event.Transcript
		item.final = true
		s.pipe.publish(s.items.snapshot())
		return s.complete(), nil
	case "conversation.item.input_audio_transcription.failed":
		return false, fmt.Errorf("openai: transcription failed: %s", event.Error.message())
	case "error":
		return false, fmt.Errorf("openai: %s", event.Error.message())
	}
	return false, nil
}

// completedItem maps a completion to the item its deltas streamed into. When
// the completion carries an unseen id and exactly one item is still pending,
// the completion replaces that item instead of duplicating its text.
func (s *openAIStream) completedItem(id string) *transcriptItem {
	id = firstNonEmpty(id, liveItemID)
	if item, ok := s.items.byID[id]; ok {
		return item
	}
	var pending []*transcriptItem
	for _, item := range s.items.items {
		if !item.final {
			pending = append(pending, item)
		}
	}
	if len(pending) == 1 {
		s.items.byID[id] = pending[0]
		return pending[0]
	}
	return s.items.get(id)
}

func (s *openAIStream) complete() bool {
	s.mu.Lock()
	committed := s.committed
	s.mu.Unlock()
	return committed && s.items.allFinal()
}
