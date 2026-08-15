package docker

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// Traefik's routing lives in labels, one router per name:
//
//	traefik.http.routers.web.rule=Host(`app.example.com`)
//	traefik.http.routers.web.tls.certresolver=letsencrypt
//
// The rule is a small expression language; only the first host in it is of
// interest here, because the page offers one link and a service answering on
// several names answers the same thing on each.
var (
	routerRule = regexp.MustCompile(`^traefik\.http\.routers\.([^.]+)\.rule$`)
	hostMatch  = regexp.MustCompile("Host\\(\\s*`([^`]+)`")

	// A hostname the page will put in a link. Anything else is left alone
	// rather than guessed at: these labels are set by whoever created the
	// container, and a link is not the place to find out they contained
	// something unexpected.
	hostname = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
)

// serviceURL finds where a service answers on the web.
//
// A container may declare several routers, and the usual reason is one serving
// http only to redirect to another serving https. A secure router is therefore
// preferred over an insecure one, whatever they are called; among equals they
// are taken in name order rather than in whatever order the labels came back
// in, so the same container always produces the same link.
func serviceURL(labels map[string]string) string {
	routers := make([]string, 0, len(labels))
	for key := range labels {
		if match := routerRule.FindStringSubmatch(key); match != nil {
			routers = append(routers, match[1])
		}
	}
	slices.SortFunc(routers, func(a, b string) int { return cmp.Compare(a, b) })

	insecure := ""
	for _, router := range routers {
		host := ruleHost(labels["traefik.http.routers."+router+".rule"])
		if host == "" {
			continue
		}

		if secure(labels, router) {
			return "https://" + host
		}
		if insecure == "" {
			insecure = "http://" + host
		}
	}
	return insecure
}

// ruleHost is the first host a routing rule matches on, or empty when it
// matches on something else — a path, a header — which names nothing that can
// be opened.
func ruleHost(rule string) string {
	match := hostMatch.FindStringSubmatch(rule)
	if match == nil {
		return ""
	}

	host := match[1]
	if !hostname.MatchString(host) {
		return ""
	}
	return host
}

// secure reports whether a router serves over TLS.
//
// Two things say so, and either is enough:
//
//   - a tls setting of any kind, which is what turns TLS on for a router;
//   - an entrypoint whose name says it, which is the convention rather than a
//     rule — Traefik lets an entrypoint be called anything, and only the static
//     configuration this service cannot see knows which port it listens on. The
//     names below are what the documentation uses and what compose files
//     overwhelmingly copy.
func secure(labels map[string]string, router string) bool {
	prefix := "traefik.http.routers." + router
	for key, value := range labels {
		switch {
		case key == prefix+".tls" || strings.HasPrefix(key, prefix+".tls."):
			return true
		case key == prefix+".entrypoints" || key == prefix+".entryPoints":
			for _, entrypoint := range strings.Split(value, ",") {
				if secureEntrypoint[strings.ToLower(strings.TrimSpace(entrypoint))] {
					return true
				}
			}
		}
	}
	return false
}

// secureEntrypoint names the entrypoints that conventionally carry TLS.
var secureEntrypoint = map[string]bool{
	"websecure":  true,
	"https":      true,
	"secure":     true,
	"web-secure": true,
}
