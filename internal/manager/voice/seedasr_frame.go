package voice

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Seed ASR frames start with a 4-byte header:
//
//	byte 0: protocol version (high nibble), header size in 4-byte words (low)
//	byte 1: message type (high nibble), message type flags (low)
//	byte 2: serialization (high nibble), compression (low)
//	byte 3: reserved
//
// Client frames then carry a big-endian int32 sequence, a big-endian uint32
// payload size, and the gzip compressed payload. The last audio frame uses a
// negated sequence.
const (
	seedProtocolVersion = 0b0001
	seedHeaderWords     = 0b0001

	seedMsgFullClientRequest  = 0b0001
	seedMsgAudioOnlyRequest   = 0b0010
	seedMsgFullServerResponse = 0b1001
	seedMsgServerError        = 0b1111

	seedFlagSequence     = 0b0001
	seedFlagLast         = 0b0010
	seedFlagLastSequence = seedFlagSequence | seedFlagLast
	seedFlagEvent        = 0b0100

	seedSerializationJSON = 0b0001
	seedCompressionGzip   = 0b0001

	maxSeedPayloadBytes = 4 << 20
)

type seedFrame struct {
	messageType byte
	flags       byte
	sequence    int32
	event       int32
	errorCode   int32
	payload     []byte
}

func (f seedFrame) last() bool {
	return f.flags&seedFlagLast != 0
}

func encodeSeedFrame(messageType, flags byte, sequence int32, payload []byte) ([]byte, error) {
	compressed, err := gzipBytes(payload)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, 12+len(compressed))
	frame = append(
		frame,
		seedProtocolVersion<<4|seedHeaderWords,
		messageType<<4|flags,
		seedSerializationJSON<<4|seedCompressionGzip,
		0,
	)
	frame = binary.BigEndian.AppendUint32(frame, uint32(sequence))
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(compressed)))
	return append(frame, compressed...), nil
}

func decodeSeedFrame(data []byte) (seedFrame, error) {
	if len(data) < 4 {
		return seedFrame{}, errors.New("seed asr frame shorter than its header")
	}
	headerBytes := int(data[0]&0x0f) * 4
	if headerBytes < 4 || len(data) < headerBytes {
		return seedFrame{}, errors.New("seed asr frame has an invalid header size")
	}
	frame := seedFrame{messageType: data[1] >> 4, flags: data[1] & 0x0f}
	compression := data[2] & 0x0f
	rest := data[headerBytes:]
	var err error
	if frame.flags&seedFlagSequence != 0 {
		var value uint32
		if value, rest, err = readUint32(rest); err != nil {
			return seedFrame{}, err
		}
		frame.sequence = int32(value)
	}
	if frame.flags&seedFlagEvent != 0 {
		var value uint32
		if value, rest, err = readUint32(rest); err != nil {
			return seedFrame{}, err
		}
		frame.event = int32(value)
	}
	switch frame.messageType {
	case seedMsgFullServerResponse:
	case seedMsgServerError:
		var code uint32
		if code, rest, err = readUint32(rest); err != nil {
			return seedFrame{}, err
		}
		frame.errorCode = int32(code)
	default:
		return seedFrame{}, fmt.Errorf("unexpected seed asr message type %d", frame.messageType)
	}
	size, rest, err := readUint32(rest)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) && len(rest) == 0 {
			return frame, nil
		}
		return seedFrame{}, err
	}
	if size > maxSeedPayloadBytes || int(size) > len(rest) {
		return seedFrame{}, errors.New("seed asr frame payload is truncated or too large")
	}
	frame.payload = rest[:size]
	if compression == seedCompressionGzip && len(frame.payload) > 0 {
		if frame.payload, err = gunzipBytes(frame.payload); err != nil {
			return seedFrame{}, fmt.Errorf("decompress seed asr payload: %w", err)
		}
	}
	return frame, nil
}

func readUint32(data []byte) (uint32, []byte, error) {
	if len(data) < 4 {
		return 0, data, io.ErrUnexpectedEOF
	}
	return binary.BigEndian.Uint32(data[:4]), data[4:], nil
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(io.LimitReader(reader, maxSeedPayloadBytes))
}
