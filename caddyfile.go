package caddymirror

import (
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
)

// UnmarshalCaddyfile deserializes Caddyfile tokens into m.
//
//	transport mirror {
//	    to <address> [<percent>]
//	    max_body_size <size>|unlimited
//	    timeout <duration>
//	    header [+|-]<field> [<value|regexp> [<replacement>]]
//	}
func (m *MirrorTransport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume transport name
	for d.NextBlock(0) {
		switch d.Val() {
		case "to":
			args := d.RemainingArgs()
			if len(args) < 1 || len(args) > 2 {
				return d.ArgErr()
			}
			mu := &MirrorUpstream{To: args[0]}
			if len(args) == 2 {
				pct, err := strconv.Atoi(args[1])
				if err != nil {
					return d.Errf("invalid percent %q: %v", args[1], err)
				}
				mu.Percent = &pct
			}
			m.Mirrors = append(m.Mirrors, mu)

		case "max_body_size":
			if !d.NextArg() {
				return d.ArgErr()
			}
			if d.Val() == "unlimited" {
				m.MaxBodySize = -1
				break
			}
			size, err := parseSize(d.Val())
			if err != nil {
				return d.Errf("invalid max_body_size %q: %v", d.Val(), err)
			}
			m.MaxBodySize = size

		case "timeout":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("invalid timeout %q: %v", d.Val(), err)
			}
			m.Timeout = caddy.Duration(dur)

		case "header":
			if m.Headers == nil {
				m.Headers = new(headers.HeaderOps)
			}
			args := d.RemainingArgs()
			var err error
			switch len(args) {
			case 1:
				err = headers.CaddyfileHeaderOp(m.Headers, args[0], "", nil)
			case 2:
				err = headers.CaddyfileHeaderOp(m.Headers, args[0], args[1], nil)
			case 3:
				err = headers.CaddyfileHeaderOp(m.Headers, args[0], args[1], &args[2])
			default:
				return d.ArgErr()
			}
			if err != nil {
				return d.Err(err.Error())
			}

		default:
			return d.Errf("unrecognized subdirective '%s'", d.Val())
		}
	}
	return nil
}

var _ caddyfile.Unmarshaler = (*MirrorTransport)(nil)
