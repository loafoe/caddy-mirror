package caddymirror

import (
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

func TestUnmarshalCaddyfile(t *testing.T) {
	input := `mirror {
		to http://mirror1:9090 10
		to mirror2:9091
		max_body_size 1MB
		timeout 2s
	}`

	d := caddyfile.NewTestDispenser(input)
	m := new(MirrorTransport)
	if err := m.UnmarshalCaddyfile(d); err != nil {
		t.Fatalf("UnmarshalCaddyfile: %v", err)
	}

	if len(m.Mirrors) != 2 {
		t.Fatalf("expected 2 mirrors, got %d", len(m.Mirrors))
	}
	if m.Mirrors[0].To != "http://mirror1:9090" || m.Mirrors[0].Percent == nil || *m.Mirrors[0].Percent != 10 {
		t.Errorf("mirror[0] = %+v, want {To: http://mirror1:9090, Percent: 10}", m.Mirrors[0])
	}
	if m.Mirrors[1].To != "mirror2:9091" || m.Mirrors[1].Percent != nil {
		t.Errorf("mirror[1] = %+v, want {To: mirror2:9091, Percent: nil}", m.Mirrors[1])
	}
	if m.MaxBodySize != 1<<20 {
		t.Errorf("MaxBodySize = %d, want %d", m.MaxBodySize, 1<<20)
	}
	if time.Duration(m.Timeout) != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", time.Duration(m.Timeout))
	}
}

func TestUnmarshalCaddyfileUnlimitedBodySize(t *testing.T) {
	input := `mirror {
		to mirror1:9090
		max_body_size unlimited
	}`
	d := caddyfile.NewTestDispenser(input)
	m := new(MirrorTransport)
	if err := m.UnmarshalCaddyfile(d); err != nil {
		t.Fatalf("UnmarshalCaddyfile: %v", err)
	}
	if m.MaxBodySize != -1 {
		t.Errorf("MaxBodySize = %d, want -1", m.MaxBodySize)
	}
}

func TestUnmarshalCaddyfileHeaders(t *testing.T) {
	input := `mirror {
		to mirror1:9090
		header X-Api-Key {env.MIRROR_TEST_SECRET}
		header +X-Mirrored-From primary
		header -Authorization
	}`
	d := caddyfile.NewTestDispenser(input)
	m := new(MirrorTransport)
	if err := m.UnmarshalCaddyfile(d); err != nil {
		t.Fatalf("UnmarshalCaddyfile: %v", err)
	}
	if m.Headers == nil {
		t.Fatal("expected Headers to be set")
	}
	if got := m.Headers.Set.Get("X-Api-Key"); got != "{env.MIRROR_TEST_SECRET}" {
		t.Errorf("Set[X-Api-Key] = %q, want the raw placeholder (expanded later at request time)", got)
	}
	if got := m.Headers.Add.Get("X-Mirrored-From"); got != "primary" {
		t.Errorf("Add[X-Mirrored-From] = %q, want %q", got, "primary")
	}
	if len(m.Headers.Delete) != 1 || m.Headers.Delete[0] != "Authorization" {
		t.Errorf("Delete = %v, want [Authorization]", m.Headers.Delete)
	}
}

func TestUnmarshalCaddyfileErrors(t *testing.T) {
	cases := []string{
		`mirror {
			to
		}`,
		`mirror {
			to a b c
		}`,
		`mirror {
			to mirror1:9090 notanumber
		}`,
		`mirror {
			max_body_size
		}`,
		`mirror {
			max_body_size notasize
		}`,
		`mirror {
			timeout notaduration
		}`,
		`mirror {
			bogus_directive
		}`,
		`mirror {
			header a b c d
		}`,
	}
	for _, input := range cases {
		d := caddyfile.NewTestDispenser(input)
		m := new(MirrorTransport)
		if err := m.UnmarshalCaddyfile(d); err == nil {
			t.Errorf("input %q: expected error, got nil", input)
		}
	}
}
