// Package docker reads services from a container runtime over its local
// socket. It reports what it sees and nothing more: classifying, comparing and
// ordering are the application layer's job.
package docker

// The labels Compose writes onto every container it creates. They are the
// whole of the join between "a project" and "the containers running it" —
// there is no other record of a container's project on the daemon.
const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelNumber  = "com.docker.compose.container-number"
)
