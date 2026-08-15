package docker

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/domain"
)

// frame builds one of the runtime's log frames: an 8-byte header carrying the
// stream and the payload length, then the payload.
func frame(stream byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

func TestReadMultiplexedLogKeepsStreamsInOrder(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frame(1, "2026-08-14T10:00:00.000000000Z starting up\n"))
	stream.Write(frame(2, "2026-08-14T10:00:01.000000000Z something went wrong\n"))
	stream.Write(frame(1, "2026-08-14T10:00:02.000000000Z carrying on\n"))

	lines, err := readMultiplexedLog(&stream)
	require.NoError(t, err)
	require.Len(t, lines, 3)

	// Both outputs share one connection, and decoding the frames in order is
	// what keeps them interleaved as they actually happened. StdCopy would
	// split them into two buffers and lose exactly this.
	assert.Equal(t, []domain.LogStream{
		domain.LogStreamStdout, domain.LogStreamStderr, domain.LogStreamStdout,
	}, []domain.LogStream{lines[0].Stream, lines[1].Stream, lines[2].Stream})

	assert.Equal(t, "starting up", lines[0].Text)
	assert.Equal(t, "something went wrong", lines[1].Text)
	assert.Equal(t, 2026, lines[0].At.Year())
}

func TestReadMultiplexedLogSplitsAFrameCarryingSeveralLines(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frame(1, "2026-08-14T10:00:00Z one\n2026-08-14T10:00:01Z two\n2026-08-14T10:00:02Z three\n"))

	lines, err := readMultiplexedLog(&stream)

	require.NoError(t, err)
	require.Len(t, lines, 3, "a frame is not guaranteed to be exactly one line")
	assert.Equal(t, []string{"one", "two", "three"}, []string{lines[0].Text, lines[1].Text, lines[2].Text})
}

func TestReadMultiplexedLogOfNothing(t *testing.T) {
	lines, err := readMultiplexedLog(bytes.NewReader(nil))

	require.NoError(t, err)
	assert.Empty(t, lines)
}

func TestReadMultiplexedLogSurvivesATruncatedStream(t *testing.T) {
	// The daemon can close mid-frame. Returning what was decoded beats losing
	// the whole log to the last partial line.
	full := frame(1, "2026-08-14T10:00:00Z complete\n")
	partial := frame(1, "2026-08-14T10:00:01Z incomplete\n")

	lines, err := readMultiplexedLog(bytes.NewReader(append(full, partial[:12]...)))

	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, "complete", lines[0].Text)

	// The fragment that did arrive is kept, but only the bytes that arrived:
	// the rest of the frame's buffer is zeroes, and emitting those would put a
	// line of NULs in the log.
	assert.Equal(t, "2026", lines[1].Text)
	assert.NotContains(t, lines[1].Text, "\x00")
}

func TestReadTTYLogHasNoFraming(t *testing.T) {
	// A container with a TTY merged its outputs before the runtime saw them,
	// so there is no framing and no way to tell stdout from stderr.
	raw := bytes.NewBufferString("2026-08-14T10:00:00Z one\n2026-08-14T10:00:01Z two\n")

	lines, err := readTTYLog(raw)

	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, domain.LogStreamStdout, lines[0].Stream)
	assert.Equal(t, domain.LogStreamStdout, lines[1].Stream)
}

func TestSplitTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantText string
		wantTime bool
	}{
		{"a normal line", "2026-08-14T10:00:00.5Z hello world", "hello world", true},
		{"a line with no timestamp", "hello world", "hello world", false},
		{"a single word", "hello", "hello", false},
		{"an unparseable prefix", "notatime hello", "notatime hello", false},
		{"an empty message", "2026-08-14T10:00:00Z ", "", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			at, text := splitTimestamp(test.line)
			assert.Equal(t, test.wantText, text,
				"a line that is not shaped as expected is shown whole rather than dropped")
			assert.Equal(t, test.wantTime, !at.IsZero())
		})
	}
}

func TestStreamOf(t *testing.T) {
	assert.Equal(t, domain.LogStreamStdout, streamOf(1))
	assert.Equal(t, domain.LogStreamStderr, streamOf(2))
	assert.Equal(t, domain.LogStreamStdout, streamOf(0), "stdin frames are not stderr")
}
