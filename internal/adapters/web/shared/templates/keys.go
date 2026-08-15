package templates

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/k15g/compose-monitor/internal/domain"
)

// SortKey is the row's position in display order, as a string the browser can
// compare. It mirrors app.SortServices so a row inserted by an event lands
// where a full render would have put it.
//
// The number is zero-padded because the comparison is lexical: without it,
// replica 10 would sort before replica 2.
func SortKey(service domain.Service) string {
	return fmt.Sprintf("%s\x1f%06d\x1f%s", service.Name, service.Number, service.ContainerID)
}

// ShortID is the abbreviated container ID, the length the CLI shows.
func ShortID(service domain.Service) string {
	if len(service.ContainerID) > 12 {
		return service.ContainerID[:12]
	}
	return service.ContainerID
}

// StateLabel is the state as the page words it.
func StateLabel(service domain.Service) string {
	if service.State == domain.StateUnknown {
		return "unknown"
	}
	return string(service.State)
}

// OnlineAttr renders the online flag for the row's data attribute, which is
// what the client counts to keep the header's totals right.
func OnlineAttr(service domain.Service) string {
	return strconv.FormatBool(service.Online())
}

// ReplicaLabel names the replica of a scaled service, and is empty for a
// service running a single container — the common case, where a "#1" on every
// row would be noise.
func ReplicaLabel(service domain.Service) string {
	if service.Number <= 1 {
		return ""
	}
	return "#" + strconv.Itoa(service.Number)
}

// PortLabel renders one port the way the CLI does: "8080→80/tcp" when it is
// published to the host, and just "80/tcp" when it is only reachable from
// inside the project's network.
func PortLabel(port domain.Port) string {
	if port.Published() {
		return fmt.Sprintf("%d→%d/%s", port.Host, port.Container, port.Protocol)
	}
	return fmt.Sprintf("%d/%s", port.Container, port.Protocol)
}

// StatusText is the runtime's status line with the health suffix taken off:
// health is drawn as its own badge, and showing it twice reads as a mistake.
func StatusText(service domain.Service) string {
	status := service.Status
	for _, suffix := range []string{" (healthy)", " (unhealthy)", " (health: starting)"} {
		status = strings.TrimSuffix(status, suffix)
	}
	if status == "" {
		return "—"
	}
	return status
}

// FormatTime renders a timestamp for the page, or an em dash when there is
// none — a container that has never run has no start time, and printing the
// zero time as a date in year 1 reads as a bug.
func FormatTime(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	return at.Format("2006-01-02 15:04:05 MST")
}

// FormatBool renders a flag as a word rather than "true"/"false", which reads
// better in a table of properties.
func FormatBool(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// Or returns value, or a dash when it is empty, so an absent property lines up
// with every other absent property.
func Or(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// FormatSize renders a byte count. A negative size means the runtime was not
// asked to measure it, which is not the same as a volume being empty.
func FormatSize(bytes int64) string {
	if bytes < 0 {
		return "not measured"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatCount renders a reference count, with the same "not measured" caveat
// as FormatSize.
func FormatCount(count int64) string {
	if count < 0 {
		return "not measured"
	}
	return strconv.FormatInt(count, 10)
}

// JoinCommand renders a container's command for display.
func JoinCommand(parts []string) string {
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// ShortContainerID abbreviates a bare container id.
func ShortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// ExitCodeLabel renders a container's exit code, or empty when there is none
// worth showing.
//
// Three cases give nothing: a running container, whose exit code is left over
// from its last run; one that has never finished, whose zero means nothing;
// and one that exited cleanly, which says so by being exited.
func ExitCodeLabel(detail domain.ServiceDetail) string {
	if detail.Online() || detail.FinishedAt.IsZero() || detail.ExitCode == 0 {
		return ""
	}
	return strconv.Itoa(detail.ExitCode)
}

// FormatInt renders a plain number.
func FormatInt(value int) string {
	return strconv.Itoa(value)
}

// LogSizeURL is the link that re-renders a service's detail page with a
// different amount of log. It is also how the log is refreshed, since the page
// is a snapshot rather than a live tail.
func LogSizeURL(containerID string, tail int) templ.SafeURL {
	return templ.SafeURL("/services/" + containerID + "?tail=" + strconv.Itoa(tail))
}

// FormatLogTime renders a log line's timestamp, or blanks it when the line
// carried none — an unparseable prefix means the line is shown whole, and a
// zero time next to it would be a lie.
func FormatLogTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format("15:04:05.000")
}

// UsedByLabel says how many containers refer to a network or volume, in words
// rather than a bare number — "1 container" reads as a fact, "1" reads as an
// id.
func UsedByLabel(count int) string {
	if count == 1 {
		return "1 container"
	}
	return strconv.Itoa(count) + " containers"
}

// InUseLabel renders a usage count as a sentence for a properties row.
func InUseLabel(count int) string {
	if count == 0 {
		return "no — nothing refers to it"
	}
	return UsedByLabel(count)
}

// Crumb is one step of a breadcrumb trail.
//
// The last crumb is the page itself and carries no Href: a link to where you
// already are is noise, and marking it instead is what tells a screen reader
// which step is current.
type Crumb struct {
	Label string
	Href  string
}

// Trail builds a breadcrumb trail from alternating label/href pairs, with a
// final label for the page itself.
//
// The project is the root, linking to the front page. It repeats the name in
// the global header, deliberately: a trail that starts halfway down says where
// a page sits relative to a root it never shows, and the root is the one step
// every page shares.
func Trail(project string, section string, sectionHref string, rest ...string) []Crumb {
	crumbs := []Crumb{
		{Label: project, Href: "/"},
		{Label: section, Href: sectionHref},
	}

	// Everything after the section is a label/href pair, until a final label
	// with no href — the page itself.
	for len(rest) >= 2 {
		crumbs = append(crumbs, Crumb{Label: rest[0], Href: rest[1]})
		rest = rest[2:]
	}
	if len(rest) == 1 {
		crumbs = append(crumbs, Crumb{Label: rest[0]})
	}

	// Whatever the trail turned out to be, its last step is where the reader
	// already is. Dropping the href here rather than at each call site makes
	// that true even for a trail that is only a section.
	crumbs[len(crumbs)-1].Href = ""
	return crumbs
}

// digestLength is how much of a digest to show, matching the twelve characters
// the CLI abbreviates a container id to.
const digestLength = 12

// ShortImage abbreviates the digest in an image reference.
//
// A reference pinned by digest carries 64 hex characters that no one reads and
// that push everything else out of the column:
//
//	ghcr.io/example/thing@sha256:1010acc839eccd5694743efd676ada2ff40e0…
//	ghcr.io/example/thing@sha256:1010acc839ec
//
// The name is what identifies the image to a person, so it is kept whole; only
// the digest is cut. A reference with a tag and no digest is already short and
// is returned unchanged. The full reference goes in the cell's title, so it is
// still there to be read.
func ShortImage(image string) string {
	name, digest, found := strings.Cut(image, "@")
	if !found {
		return image
	}

	algorithm, hex, valid := strings.Cut(digest, ":")
	if !valid || len(hex) <= digestLength {
		return image
	}

	return name + "@" + algorithm + ":" + hex[:digestLength]
}

// UptimeLabel is how long a service has been in the state it is in: "2 hours"
// for one that is up, "2 hours ago" for one that stopped.
//
// The state itself is a badge of its own, so repeating it here — "Up", or
// "Exited (0)" — would say it twice. What is left when a status has no time in
// it at all, such as "Created", is the status itself, because a badge with
// nothing in it says less than one saying "Created".
func UptimeLabel(service domain.Service) string {
	if service.Elapsed != "" {
		return service.Elapsed
	}
	return StatusText(service)
}

// PortClass is the badge class for a port, marking the published ones — being
// reachable from the host is the one thing about a port worth noticing at a
// glance.
func PortClass(port domain.Port) string {
	if port.Published() {
		return "badge-port badge-port-published"
	}
	return "badge-port"
}

// PortTitle says in words what the badge says in symbols.
func PortTitle(port domain.Port) string {
	if port.Published() {
		return fmt.Sprintf("Published on the host: %d → %d/%s", port.Host, port.Container, port.Protocol)
	}
	return fmt.Sprintf("Reachable only from the project's networks: %d/%s", port.Container, port.Protocol)
}

// UsageLabel says how many containers refer to a network or volume, in words
// rather than a bare number — "1 container" reads as a fact, "1" reads as an
// id. Nothing referring to it is the interesting case, so it says so.
func UsageLabel(count int) string {
	switch count {
	case 0:
		return "unused"
	case 1:
		return "1 container"
	default:
		return strconv.Itoa(count) + " containers"
	}
}

// UsageClass marks the unused ones, which are the ones that can be removed.
func UsageClass(count int) string {
	if count == 0 {
		return "badge-unused"
	}
	return "badge-container"
}
