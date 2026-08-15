package domain

import "time"

// Volume is a Docker volume belonging to the project.
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	Scope      string
	Created    time.Time
	Labels     []Label
	Options    []Label

	// Size is the volume's size on disk in bytes, only populated when the
	// runtime was asked to measure it, and -1 when it was not.
	Size int64

	// UsedBy is how many of the project's containers mount the volume,
	// counting stopped ones — which is what the runtime itself refuses a
	// removal on.
	UsedBy int
}

// InUse reports whether any container still mounts the volume.
func (v Volume) InUse() bool {
	return v.UsedBy > 0
}
