// Package voice relays browser microphone audio to a streaming speech
// recognition provider and reports results as whole-transcript snapshots.
package voice

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ProviderOpenAI     = "openai"
	ProviderQwen       = "qwen"
	ProviderSoniox     = "soniox"
	ProviderVolcengine = "volcengine"
	ProviderBytePlus   = "byteplus"

	// Encoding is the only audio format accepted from clients: signed 16-bit
	// little-endian mono PCM at the provider sample rate.
	Encoding = "pcm_s16le"

	maxKeywords     = 100
	maxKeywordRunes = 64
	maxContextItems = 20
	maxContextRunes = 800
)

// Settings selects and configures the provider for one manager instance.
type Settings struct {
	Provider  string
	Languages []string
	OpenAI    OpenAISettings
	Qwen      QwenSettings
	Soniox    SonioxSettings
	SeedASR   SeedASRSettings
}

type OpenAISettings struct {
	APIKey string
	URL    string
	Model  string
	Delay  string
}

// QwenSettings configures Alibaba Cloud Model Studio realtime ASR.
type QwenSettings struct {
	APIKey string
	URL    string
	Model  string
	// Language pins recognition to one language code; empty auto-detects,
	// which suits mixed Chinese and English dictation.
	Language string
}

// SonioxSettings configures multilingual streaming dictation.
type SonioxSettings struct {
	APIKey    string
	URL       string
	Model     string
	Languages []string
}

// SeedASRSettings configures the ByteDance Seed ASR streaming API, served by
// Volcengine in mainland China and by BytePlus internationally.
type SeedASRSettings struct {
	URL        string
	APIKey     string
	AppKey     string
	AccessKey  string
	ResourceID string
}

// Options carries per-session recognition hints.
type Options struct {
	Keywords []string
	// Context holds recent conversation text, oldest first.
	Context []string
	// UserRef is a stable, privacy-preserving end-user identifier.
	UserRef string
}

// Snapshot is the whole transcript of a session so far. Final text never
// changes; Partial is the unstable tail that later results may rewrite.
type Snapshot struct {
	Final   string
	Partial string
}

func (s Snapshot) Text() string {
	return joinText(s.Final, s.Partial)
}

type Provider interface {
	Name() string
	SampleRate() int
	Open(ctx context.Context, opts Options) (Stream, error)
}

// Stream is one recognition session. SendAudio and Finish must be called from
// a single goroutine; Close may be called concurrently.
type Stream interface {
	SendAudio(pcm []byte) error
	// Finish marks the end of speech. Snapshots keeps delivering until the
	// provider returns its final result.
	Finish() error
	// Snapshots is closed when the session ends; Err then reports why.
	Snapshots() <-chan Snapshot
	Err() error
	Close() error
}

// NewProvider returns nil without an error when voice input is disabled.
func NewProvider(s Settings) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s.Provider)) {
	case "", "none", "disabled":
		return nil, nil
	case ProviderOpenAI:
		return newOpenAIProvider(s.OpenAI, s.Languages)
	case ProviderQwen:
		return newQwenProvider(s.Qwen)
	case ProviderSoniox:
		return newSonioxProvider(s.Soniox)
	case ProviderVolcengine:
		return newSeedASRProvider(ProviderVolcengine, s.SeedASR, defaultVolcengineURL)
	case ProviderBytePlus:
		return newSeedASRProvider(ProviderBytePlus, s.SeedASR, defaultBytePlusURL)
	default:
		return nil, fmt.Errorf("unknown voice provider %q", s.Provider)
	}
}

// normalizeOptions bounds client supplied hints so a provider never rejects a
// session because of oversized or malformed context.
func normalizeOptions(opts Options) Options {
	out := Options{UserRef: strings.TrimSpace(opts.UserRef)}
	seen := map[string]bool{}
	for _, raw := range opts.Keywords {
		keyword := truncateRunes(singleLine(raw), maxKeywordRunes)
		if keyword == "" || seen[keyword] {
			continue
		}
		seen[keyword] = true
		out.Keywords = append(out.Keywords, keyword)
		if len(out.Keywords) == maxKeywords {
			break
		}
	}
	budget := maxContextRunes
	var newestFirst []string
	for i := len(opts.Context) - 1; i >= 0 && len(newestFirst) < maxContextItems; i-- {
		text := strings.Join(strings.Fields(opts.Context[i]), " ")
		if text == "" {
			continue
		}
		text = truncateRunes(text, budget)
		budget -= utf8.RuneCountInString(text)
		newestFirst = append(newestFirst, text)
		if budget <= 0 {
			break
		}
	}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		out.Context = append(out.Context, newestFirst[i])
	}
	return out
}

func singleLine(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '<' || r == '>':
			return -1
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, raw)
	return strings.Join(strings.Fields(cleaned), " ")
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// joinText concatenates transcript pieces, adding a space only where the
// boundary would otherwise glue two Latin words together. Chinese text is
// joined without spaces.
func joinText(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			last, _ := utf8.DecodeLastRuneInString(b.String())
			first, _ := utf8.DecodeRuneInString(part)
			if needsSpace(last, first) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(part)
	}
	return b.String()
}

func needsSpace(last, first rune) bool {
	if first >= utf8.RuneSelf || last >= utf8.RuneSelf {
		return false
	}
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return false
	}
	return unicode.IsLetter(last) || unicode.IsDigit(last) || strings.ContainsRune(".,!?;:", last)
}

var errStreamFinished = errors.New("voice stream already finished")

const providerWriteTimeout = 10 * time.Second

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// dialError keeps the provider's rejection reason, which is usually the only
// hint for bad credentials or an unsupported region.
func dialError(provider string, resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("%s: dial: %w", provider, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	detail := truncateRunes(strings.TrimSpace(string(body)), 300)
	if logID := resp.Header.Get("X-Tt-Logid"); logID != "" {
		detail = strings.TrimSpace(detail + " log_id=" + logID)
	}
	return fmt.Errorf("%s: dial rejected with HTTP %d: %s", provider, resp.StatusCode, detail)
}

// realtimeError is the error object of OpenAI-style realtime events.
type realtimeError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *realtimeError) message() string {
	if e == nil {
		return "unknown error"
	}
	return firstNonEmpty(e.Message, e.Code, e.Type, "unknown error")
}

func newEventID() string {
	return "event_" + strings.ReplaceAll(newRequestID(), "-", "")
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// snapshotPipe delivers the latest snapshot to one consumer. Snapshots are
// cumulative, so replacing an unread snapshot never loses final text.
type snapshotPipe struct {
	ch  chan Snapshot
	err error
}

func newSnapshotPipe() *snapshotPipe {
	return &snapshotPipe{ch: make(chan Snapshot, 1)}
}

// publish must only be called by the single producer goroutine.
func (p *snapshotPipe) publish(s Snapshot) {
	for {
		select {
		case p.ch <- s:
			return
		default:
			select {
			case <-p.ch:
			default:
			}
		}
	}
}

// finish records the terminal error and closes the channel. The close
// publishes err to consumers that observe the closed channel.
func (p *snapshotPipe) finish(err error) {
	p.err = err
	close(p.ch)
}
