package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceURL(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name: "the common case",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`app.example.com`)",
			},
			want: "http://app.example.com",
		},
		{
			name: "terminating TLS makes it https",
			labels: map[string]string{
				"traefik.http.routers.web.rule":             "Host(`app.example.com`)",
				"traefik.http.routers.web.tls.certresolver": "letsencrypt",
			},
			want: "https://app.example.com",
		},
		{
			name: "tls on its own is enough",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`app.example.com`)",
				"traefik.http.routers.web.tls":  "true",
			},
			want: "https://app.example.com",
		},
		{
			name: "a rule with more than a host in it",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`app.example.com`) && PathPrefix(`/api`)",
			},
			want: "http://app.example.com",
		},
		{
			// A service answering on several names answers the same thing on
			// each, so the page offers the first.
			name: "several hosts in one rule",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`first.example.com`, `second.example.com`)",
			},
			want: "http://first.example.com",
		},
		{
			name: "hosts joined with or",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`first.example.com`) || Host(`second.example.com`)",
			},
			want: "http://first.example.com",
		},
		{
			// Two routers, and the labels come back in whatever order the
			// daemon felt like. Taking them in name order is what stops the
			// link changing between two reads of the same container.
			name: "several routers",
			labels: map[string]string{
				"traefik.http.routers.zulu.rule":  "Host(`zulu.example.com`)",
				"traefik.http.routers.alpha.rule": "Host(`alpha.example.com`)",
			},
			want: "http://alpha.example.com",
		},
		{
			name: "a router that does not route on a host",
			labels: map[string]string{
				"traefik.http.routers.api.rule": "PathPrefix(`/api`)",
				"traefik.http.routers.web.rule": "Host(`app.example.com`)",
			},
			want: "http://app.example.com",
		},
		{
			name: "a host with a port",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`app.example.com:8443`)",
			},
			want: "http://app.example.com:8443",
		},
		{
			name:   "no traefik labels at all",
			labels: map[string]string{"com.docker.compose.project": "example"},
			want:   "",
		},
		{
			name:   "nothing",
			labels: nil,
			want:   "",
		},
		{
			// These labels are set by whoever created the container. A link is
			// not the place to discover one of them held something else.
			name: "a host that is not a hostname",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`javascript:alert(1)`)",
			},
			want: "",
		},
		{
			name: "a host with a path glued to it",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(`example.com/../evil`)",
			},
			want: "",
		},
		{
			name: "an empty host",
			labels: map[string]string{
				"traefik.http.routers.web.rule": "Host(``)",
			},
			want: "",
		},
		{
			name: "a tcp router, which routes on SNI rather than a host header",
			labels: map[string]string{
				"traefik.tcp.routers.db.rule": "HostSNI(`db.example.com`)",
			},
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, serviceURL(test.labels))
		})
	}
}
