# caddy-mirror

Caddy `reverse_proxy` transport that mirrors ("shadows") traffic to secondary
upstreams — similar to nginx's `mirror` module. Mirror responses are always
discarded, so mirroring never affects the primary request's outcome,
timing, or error handling. Implemented as a proxy **transport** module
plugged into an existing `reverse_proxy` directive, not a new standalone
handler.

## Quick Start

```caddyfile
:3000 {
    reverse_proxy backend:8080 {
        transport mirror {
            to shadow:9090 10
            header X-Mirrored-From primary
        }
    }
}
```

Every request served by `backend:8080` also gets a 10%-sampled duplicate
sent to `shadow:9090`, tagged with `X-Mirrored-From: primary`. The client
only ever sees `backend:8080`'s response.

## Development

Read [Extending Caddy](https://caddyserver.com/docs/extending-caddy) to get
an overview of what interfaces you need to implement.

## Building

You first need to build a new Caddy executable with this plugin. The
easiest way is with [xcaddy](https://github.com/caddyserver/xcaddy).

Install xcaddy:

```shell
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
```

After xcaddy installation you can build Caddy with this plugin by executing:

```shell
xcaddy build --with github.com/loafoe/caddy-mirror
```

## Configuration

The `transport mirror` block is nested inside a `reverse_proxy` directive
and supports the following options.

### Directive Syntax

```caddyfile
reverse_proxy <primary_upstream> {
    transport mirror {
        to <address> [<percent>]
        max_body_size <size>|unlimited
        timeout <duration>
        header [+|-]<field> [<value>]
    }
}
```

### Directives Reference

#### `to`
Adds a mirror upstream. Repeatable — every mirror listed receives its own
sampled copy of each request.

**Syntax:** `to <address> [<percent>]`

- `<address>` may be a full URL (`http://host:port`) or a bare `host:port`
  (assumed `http://`).
- `<percent>` is the sampling rate (0-100). Optional; defaults to `100`
  (mirror every request). An explicit `0` disables that mirror without
  removing the line.

**Example:**
```caddyfile
transport mirror {
    to http://shadow1:9090 10
    to shadow2:9090
}
```

#### `max_body_size`
Bounds how much of the request body is buffered in memory to duplicate it
to mirrors.

**Syntax:** `max_body_size <size>|unlimited`
**Default:** `2MB`

Accepts a plain byte count or a size with `KB`/`MB`/`GB` suffix. Requests
with a larger body are still forwarded to the primary upstream in full —
they are just mirrored without a body, so mirroring can never truncate or
slow down the real request.

**Example:**
```caddyfile
transport mirror {
    to shadow:9090
    max_body_size 512KB
}
```

#### `timeout`
Bounds how long a single mirror request is allowed to run before being
cancelled.

**Syntax:** `timeout <duration>`
**Default:** `5s`

Each mirror request runs with its own timeout, independent of the primary
request's context — a client disconnecting early doesn't cut a mirror
short, but a hung mirror also can't leak goroutines/connections forever.

**Example:**
```caddyfile
transport mirror {
    to shadow:9090
    timeout 2s
}
```

#### `header`
Adds, sets, or deletes a header on every mirrored request. Repeatable.
Applies uniformly to all mirrors — the primary request's headers are
**never** touched.

**Syntax:** `header [+|-]<field> [<value>]`

- `header <field> <value>` — set (replaces any existing value)
- `header +<field> <value>` — add (keeps existing values)
- `header -<field>` — delete

`<value>` supports Caddy's `{env.VAR}` placeholder, resolved from the
process environment at the time each mirror request is dispatched — this
is how you inject secrets (API keys, mirror auth tokens) without putting
them in the config file itself.

**Example:**
```caddyfile
transport mirror {
    to shadow:9090
    header X-Api-Key {env.MIRROR_API_KEY}
    header +X-Mirrored-From primary
    header -Authorization
}
```

## Complete Configuration Examples

### Basic mirroring
Mirror 100% of traffic to a single shadow backend for testing a new
service version against real production requests.

```caddyfile
:8080 {
    reverse_proxy backend:8080 {
        transport mirror {
            to shadow-canary:8080
        }
    }
}
```

### Percentage-sampled mirroring with authenticated mirror
Mirror 10% of traffic to an external analytics endpoint that requires its
own API key, stripping the client's own `Authorization` header so it
never reaches that third party.

```caddyfile
:8080 {
    reverse_proxy backend:8080 {
        transport mirror {
            to https://analytics.example.com 10
            max_body_size 1MB
            timeout 3s
            header X-Api-Key {env.ANALYTICS_API_KEY}
            header -Authorization
        }
    }
}
```

### Multiple mirrors with independent sampling
Send everything to an internal debug endpoint, but only 1% to a
higher-latency external one.

```caddyfile
:8080 {
    reverse_proxy backend:8080 {
        transport mirror {
            to debug-sink:9090
            to https://slow-external-mirror.example.com 1
            timeout 10s
        }
    }
}
```

## JSON Config

```json
{
  "handler": "reverse_proxy",
  "upstreams": [{ "dial": "backend:8080" }],
  "transport": {
    "protocol": "mirror",
    "mirrors": [
      { "to": "http://shadow1:9090", "percent": 10 },
      { "to": "shadow2:9090" }
    ],
    "max_body_size": 2097152,
    "timeout": 5000000000,
    "headers": {
      "set": { "X-Api-Key": ["{env.MIRROR_API_KEY}"] },
      "add": { "X-Mirrored-From": ["primary"] },
      "delete": ["Authorization"]
    }
  }
}
```

## Not (Yet) Supported

- Per-mirror header overrides (headers currently apply uniformly to all
  mirrors).
- Health checks or load balancing across multiple addresses for a single
  mirror target — each `to` is a single static address, unlike the primary
  upstream's full `reverse_proxy` feature set.
- Capturing or comparing mirror responses (they are always discarded).

## Security Considerations

- **Mirror responses are always discarded and never affect the client.**
  A mirror returning errors, timing out, or being entirely unreachable has
  no effect on the primary request/response — errors are only logged (at
  debug level).
- **Injected `header` operations apply only to mirror requests.** The
  primary upstream never sees mirror-only headers (e.g. a mirror API key),
  and headers deleted for mirrors (e.g. `header -Authorization`) are still
  sent to the primary upstream untouched.
- **`{env.VAR}` secrets are resolved at request-dispatch time**, not baked
  into the loaded config, via Caddy's replacer (`os.Getenv` under the
  hood). Treat the process environment as the source of truth for mirror
  secrets.
- **`max_body_size` bounds memory usage**, not request size. Oversized
  request bodies are always forwarded to the primary upstream in full;
  only the mirror copy is dropped, so this setting cannot be used to
  reject or truncate real traffic.
- **`timeout` bounds background resource usage.** Each mirror request runs
  in its own goroutine with an independent timeout, so a slow or dead
  mirror cannot accumulate unbounded goroutines or connections over time.

## Testing

```sh
go test ./... -race
```

Includes unit tests for Caddyfile/size parsing, request sampling, header
injection (including `{env.VAR}` secret substitution and primary/mirror
header isolation), and body buffering, plus a full integration test that
spins up a real Caddy instance (via `caddytest`) to verify end-to-end
mirroring behavior.

## License

Apache 2.0 — see [LICENSE](LICENSE).
