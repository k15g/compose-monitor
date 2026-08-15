package domain

import "time"

// Subnet is one IP range a network hands addresses out of.
type Subnet struct {
	Range   string
	Gateway string
}

// NetworkMember is a container attached to a network.
type NetworkMember struct {
	ContainerID string
	Name        string
	IPv4Address string
	IPv6Address string
	MacAddress  string
}

// Network is a Docker network belonging to the project.
type Network struct {
	ID         string
	Name       string
	Driver     string
	Scope      string
	Created    time.Time
	Internal   bool
	Attachable bool
	IPv6       bool
	Subnets    []Subnet
	Labels     []Label
	Options    []Label
	Members    []NetworkMember

	// UsedBy is how many of the project's containers are attached. It is
	// counted from the containers rather than read off the network, because
	// listing networks does not report their members at all.
	UsedBy int
}

// ShortID is the abbreviated network ID, the length the CLI shows.
func (n Network) ShortID() string {
	if len(n.ID) > 12 {
		return n.ID[:12]
	}
	return n.ID
}

// UsedBy is how many of the project's containers are attached to the network,
// counting stopped ones.
//
// Stopped containers are counted deliberately. The runtime will often let a
// network with only stopped members be removed, but "often" is not something a
// button should be built on: offering a remove that fails is worse than
// withholding one that would have worked, and the container is still
// configured to join it.
func (n Network) InUse() bool {
	return n.UsedBy > 0
}
