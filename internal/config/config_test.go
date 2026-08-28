package config

import (
	"strings"
	"testing"
	"time"
)

const good = `
identity:
  designator: 1J
  tty_address: LONRM1J
  name: jetway
store:
  backend: postgres
  dsn: ${TEST_DSN}
  migrate: true
spool:
  enabled: true
  dir: /var/lib/jetway/spool
http:
  addr: 127.0.0.1:8080
  console: false
  metrics: true
ingress:
  - name: partners-https
    type: https
    addr: 0.0.0.0:8443
    tls:
      cert: /etc/jetway/server.crt
      key: /etc/jetway/server.key
      client_ca: /etc/jetway/partners-ca.crt
    identify:
      by_cert_cn:
        ba.example.com: BA
  - name: ba-batch
    type: filedrop
    dir: /var/spool/jetway/ba
    pattern: "*.pnl"
    identify:
      peer: BA
peers:
  - name: BA
    carrier: BA
    format: typeb
    tty_address: LHRRMBA
    egress:
      type: https_post
      url: https://ba.example.com/jetway/messages
      retry:
        max_attempts: 12
`

func TestParseGoodConfig(t *testing.T) {
	t.Setenv("TEST_DSN", "postgres://user@db/jetway")
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Store.DSN != "postgres://user@db/jetway" {
		t.Errorf("environment substitution failed: %q", c.Store.DSN)
	}
	if len(c.Ingress) != 2 {
		t.Fatalf("ingress = %d, want 2", len(c.Ingress))
	}
	if !c.Ingress[0].TLS.Mutual() {
		t.Error("a client_ca must mark the listener as mutually authenticated")
	}
	if c.Ingress[1].Pattern != "*.pnl" {
		t.Errorf("pattern = %q", c.Ingress[1].Pattern)
	}
	// Defaults must be filled in so callers never see a zero interval.
	if c.Ingress[1].Poll <= 0 || c.Ingress[1].StableFor <= 0 {
		t.Errorf("filedrop timings not defaulted: %+v", c.Ingress[1])
	}
	if c.Peers[0].Egress.Retry.MaxAttempts != 12 {
		t.Errorf("retry policy lost: %+v", c.Peers[0].Egress.Retry)
	}
	if c.Peers[0].Egress.Retry.Initial != 2*time.Second {
		t.Errorf("retry initial not defaulted: %v", c.Peers[0].Egress.Retry.Initial)
	}
	if c.Peers[0].EDIFACTID != "BA" {
		t.Errorf("edifact id should default to the carrier, got %q", c.Peers[0].EDIFACTID)
	}
}

// An ops file is edited by hand under pressure. A typo must fail loudly rather
// than silently leave a listener unauthenticated.
func TestUnknownFieldIsRejected(t *testing.T) {
	_, err := Parse([]byte("identity:\n  designator: 1J\n  tty_addres: LONRM1J\n"))
	if err == nil {
		t.Fatal("expected a misspelled field to be rejected")
	}
	if !strings.Contains(err.Error(), "tty_addres") {
		t.Errorf("error should name the offending field: %v", err)
	}
}

func TestValidationCatchesMisconfiguration(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"short designator", "identity:\n  designator: 1JX\n", "two characters"},
		{"bad tty length", "identity:\n  designator: 1J\n  tty_address: SHORT\n", "seven characters"},
		{"postgres without dsn", "identity:\n  designator: 1J\nstore:\n  backend: postgres\n", "store.dsn"},
		{"unknown backend", "identity:\n  designator: 1J\nstore:\n  backend: sqlite\n", "mem or postgres"},
		{"spool without dir", "identity:\n  designator: 1J\nspool:\n  enabled: true\n", "spool.dir"},
		{
			"ingress with no identity",
			"identity:\n  designator: 1J\ningress:\n  - name: x\n    type: tcp\n    addr: :9100\n",
			"no way to identify a peer",
		},
		{
			"cert identity without a client CA",
			"identity:\n  designator: 1J\ningress:\n  - name: x\n    type: tcp\n    addr: \":9100\"\n" +
				"    identify:\n      by_cert_cn:\n        a: BA\n",
			"no tls.client_ca",
		},
		{
			"filedrop with no peer",
			"identity:\n  designator: 1J\ningress:\n  - name: x\n    type: filedrop\n    dir: /tmp/x\n",
			"identify.peer",
		},
		{
			"duplicate ingress names",
			"identity:\n  designator: 1J\ningress:\n" +
				"  - name: x\n    type: filedrop\n    dir: /tmp/a\n    identify:\n      peer: BA\n" +
				"  - name: x\n    type: filedrop\n    dir: /tmp/b\n    identify:\n      peer: BA\n",
			"duplicate ingress name",
		},
		{
			"bad framing width",
			"identity:\n  designator: 1J\ningress:\n  - name: x\n    type: tcp\n    addr: \":1\"\n" +
				"    identify:\n      peer: BA\n    framing:\n      header_bytes: 3\n",
			"header_bytes",
		},
		{
			"tcp_dial without an address",
			"identity:\n  designator: 1J\npeers:\n  - name: BA\n    egress:\n      type: tcp_dial\n",
			"needs an addr",
		},
		{
			"https_post without a url",
			"identity:\n  designator: 1J\npeers:\n  - name: BA\n    egress:\n      type: https_post\n",
			"needs a url",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("expected rejection")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// Traffic that authenticates and then has nowhere to go is worse than traffic
// that is refused, because it looks like it worked.
func TestIngressPeerMustBeConfigured(t *testing.T) {
	y := "identity:\n  designator: 1J\n" +
		"ingress:\n  - name: x\n    type: filedrop\n    dir: /tmp/x\n    identify:\n      peer: ZZ\n" +
		"peers:\n  - name: BA\n"
	_, err := Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "not configured under peers") {
		t.Errorf("expected an unconfigured peer to be caught, got %v", err)
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("the built-in default must validate: %v", err)
	}
	if len(c.Ingress) != len(c.Peers) {
		t.Errorf("the demo should give each peer its own listener: %d ingress, %d peers",
			len(c.Ingress), len(c.Peers))
	}
	for _, in := range c.Ingress {
		if !in.Identify.Resolvable() {
			t.Errorf("ingress %q cannot identify anybody", in.Name)
		}
	}
}

// Only ${VAR} is expanded. A bare dollar appears in passwords and in message
// content, and eating it would be worse than not supporting it.
func TestOnlyBracedEnvIsExpanded(t *testing.T) {
	t.Setenv("JETWAY_TEST_VAR", "expanded")
	if got := expandEnv("a ${JETWAY_TEST_VAR} b $JETWAY_TEST_VAR c $ d"); got != "a expanded b $JETWAY_TEST_VAR c $ d" {
		t.Errorf("expandEnv = %q", got)
	}
}

func TestFileConfigReplacesDefaultIngress(t *testing.T) {
	c, err := Parse([]byte("identity:\n  designator: 1J\n" +
		"ingress:\n  - name: only\n    type: filedrop\n    dir: /tmp/x\n    identify:\n      peer: BA\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Ingress) != 1 || c.Ingress[0].Name != "only" {
		t.Errorf("a declared ingress list must replace the default, not append: %+v", c.Ingress)
	}
}
