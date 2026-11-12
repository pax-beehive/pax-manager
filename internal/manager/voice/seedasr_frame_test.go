package voice

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientFrame is decoded independently from the production encoder so the
// tests pin the wire layout from the vendor demo.
type clientFrame struct {
	header   [4]byte
	sequence int32
	payload  []byte
}

func decodeClientFrame(t *testing.T, data []byte) clientFrame {
	t.Helper()
	require.GreaterOrEqual(t, len(data), 12)
	var frame clientFrame
	copy(frame.header[:], data[:4])
	frame.sequence = int32(binary.BigEndian.Uint32(data[4:8]))
	size := binary.BigEndian.Uint32(data[8:12])
	require.Equal(t, int(size), len(data)-12)
	payload, err := gunzipBytes(data[12:])
	require.NoError(t, err)
	frame.payload = payload
	return frame
}

func serverFrame(t *testing.T, flags byte, sequence int32, payload string) []byte {
	t.Helper()
	compressed, err := gzipBytes([]byte(payload))
	require.NoError(t, err)
	frame := []byte{0x11, seedMsgFullServerResponse<<4 | flags, 0x11, 0x00}
	if flags&seedFlagSequence != 0 {
		frame = binary.BigEndian.AppendUint32(frame, uint32(sequence))
	}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(compressed)))
	return append(frame, compressed...)
}

func serverErrorFrame(t *testing.T, code int32, message string) []byte {
	t.Helper()
	compressed, err := gzipBytes([]byte(message))
	require.NoError(t, err)
	frame := []byte{0x11, seedMsgServerError << 4, 0x11, 0x00}
	frame = binary.BigEndian.AppendUint32(frame, uint32(code))
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(compressed)))
	return append(frame, compressed...)
}

func TestEncodeSeedFrameGivenClientMessagesThenHeaderMatchesVendorLayout(t *testing.T) {
	data, err := encodeSeedFrame(seedMsgFullClientRequest, seedFlagSequence, 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	frame := decodeClientFrame(t, data)
	assert.Equal(t, [4]byte{0x11, 0x11, 0x11, 0x00}, frame.header)
	assert.Equal(t, int32(1), frame.sequence)
	assert.JSONEq(t, `{"a":1}`, string(frame.payload))

	data, err = encodeSeedFrame(seedMsgAudioOnlyRequest, seedFlagLastSequence, -7, []byte{1, 2})
	require.NoError(t, err)
	frame = decodeClientFrame(t, data)
	assert.Equal(t, [4]byte{0x11, 0x23, 0x11, 0x00}, frame.header)
	assert.Equal(t, int32(-7), frame.sequence)
	assert.Equal(t, []byte{1, 2}, frame.payload)
}

func TestDecodeSeedFrameGivenServerResponsesThenPayloadAndFlagsAreRead(t *testing.T) {
	frame, err := decodeSeedFrame(serverFrame(t, seedFlagSequence, 3, `{"result":{}}`))
	require.NoError(t, err)
	assert.Equal(t, byte(seedMsgFullServerResponse), frame.messageType)
	assert.Equal(t, int32(3), frame.sequence)
	assert.False(t, frame.last())
	assert.JSONEq(t, `{"result":{}}`, string(frame.payload))

	frame, err = decodeSeedFrame(serverFrame(t, seedFlagLastSequence, -4, `{}`))
	require.NoError(t, err)
	assert.True(t, frame.last())
	assert.Equal(t, int32(-4), frame.sequence)

	frame, err = decodeSeedFrame(serverErrorFrame(t, 45000001, "invalid request"))
	require.NoError(t, err)
	assert.Equal(t, byte(seedMsgServerError), frame.messageType)
	assert.Equal(t, int32(45000001), frame.errorCode)
	assert.Equal(t, "invalid request", string(frame.payload))
}

func TestDecodeSeedFrameGivenMalformedDataThenItFails(t *testing.T) {
	valid := serverFrame(t, seedFlagSequence, 1, `{"result":{}}`)
	for name, data := range map[string][]byte{
		"short header":      {0x11, 0x91},
		"zero header size":  {0x10, 0x91, 0x11, 0x00, 0, 0, 0, 0},
		"truncated payload": valid[:len(valid)-3],
		"client type":       {0x11, 0x11, 0x11, 0x00, 0, 0, 0, 1, 0, 0, 0, 0},
	} {
		_, err := decodeSeedFrame(data)
		assert.Error(t, err, name)
	}
}
