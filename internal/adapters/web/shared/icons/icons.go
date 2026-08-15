// Package icons holds the small marks that sit inside badges.
//
// They are files rather than markup buried in a template, so they can be
// opened and edited as drawings — and they are inlined into the page rather
// than linked, so each one takes the colour of the badge it is in. An <img>
// cannot do that: it renders in its own document and knows nothing of the text
// beside it.
package icons

import (
	"embed"
	"fmt"

	"github.com/a-h/templ"
)

//go:embed files/*.svg
var files embed.FS

// Name identifies one mark.
type Name string

const (
	// Container marks the container's own id.
	Container Name = "container"
	// Clock marks how long a service has been up.
	Clock Name = "clock"
	// Pulse marks the outcome of a healthcheck.
	Pulse Name = "pulse"
	// Layers marks the image a container was created from.
	Layers Name = "layers"
	// Port marks a port the container answers on.
	Port Name = "port"
	// External marks a link that leaves this page.
	External Name = "external"
	// State marks whether a container is up.
	State Name = "state"
	// Network marks a network.
	Network Name = "network"
	// Volume marks a volume.
	Volume Name = "volume"
	// Folder marks a path on the host.
	Folder Name = "folder"
	// Globe marks an address range.
	Globe Name = "globe"
	// Gear marks a driver or an option.
	Gear Name = "gear"
	// Play marks starting a container.
	Play Name = "play"
	// Stop marks stopping one.
	Stop Name = "stop"
	// Trash marks removing one.
	Trash Name = "trash"
)

// svg holds each mark's markup, read once at startup.
var svg = map[Name]string{}

func init() {
	for _, name := range []Name{Container, Clock, Pulse, Layers, Port, External, State, Network, Volume, Folder, Globe, Gear, Play, Stop, Trash} {
		content, err := files.ReadFile(fmt.Sprintf("files/%s.svg", name))
		if err != nil {
			// The files are embedded at compile time, so a missing one is a
			// build that should not have been produced rather than a runtime
			// condition to handle.
			panic(fmt.Sprintf("icons: %s is missing from the embedded set: %v", name, err))
		}
		svg[name] = string(content)
	}
}

// SVG returns one mark's markup, ready to be written into a page.
//
// It is trusted rather than escaped because it is embedded from this repository
// at compile time — there is no path by which a container, a label or a request
// can reach it.
func SVG(name Name) templ.Component {
	return templ.Raw(svg[name])
}
