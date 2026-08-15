package domain

import "time"

// LogStream is which of a container's two output streams a line came from.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
)

// LogLine is one line of a container's output.
type LogLine struct {
	// At is when the runtime recorded the line. It is zero when the line
	// carried no parseable timestamp.
	At time.Time
	// Stream is which output the line came from.
	Stream LogStream
	// Text is the line itself, without the timestamp the runtime prefixes.
	Text string
}

// Logs is a container's recent output, and how much of it was asked for.
type Logs struct {
	Lines []LogLine
	// Tail is how many lines were requested, so the page can say what it is
	// showing rather than implying it is everything.
	Tail int
	// Truncated is whether the runtime had at least as many lines as were
	// asked for, meaning there is probably more above.
	Truncated bool
}
