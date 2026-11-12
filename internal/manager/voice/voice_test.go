package voice

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderGivenNoProviderThenVoiceIsDisabled(t *testing.T) {
	for _, name := range []string{"", "none", " Disabled "} {
		provider, err := NewProvider(Settings{Provider: name})
		require.NoError(t, err)
		assert.Nil(t, provider)
	}
}

func TestNewProviderGivenMissingCredentialsThenItFails(t *testing.T) {
	for _, name := range []string{
		ProviderOpenAI,
		ProviderQwen,
		ProviderVolcengine,
		ProviderBytePlus,
	} {
		_, err := NewProvider(Settings{Provider: name})
		assert.Error(t, err, name)
	}
	_, err := NewProvider(Settings{Provider: "whisper"})
	assert.ErrorContains(t, err, "unknown voice provider")
}

func TestNewProviderGivenSeedASRThenRegionDefaultsApply(t *testing.T) {
	provider, err := NewProvider(Settings{
		Provider: ProviderVolcengine,
		SeedASR:  SeedASRSettings{AppKey: "app", AccessKey: "access"},
	})
	require.NoError(t, err)
	seed := provider.(*seedASRProvider)
	assert.Equal(t, defaultVolcengineURL, seed.settings.URL)
	assert.Equal(t, defaultSeedASRResourceID, seed.settings.ResourceID)
	assert.Equal(t, 16000, provider.SampleRate())
	headers := seed.headers()
	assert.Equal(t, "app", headers.Get("X-Api-App-Key"))
	assert.Equal(t, "access", headers.Get("X-Api-Access-Key"))
	assert.Empty(t, headers.Get("X-Api-Key"))

	provider, err = NewProvider(Settings{
		Provider: ProviderBytePlus,
		SeedASR:  SeedASRSettings{APIKey: "key"},
	})
	require.NoError(t, err)
	assert.Equal(t, defaultBytePlusURL, provider.(*seedASRProvider).settings.URL)
	assert.Equal(t, ProviderBytePlus, provider.Name())
}

func TestNormalizeOptionsGivenNoisyHintsThenTheyAreBounded(t *testing.T) {
	long := strings.Repeat("x", 200)
	keywords := []string{" paxd ", "paxd", "<script>", "two\nlines", "", long}
	for i := 0; i < maxKeywords+10; i++ {
		keywords = append(keywords, fmt.Sprintf("kw%d", i))
	}
	opts := normalizeOptions(Options{
		Keywords: keywords,
		Context: []string{
			strings.Repeat("old ", 400),
			"  middle\n message ",
			"newest",
		},
		UserRef: " ref ",
	})
	assert.Equal(
		t,
		[]string{"paxd", "script", "two lines", long[:maxKeywordRunes], "kw0"},
		opts.Keywords[:5],
	)
	assert.Len(t, opts.Keywords, maxKeywords)
	require.Len(t, opts.Context, 3)
	assert.Equal(t, "middle message", opts.Context[1])
	assert.Equal(t, "newest", opts.Context[2])
	total := 0
	for _, text := range opts.Context {
		total += len([]rune(text))
	}
	assert.LessOrEqual(t, total, maxContextRunes)
	assert.Equal(t, "ref", opts.UserRef)
}

func TestJoinTextGivenMixedScriptsThenSpacesOnlySeparateLatinWords(t *testing.T) {
	assert.Equal(t, "Hello. World", joinText("Hello.", "World"))
	assert.Equal(t, "Hello,", joinText("Hello", ","))
	assert.Equal(t, "\u4f60\u597d\u4e16\u754c", joinText("\u4f60\u597d", "\u4e16\u754c"))
	assert.Equal(t, "\u8fd0\u884cpaxd", joinText("\u8fd0\u884c", "paxd"))
	assert.Equal(t, "a b", joinText("a", "", "b"))
	assert.Equal(t, "Done. ok", Snapshot{Final: "Done.", Partial: "ok"}.Text())
}

func TestSnapshotPipeGivenSlowConsumerThenLatestSnapshotWins(t *testing.T) {
	pipe := newSnapshotPipe()
	pipe.publish(Snapshot{Partial: "a"})
	pipe.publish(Snapshot{Final: "ab"})
	pipe.finish(nil)
	var got []Snapshot
	for snapshot := range pipe.ch {
		got = append(got, snapshot)
	}
	assert.Equal(t, []Snapshot{{Final: "ab"}}, got)
	assert.NoError(t, pipe.err)
}

func TestNewRequestIDGivenCallsThenUUIDv4IsUnique(t *testing.T) {
	first, second := newRequestID(), newRequestID()
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, first)
	assert.NotEqual(t, first, second)
}
