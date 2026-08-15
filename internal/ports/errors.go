package ports

import "errors"

// ErrSourceUnavailable is returned by a ContainerSource that cannot reach the
// container runtime at all — a missing socket, or one it may not read.
var ErrSourceUnavailable = errors.New("container source unavailable")

// ErrNotFound is returned when the thing asked for is not one of the
// project's. It deliberately does not distinguish "does not exist" from
// "exists but is not ours": the caller takes ids from HTTP requests, and
// telling those apart would let it probe what else is on the host.
var ErrNotFound = errors.New("not found in this project")
