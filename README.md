# Compose Monitor

A small service that shows what a Docker Compose project is doing — its
containers, networks and volumes — on a page that updates itself as things come
and go.

[![CI](https://github.com/k15g/compose-monitor/actions/workflows/ci.yml/badge.svg)](https://github.com/k15g/compose-monitor/actions/workflows/ci.yml)
[![Licence: GPL-3.0](https://img.shields.io/badge/licence-GPL--3.0-blue.svg)](LICENSE)

It reads the local Docker socket and nothing else. There is no database, no
agent, and no configuration beyond naming the project to watch.

## What it shows

Compose labels everything it creates — containers, networks and volumes — with
the project it belongs to (`com.docker.compose.project`), and containers with
the service they run (`com.docker.compose.service`). Those labels are the whole
of the join between "a project" and "the things making it up", and they are what
this reads.

**Front page** — what is running, one line each: the name the image gives
itself, how long it has been up, whether it is healthy, what image it is, and
what it answers on.

**Services** — every container in every state, so a stopped service is shown as
offline rather than disappearing. With `CONTROL_ENABLED=true`, running ones get
a **Stop** button and stopped ones **Start** and **Remove**. Where a service is
routed by Traefik, an **Open** button goes to it — that one needs no permission,
since opening a service does nothing to it.

**A service in detail** — image, command, lifecycle, ports, networks, mounts,
environment and labels, and the tail of its log. Ports for a stopped container
come from its configuration, so the panel says what it will listen on rather
than nothing at all.

**Networks and volumes** — what Compose created, each saying how many containers
still refer to it. Anything nothing refers to can be removed.

The pages that list services follow the daemon's event stream, so they reflect a
`docker compose up` or `stop` in about the time the daemon takes to report it —
no polling from the browser and no refresh. A row is highlighted when something
actually happened to it; one redrawn because "Up 3 minutes" became "Up 4
minutes" changes quietly, or every row would blink on a timer with nothing to
look at.

## Running it

The image is published to the GitHub Container Registry for `linux/amd64` and
`linux/arm64`, under one manifest — `docker pull` gets the right binary without
being told which:

```
ghcr.io/k15g/compose-monitor:latest   # the most recent release
ghcr.io/k15g/compose-monitor:1.0.0    # a release, pinned
ghcr.io/k15g/compose-monitor:edge     # the tip of main
```

The only required setting is the project to watch — the `name:` of its compose
file, which is also what its containers are labelled with:

```yaml
services:
  compose-monitor:
    image: ghcr.io/k15g/compose-monitor:latest
    environment:
      PROJECT_NAME: my-project
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    group_add:
      - "${DOCKER_GID}"          # see below
    ports:
      - "8080:8080"
```

There is no default for `PROJECT_NAME` on purpose: a guess would quietly monitor
the wrong project, which is worse than refusing to start.

`compose.yaml` in this repository is a fuller example, including running the
socket through a proxy.

### The socket, and the group

The image runs as a non-root user and the socket is mode 660, so the container
needs a supplementary group matching the socket's owner — otherwise every read
fails with `permission denied`.

The GID to use is the one the **container** sees, which is not always the one
the host reports. On Docker Desktop the socket inside a container is usually
`root:root`, making the answer `0`. Ask the container rather than the host:

```sh
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock:ro \
  alpine stat -c '%u:%g %a' /var/run/docker.sock
```

Mounting the socket read-only is worth doing and worth not over-trusting: the
Engine API has no read-only mode, so `:ro` stops the socket *file* being
written, not the API being used. Anything that reaches it can start a container.
`compose.yaml` documents putting a socket proxy in front, which is the real
restriction.

### Configuration

Everything is environment variables. The ones that matter:

| Variable          | Default                       | Meaning                                      |
|-------------------|-------------------------------|----------------------------------------------|
| `PROJECT_NAME`    | — (required)                  | The `name:` of the Compose project to watch  |
| `PROJECT_TITLE`   | `PROJECT_NAME`                | What the pages call it                       |
| `DOCKER_HOST`     | `unix:///var/run/docker.sock` | The socket, or a proxy in front of it        |
| `HTTP_PORT`       | `8080`                        | Port to serve on                             |
| `CONTROL_ENABLED` | `false`                       | Whether the action buttons exist             |

The rest are documented in [`.env.example`](.env.example).

### Acting on containers

The action buttons are **off by default**. Set `CONTROL_ENABLED=true` to get
them, and know what it means first:

- **The page has no authentication.** Anyone who can reach it can stop or remove
  a container, so turning this on says that whoever can reach the page is
  allowed to do that. Left off, the service never holds a handle that can act on
  a container, and the endpoints refuse even when called directly.
- **What an action names is checked against a fresh, project-scoped read**, so
  an id naming something outside the project comes back as "not found" — the
  same answer as one that does not exist, so the service does not confirm what
  else is on the host. State-changing requests also require `Sec-Fetch-Site` to
  be same-origin, which is what stops a form on another site posting here.

Removing is the one action that cannot be undone. Every removal asks first, in a
modal whose question is rendered by the server — so it cannot drift from what
the server will actually do, and without JavaScript the same control navigates
to the same question as a page. Removing a **volume** destroys the data in it,
and is the only action here that does.

### Health

`/healthz` reports whether the process is serving, as JSON. It deliberately
stays `ok` when the Docker socket is unreadable and says so in a `runtime` field
instead: that is a state the service recovers from by itself, and failing the
probe would have an orchestrator restart a process that is working fine. What it
does catch is a server that is up but wedged.

The image has no shell, so its `HEALTHCHECK` runs the binary against itself
(`compose-monitor -healthcheck`) rather than a `curl` that is not there.

### When the socket cannot be read

The service starts anyway and serves a page naming the cause, and that page
reloads itself. Fixing the mount or the group is enough — there is nothing to
restart, because the read is retried on an interval and the event stream
reconnects with backoff.

The one configuration error that does stop the process is a `DOCKER_HOST` that
cannot be parsed, since no amount of waiting fixes it.

## Architecture

Clean hexagonal, with dependencies flowing inward only:

```
cmd/server/            wiring and graceful shutdown
internal/domain/       Service, Network, Volume, Event — no dependencies at all
internal/ports/        the interfaces the application layer depends on
internal/app/          MonitorService, NetworkService, VolumeService
internal/adapters/
  docker/              the Engine API over the local socket
  realtime/            the SSE endpoint, the fan-out hub, the renderer
  web/                 the pages, the icons and the embedded assets
internal/config/       loaded once in cmd/, carried on the context
```

Reading and acting are separate ports — `ContainerSource` and
`ContainerControl`, and the same split for networks and volumes. That is what
lets a read-only deployment be one that never holds a handle able to stop
anything, rather than one that remembers not to use it.

Four decisions are worth knowing before changing anything:

**The comparison lives in `internal/app`, not in the Docker adapter.** The
adapter reports state and nothing else. What counts as online, what counts as a
change, and what is worth drawing attention to are decisions about the product,
so they live with the business logic and are testable without a daemon.

**Scoping to the project is authorisation, and it lives in `internal/app`.**
Listing is scoped by a label filter, but inspect, logs, start, stop and remove
all take any id the daemon knows — so every one of them checks membership before
acting.

**Rendering happens on the way out, per connection.** An event carries the
service and not a rendering of it: a service is drawn differently on the front
page than on the services page, and only the subscriber's connection knows which
page it belongs to. The pages ask for the shape they want when they open the
stream.

**The HTTP server must not set `WriteTimeout`.** Go applies it as a deadline on
the connection once the request headers are read, and an event stream stays open
for as long as the tab does. There is no way to exempt one route from it.

### Why SSE rather than a WebSocket

The traffic is one-way and the browser reconnects on its own, so there is no
backoff loop to write and no ping/pong to maintain. Every connection opens with
a full snapshot, which means a client that reconnects after a drop is correct
immediately without replaying what it missed.

## Development

```sh
make generate   # regenerate the templ templates
make check      # vet, test with the race detector, lint
make build
make run        # needs PROJECT_NAME and a readable socket
```

The templates' generated Go (`*_templ.go`) is committed and CI fails if it is
stale — run `make generate` after editing a `.templ` file.

The Docker adapter is tested against a stand-in daemon that speaks the Engine
API over a real unix socket, so the requests under test are the ones the SDK
builds. htmx is vendored into the repository; nothing is fetched from a CDN at
runtime.

### Releasing

CI builds and publishes to `ghcr.io/k15g/compose-monitor` after the checks pass:

- a push to `main` publishes **`edge`**;
- publishing a GitHub release from a tag such as `v1.0.1` publishes **`1.0.1`**
  and moves **`latest`**.

Both tags of a release come from a single build, so the versioned image and
`latest` are the same image rather than two builds of the same commit.
Pre-releases get their version tag but do not move `latest`.

`org.opencontainers.image.version` is passed into the build: a release stamps
its number, and any other build stamps `<unix timestamp>-<short commit>` — enough
to tell two `edge` images apart and to trace either back to a commit. It has to
be passed in rather than worked out in the Dockerfile, because `.dockerignore`
keeps `.git` out of the build context. `make image` applies the same rule
locally, and `make version` prints what it would use.

## What it does not do

**A project that has been fully `docker compose down`** has no containers left,
and the daemon keeps no record that it ever did, so the services page goes empty
rather than showing them greyed out. The volumes page is unaffected, since
volumes survive a `down`.

**Logs do not stream.** The detail page fetches a snapshot when the panel is
opened. Live tailing needs a log stream per open container, with its own
cancellation and backpressure.

**Networks and volumes do not update live.** They change rarely, and a page load
is cheap.

**A network or volume declared `external: true`** was not created by Compose,
carries no project label, and is therefore not listed. That is deliberate: it
belongs to whoever made it.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE).

Copyright © Klakegg Consulting AS.
