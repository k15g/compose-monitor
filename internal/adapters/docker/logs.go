package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Logs returns the tail of a container's output.
//
// The container is inspected first, for two reasons: it is where the project
// label is checked — the logs endpoint, like inspect, will happily serve any
// container the daemon knows — and it is the only way to learn whether the
// container has a TTY, which decides how the stream is framed.
func (c *Client) Logs(ctx context.Context, containerID string, tail int) (domain.Logs, error) {
	inspected, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return domain.Logs{}, fmt.Errorf("%w: container %s", ports.ErrNotFound, containerID)
		}
		return domain.Logs{}, fmt.Errorf("inspecting container %s: %w", containerID, err)
	}
	if inspected.Config == nil || inspected.Config.Labels[labelProject] != c.project {
		return domain.Logs{}, fmt.Errorf("%w: container %s", ports.ErrNotFound, containerID)
	}

	stream, err := c.api.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return domain.Logs{}, fmt.Errorf("reading logs of container %s: %w", containerID, err)
	}
	defer func() { _ = stream.Close() }()

	var lines []domain.LogLine
	if inspected.Config.Tty {
		lines, err = readTTYLog(stream)
	} else {
		lines, err = readMultiplexedLog(stream)
	}
	if err != nil {
		return domain.Logs{}, fmt.Errorf("decoding logs of container %s: %w", containerID, err)
	}

	return domain.Logs{
		Lines: lines,
		Tail:  tail,
		// The runtime returned exactly what was asked for, so there is very
		// likely more above. It cannot say how much without reading it all.
		Truncated: len(lines) >= tail,
	}, nil
}

// readMultiplexedLog decodes the framed stream a container without a TTY
// produces.
//
// Each frame is an 8-byte header — stream type, three padding bytes, then a
// big-endian payload length — followed by the payload. Both outputs share the
// one connection, so decoding the frames in order is what keeps stdout and
// stderr interleaved as they actually happened. The SDK's StdCopy would split
// them into two writers, which loses exactly that.
func readMultiplexedLog(stream io.Reader) ([]domain.LogLine, error) {
	const headerSize = 8

	reader := bufio.NewReader(stream)
	header := make([]byte, headerSize)
	var lines []domain.LogLine

	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return lines, nil
			}
			return lines, err
		}

		size := binary.BigEndian.Uint32(header[4:])
		if size == 0 {
			continue
		}

		payload := make([]byte, size)
		read, err := io.ReadFull(reader, payload)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// The daemon closed mid-frame. Only the bytes that actually
				// arrived are output — the rest of the buffer is zeroes, and
				// emitting those would append a line of NULs to the log.
				return append(lines, splitLines(string(payload[:read]), streamOf(header[0]))...), nil
			}
			return lines, err
		}

		lines = append(lines, splitLines(string(payload), streamOf(header[0]))...)
	}
}

// readTTYLog decodes the raw stream a container with a TTY produces. There is
// no framing, and no way to tell stdout from stderr — the TTY merged them
// before the runtime saw them — so every line is reported as stdout.
func readTTYLog(stream io.Reader) ([]domain.LogLine, error) {
	payload, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	return splitLines(string(payload), domain.LogStreamStdout), nil
}

// streamOf maps a frame's stream byte. Anything that is not stderr is treated
// as stdout, including stdin frames, which a log stream should never carry.
func streamOf(kind byte) domain.LogStream {
	if kind == 2 {
		return domain.LogStreamStderr
	}
	return domain.LogStreamStdout
}

// splitLines turns a payload into lines, pulling off the timestamp the runtime
// prefixes each one with.
//
// A frame is not guaranteed to be one line — it can carry several, or a
// partial one — so the payload is split rather than taken whole.
func splitLines(payload string, stream domain.LogStream) []domain.LogLine {
	payload = strings.TrimSuffix(payload, "\n")
	if payload == "" {
		return nil
	}

	parts := strings.Split(payload, "\n")
	lines := make([]domain.LogLine, 0, len(parts))
	for _, part := range parts {
		at, text := splitTimestamp(part)
		lines = append(lines, domain.LogLine{At: at, Stream: stream, Text: text})
	}
	return lines
}

// splitTimestamp separates the RFC 3339 timestamp the runtime prefixes from
// the line itself. A line that does not start with one is returned whole, with
// a zero time: the alternative is dropping output because it was not shaped as
// expected.
func splitTimestamp(line string) (time.Time, string) {
	stamp, rest, found := strings.Cut(line, " ")
	if !found {
		return time.Time{}, line
	}

	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, line
	}
	return at.UTC(), rest
}
