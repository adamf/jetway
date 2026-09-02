// Package config loads the gateway's deployment configuration.
//
// A gateway's shape is per-partner: how each link is framed, how each peer is
// identified, where its traffic goes. Flags cannot express that, so from the
// point where more than one real partner exists, configuration is a file.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole deployment.
type Config struct {
	Identity  Identity  `yaml:"identity"`
	Store     Store     `yaml:"store"`
	Spool     Spool     `yaml:"spool"`
	HTTP      HTTP      `yaml:"http"`
	Ingress   []Ingress `yaml:"ingress"`
	Peers     []Peer    `yaml:"peers"`
	Routing   Routing   `yaml:"routing"`
	Telemetry Telemetry `yaml:"telemetry"`
	Demo      Demo      `yaml:"demo"`
	// LocatorSecret keys record locator allocation. Prefer the
	// JETWAY_LOCATOR_SECRET environment variable to putting it in a file.
	LocatorSecret string `yaml:"locator_secret"`
}

// Telemetry configures OpenTelemetry tracing.
//
// Off unless an endpoint is given: a gateway with nowhere to send spans should
// not pay to make them.
type Telemetry struct {
	// Endpoint is an OTLP HTTP traces endpoint, e.g.
	// http://collector:4318/v1/traces.
	Endpoint string `yaml:"endpoint"`
	// Headers are sent with every export, for a collector behind an API key.
	Headers map[string]string `yaml:"headers"`
	// ServiceName identifies this node in traces. Defaults to the identity name.
	ServiceName string `yaml:"service_name"`
	Environment string `yaml:"environment"`
	// SampleRatio is head sampling, 0 to 1. Zero means everything.
	SampleRatio float64 `yaml:"sample_ratio"`
}

// Routing controls what the node does with messages addressed elsewhere.
type Routing struct {
	// Relay forwards a message to the addressees on its priority line that are
	// served by other links -- what a Type B switch does, as opposed to an
	// endpoint that only terminates.
	//
	// It defaults to off deliberately. A node that relays for anyone who can
	// reach it is an open relay: a partner can spend another partner's link
	// budget through us, under our originator address, and we carry the
	// traffic and the blame. Turn it on when this deployment is meant to be a
	// switch, and only for links you would answer for.
	Relay bool `yaml:"relay"`
}

// Identity is how this node names itself to partners.
type Identity struct {
	Designator string `yaml:"designator"`
	TTYAddress string `yaml:"tty_address"`
	Name       string `yaml:"name"`
}

// Store selects persistence.
type Store struct {
	// Backend is "postgres" or "mem".
	Backend string `yaml:"backend"`
	DSN     string `yaml:"dsn"`
	// Migrate applies pending schema changes on start.
	Migrate bool `yaml:"migrate"`
	// MaxMessages and MaxRecords bound the in-memory backend. Zero is
	// unbounded; set them on anything reachable from the internet.
	MaxMessages int `yaml:"max_messages"`
	MaxRecords  int `yaml:"max_records"`
}

// Spool configures the durable inbound buffer.
type Spool struct {
	// Enabled turns on write-ahead spooling. Strongly recommended: without it
	// a database outage becomes refused acknowledgements to partners.
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
	// DrainInterval is how often the drainer sweeps after a failure.
	DrainInterval time.Duration `yaml:"drain_interval"`
}

// HTTP configures the console, API, health and metrics endpoints.
type HTTP struct {
	Addr string `yaml:"addr"`
	// Console serves the operations console. It is unauthenticated, so turn it
	// off on any listener reachable beyond a trusted network.
	Console bool `yaml:"console"`
	// Metrics serves /metrics.
	Metrics bool `yaml:"metrics"`
	TLS     *TLS `yaml:"tls"`
}

// TLS configures a listener's transport security.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	// ClientCA enables mutual TLS: connections must present a certificate
	// signed by this authority. This is what turns a link into an
	// authenticated one, rather than a peer asserting its own name.
	ClientCA string `yaml:"client_ca"`
	// MinVersion is "1.2" or "1.3". Defaults to 1.2.
	MinVersion string `yaml:"min_version"`
}

// Mutual reports whether client certificates are required.
func (t *TLS) Mutual() bool { return t != nil && t.ClientCA != "" }

// Framing describes how a byte stream is split into messages.
type Framing struct {
	// Kind is "length_prefix" or "sentinel".
	Kind string `yaml:"kind"`
	// HeaderBytes is the width of the length field: 2 or 4.
	HeaderBytes int `yaml:"header_bytes"`
	// LittleEndian selects byte order; network links are usually big endian.
	LittleEndian bool `yaml:"little_endian"`
	// Inclusive means the declared length counts the header itself.
	Inclusive bool `yaml:"inclusive"`
	// Terminator ends a message when Kind is "sentinel", e.g. "\nNNNN\n".
	Terminator string `yaml:"terminator"`
	// MaxBytes bounds one message.
	MaxBytes int `yaml:"max_bytes"`
}

// Identify maps an incoming connection or request to a configured peer.
//
// A peer name is never taken from the message payload or from anything the
// sender asserts about itself. It comes from the certificate they presented,
// the network they came from, or the fact that this listener serves exactly
// one partner.
type Identify struct {
	// Peer names the single partner this listener serves.
	Peer string `yaml:"peer"`
	// ByCertCN maps a client certificate common name to a peer name.
	ByCertCN map[string]string `yaml:"by_cert_cn"`
	// ByCIDR maps a source network to a peer name. Weaker than a certificate
	// and only appropriate on a private link.
	ByCIDR map[string]string `yaml:"by_cidr"`
	// ByHello accepts the peer name the connection asserts in its first
	// frame -- a transport hello. This is how one listener serves a whole
	// subscriber population. It is an assertion, not a proof, so it belongs
	// only where the network itself is trusted, exactly like ByCIDR.
	ByHello bool `yaml:"by_hello"`
}

// Ingress is one way messages get in.
type Ingress struct {
	Name string `yaml:"name"`
	// Type is "tcp", "https" or "filedrop".
	Type string `yaml:"type"`
	Addr string `yaml:"addr"`
	TLS  *TLS   `yaml:"tls"`

	Framing  Framing  `yaml:"framing"`
	Identify Identify `yaml:"identify"`
	MATIP    MATIP    `yaml:"matip"`

	// Synchronous makes an https listener hold the request open and return any
	// generated reply in the response body, rather than answering 202 and
	// delivering the reply over this peer's egress.
	Synchronous bool `yaml:"synchronous"`

	// Filedrop settings.
	Dir        string        `yaml:"dir"`
	Pattern    string        `yaml:"pattern"`
	ArchiveDir string        `yaml:"archive_dir"`
	Poll       time.Duration `yaml:"poll"`
	// StableFor is how long a file's size must stay unchanged before it is read,
	// so a file still being uploaded is not processed half-written.
	StableFor time.Duration `yaml:"stable_for"`
}

// MATIP configures a session under RFC 2351.
type MATIP struct {
	// Coding is the character coding both sides must agree on: ascii, ebcdic,
	// ipars or baudot. Empty means ascii, which is what Type B over IP carries.
	Coding string `yaml:"coding"`
	// Protection identifies the messaging responsibility transfer protocol.
	// 2 is BATAP; the standard assigns no other value.
	Protection int `yaml:"protection"`
	// SenderHLD and RecipientHLD are the host identifiers, when the link uses
	// them. Both must be set together.
	SenderHLD    int `yaml:"sender_hld"`
	RecipientHLD int `yaml:"recipient_hld"`
	// HandshakeTimeout bounds the session open exchange.
	HandshakeTimeout time.Duration `yaml:"handshake_timeout"`
}

// Egress is how replies and requests reach a peer.
type Egress struct {
	// Type is "tcp_dial", "tcp_accept", "https_post", "filedrop" or "via".
	//
	// "tcp_accept" means the peer connects to us and we reply on that session,
	// which is the common arrangement when we host the listener.
	//
	// "via" means this peer is reached through another peer's link, named in
	// Via. This is how the real network is wired: a carrier does not hold a
	// circuit to every partner, it holds one to the message switch and
	// addresses the rest by their teletype address. The transit link carries
	// the bytes; the address line already names the true destination, and the
	// switch routes on it.
	Type string `yaml:"type"`
	// Via names the peer whose link carries this peer's traffic, for
	// egress type "via".
	Via  string `yaml:"via"`
	Addr string `yaml:"addr"`
	URL  string `yaml:"url"`
	Dir  string `yaml:"dir"`

	TLS     *TLS    `yaml:"tls"`
	Framing Framing `yaml:"framing"`

	// Retry bounds redelivery of a message the transport would not take.
	Retry Retry `yaml:"retry"`
}

// Retry controls outbound redelivery.
type Retry struct {
	// MaxAttempts is the total number of delivery attempts. Zero uses the
	// default.
	MaxAttempts int `yaml:"max_attempts"`
	// Initial is the first backoff interval; it doubles up to Max.
	Initial time.Duration `yaml:"initial"`
	Max     time.Duration `yaml:"max"`
}

// Peer is a configured partner.
type Peer struct {
	Name    string `yaml:"name"`
	Carrier string `yaml:"carrier"`
	// Format is "typeb" or "edifact".
	Format     string `yaml:"format"`
	TTYAddress string `yaml:"tty_address"`
	// Addresses are further Type B addresses this link serves, beyond
	// TTYAddress, used when routing on the address line.
	Addresses []string `yaml:"addresses"`
	// ICAO is the peer's three-letter ICAO designator, so AFTN addressees
	// carrying it route down this link.
	ICAO string `yaml:"icao"`
	// AFTN marks the link to the aeronautical fixed network: AFTN addressees
	// no other link claims route here.
	AFTN bool `yaml:"aftn"`
	// CONTRL is when to send a syntax and service report for an interchange
	// this peer sends: "requested" (the default) honours the acknowledgement
	// request in UNB 0031, "always", "errors" for rejections only, or "never".
	CONTRL string `yaml:"contrl"`
	// EDIFACTID is the UNB sender identification to address this peer with.
	// Defaults to Carrier.
	EDIFACTID string `yaml:"edifact_id"`
	Egress    Egress `yaml:"egress"`
}

// Demo runs the simulated carrier fleet in this process.
type Demo struct {
	Carriers bool `yaml:"carriers"`
	// LinkAddrs maps a carrier designator to the gateway listener it dials.
	// Empty means each carrier dials the tcp ingress that identifies it.
	LinkAddrs map[string]string `yaml:"link_addrs"`
}

// Default returns the configuration jetwayd runs with when none is supplied:
// the demo, on loopback, in memory.
func Default() *Config {
	return &Config{
		Identity: Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		Store:    Store{Backend: "mem", Migrate: true},
		Spool:    Spool{Enabled: false, DrainInterval: 5 * time.Second},
		HTTP:     HTTP{Addr: "127.0.0.1:8080", Console: true, Metrics: true},
		// One listener per partner. That is how circuits are actually
		// provisioned, and it is what lets a peer be identified without the
		// sender asserting who it is.
		Ingress: []Ingress{
			{Name: "link-ba", Type: "tcp", Addr: "127.0.0.1:9101",
				Framing:  Framing{Kind: "length_prefix", HeaderBytes: 4},
				Identify: Identify{Peer: "BA"}},
			{Name: "link-aa", Type: "tcp", Addr: "127.0.0.1:9102",
				Framing:  Framing{Kind: "length_prefix", HeaderBytes: 4},
				Identify: Identify{Peer: "AA"}},
			{Name: "link-lh", Type: "tcp", Addr: "127.0.0.1:9103",
				Framing:  Framing{Kind: "length_prefix", HeaderBytes: 4},
				Identify: Identify{Peer: "LH"}},
		},
		Peers: []Peer{
			{Name: "BA", Carrier: "BA", Format: "typeb", TTYAddress: "LHRRMBA",
				Egress: Egress{Type: "tcp_accept"}},
			{Name: "AA", Carrier: "AA", Format: "edifact", TTYAddress: "DFWRMAA",
				Egress: Egress{Type: "tcp_accept"}},
			{Name: "LH", Carrier: "LH", Format: "typeb", TTYAddress: "FRARMLH",
				Egress: Egress{Type: "tcp_accept"}},
		},
		Demo: Demo{Carriers: true},
	}
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv substitutes ${VAR} from the environment.
//
// Only the braced form is expanded. A bare $ appears in passwords and in
// message content, and silently eating it would be worse than not supporting it.
func expandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(envRe.FindStringSubmatch(m)[1])
	})
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(b)
}

// Parse decodes and validates configuration bytes.
func Parse(b []byte) (*Config, error) {
	c := Default()
	// Start from an empty ingress list so a file that declares listeners
	// replaces the default rather than appending to it.
	c.Ingress = nil

	dec := yaml.NewDecoder(strings.NewReader(expandEnv(string(b))))
	dec.KnownFields(true) // a typo in an ops file must fail loudly, not silently
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the configuration is internally coherent.
func (c *Config) Validate() error {
	if len(c.Identity.Designator) != 2 {
		return fmt.Errorf("config: identity.designator must be two characters, got %q", c.Identity.Designator)
	}
	if c.Identity.TTYAddress != "" && len(c.Identity.TTYAddress) != 7 {
		return fmt.Errorf("config: identity.tty_address must be seven characters, got %q", c.Identity.TTYAddress)
	}
	if c.Identity.Name == "" {
		c.Identity.Name = c.Identity.Designator
	}
	switch c.Store.Backend {
	case "mem":
	case "postgres":
		if c.Store.DSN == "" {
			return fmt.Errorf("config: store.dsn is required when backend is postgres")
		}
	default:
		return fmt.Errorf("config: store.backend must be mem or postgres, got %q", c.Store.Backend)
	}
	if c.Spool.Enabled && c.Spool.Dir == "" {
		return fmt.Errorf("config: spool.dir is required when the spool is enabled")
	}
	if c.Spool.DrainInterval <= 0 {
		c.Spool.DrainInterval = 5 * time.Second
	}
	if err := c.HTTP.TLS.validate("http.tls"); err != nil {
		return err
	}

	names := map[string]bool{}
	for i := range c.Ingress {
		in := &c.Ingress[i]
		if in.Name == "" {
			return fmt.Errorf("config: ingress[%d] has no name", i)
		}
		if names[in.Name] {
			return fmt.Errorf("config: duplicate ingress name %q", in.Name)
		}
		names[in.Name] = true
		if err := in.validate(); err != nil {
			return err
		}
	}

	peers := map[string]bool{}
	for i := range c.Peers {
		p := &c.Peers[i]
		if p.Name == "" {
			return fmt.Errorf("config: peers[%d] has no name", i)
		}
		if peers[p.Name] {
			return fmt.Errorf("config: duplicate peer %q", p.Name)
		}
		peers[p.Name] = true
		if p.Carrier == "" {
			p.Carrier = p.Name
		}
		if p.EDIFACTID == "" {
			p.EDIFACTID = p.Carrier
		}
		switch p.Format {
		case "typeb", "edifact":
		case "":
			p.Format = "typeb"
		default:
			return fmt.Errorf("config: peer %q: format must be typeb or edifact, got %q", p.Name, p.Format)
		}
		if err := p.Egress.validate(p.Name); err != nil {
			return err
		}
	}

	// Every peer an ingress can resolve to must actually be configured, or
	// traffic authenticates successfully and then has nowhere to go.
	for _, in := range c.Ingress {
		for _, peer := range in.Identify.peers() {
			if !peers[peer] && len(c.Peers) > 0 {
				return fmt.Errorf("config: ingress %q can identify peer %q, which is not configured under peers",
					in.Name, peer)
			}
		}
	}
	return nil
}

func (id Identify) peers() []string {
	var out []string
	if id.Peer != "" {
		out = append(out, id.Peer)
	}
	for _, v := range id.ByCertCN {
		out = append(out, v)
	}
	for _, v := range id.ByCIDR {
		out = append(out, v)
	}
	return out
}

// Resolvable reports whether this listener can identify anybody at all.
func (id Identify) Resolvable() bool {
	return id.Peer != "" || len(id.ByCertCN) > 0 || len(id.ByCIDR) > 0 || id.ByHello
}

func (t *TLS) validate(where string) error {
	if t == nil {
		return nil
	}
	if t.Cert == "" || t.Key == "" {
		return fmt.Errorf("config: %s needs both cert and key", where)
	}
	switch t.MinVersion {
	case "", "1.2", "1.3":
	default:
		return fmt.Errorf("config: %s.min_version must be 1.2 or 1.3, got %q", where, t.MinVersion)
	}
	return nil
}

func (in *Ingress) validate() error {
	if err := in.TLS.validate("ingress " + in.Name + " tls"); err != nil {
		return err
	}
	switch in.Type {
	case "matip":
		if !in.Identify.Resolvable() {
			return fmt.Errorf("config: ingress %q has no way to identify a peer; "+
				"a MATIP session open declares traffic characteristics, not identity", in.Name)
		}
		if len(in.Identify.ByCertCN) > 0 && !in.TLS.Mutual() {
			return fmt.Errorf("config: ingress %q identifies peers by certificate but has no "+
				"tls.client_ca, so no certificate is required", in.Name)
		}
		switch in.MATIP.Coding {
		case "", "ascii", "ebcdic", "ipars", "baudot":
		default:
			return fmt.Errorf("config: ingress %q: matip.coding must be ascii, ebcdic, ipars or baudot, got %q",
				in.Name, in.MATIP.Coding)
		}
	case "tcp", "https":
		if in.Addr == "" {
			return fmt.Errorf("config: ingress %q needs an addr", in.Name)
		}
		if !in.Identify.Resolvable() {
			return fmt.Errorf("config: ingress %q has no way to identify a peer; "+
				"set identify.peer, identify.by_cert_cn, identify.by_cidr or identify.by_hello", in.Name)
		}
		if len(in.Identify.ByCertCN) > 0 && !in.TLS.Mutual() {
			return fmt.Errorf("config: ingress %q identifies peers by certificate but has no "+
				"tls.client_ca, so no certificate is required", in.Name)
		}
	case "filedrop":
		if in.Dir == "" {
			return fmt.Errorf("config: ingress %q needs a dir", in.Name)
		}
		if in.Identify.Peer == "" {
			return fmt.Errorf("config: ingress %q needs identify.peer; a file carries no identity", in.Name)
		}
	default:
		return fmt.Errorf("config: ingress %q: type must be matip, tcp, https or filedrop, got %q", in.Name, in.Type)
	}
	if in.Identify.ByHello && in.Type != "tcp" {
		return fmt.Errorf("config: ingress %q: by_hello identification is only for tcp listeners", in.Name)
	}
	if in.Type != "filedrop" && in.Type != "matip" {
		if err := in.Framing.validate("ingress " + in.Name); err != nil {
			return err
		}
	}
	if in.Poll <= 0 {
		in.Poll = 5 * time.Second
	}
	if in.StableFor <= 0 {
		in.StableFor = 2 * time.Second
	}
	if in.Pattern == "" {
		in.Pattern = "*"
	}
	return nil
}

func (f *Framing) validate(where string) error {
	switch f.Kind {
	case "", "length_prefix":
		f.Kind = "length_prefix"
		if f.HeaderBytes == 0 {
			f.HeaderBytes = 4
		}
		if f.HeaderBytes != 2 && f.HeaderBytes != 4 {
			return fmt.Errorf("config: %s framing.header_bytes must be 2 or 4, got %d", where, f.HeaderBytes)
		}
	case "sentinel":
		if f.Terminator == "" {
			f.Terminator = "\nNNNN\n"
		}
	default:
		return fmt.Errorf("config: %s framing.kind must be length_prefix or sentinel, got %q", where, f.Kind)
	}
	return nil
}

func (e *Egress) validate(peer string) error {
	if err := e.TLS.validate("peer " + peer + " egress tls"); err != nil {
		return err
	}
	switch e.Type {
	case "", "tcp_accept":
		e.Type = "tcp_accept"
	case "tcp_dial":
		if e.Addr == "" {
			return fmt.Errorf("config: peer %q egress needs an addr", peer)
		}
		if err := e.Framing.validate("peer " + peer + " egress"); err != nil {
			return err
		}
	case "https_post":
		if e.URL == "" {
			return fmt.Errorf("config: peer %q egress needs a url", peer)
		}
	case "filedrop":
		if e.Dir == "" {
			return fmt.Errorf("config: peer %q egress needs a dir", peer)
		}
	default:
		return fmt.Errorf("config: peer %q: egress.type must be tcp_accept, tcp_dial, https_post or filedrop, got %q",
			peer, e.Type)
	}
	if e.Retry.MaxAttempts == 0 {
		e.Retry.MaxAttempts = 8
	}
	if e.Retry.Initial <= 0 {
		e.Retry.Initial = 2 * time.Second
	}
	if e.Retry.Max <= 0 {
		e.Retry.Max = 5 * time.Minute
	}
	return nil
}
