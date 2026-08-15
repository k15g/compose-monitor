package docker

import (
	"regexp"
	"strings"
)

// elapsed matches the durations the runtime renders into a status line.
//
// The set is exactly what the runtime's own humaniser produces: "Less than a
// second", "1 second", "N seconds", "About a minute", "N minutes", "About an
// hour", "N hours", and N days/weeks/months/years. A trailing " ago" belongs to
// the duration and goes with it.
var elapsed = regexp.MustCompile(
	`(Less than a second|About an hour|About a minute|\d+ (?:seconds?|minutes?|hours?|days?|weeks?|months?|years?))( ago)?`,
)

// statusElapsed is the elapsed time in a status line, and nothing else.
//
// It is the complement of statusKind: between them they split "Exited (0) 2
// hours ago" into the part that says what happened and the part that says when.
// A listing showing the state as its own badge wants only the second, and a
// status with no time in it — "Created", "Dead" — has none to give.
func statusElapsed(status string) string {
	return elapsed.FindString(status)
}

// statusKind is a status line with the elapsed time removed.
//
// "Up 3 minutes" and "Up 4 minutes" both become "Up", so a container nothing
// has happened to compares equal between two reads. What is left is the part
// that carries meaning — "Exited (0)" stays distinct from "Exited (137)", and
// a status with no duration in it, like "Created", is returned unchanged.
func statusKind(status string) string {
	return strings.Join(strings.Fields(elapsed.ReplaceAllString(status, "")), " ")
}
