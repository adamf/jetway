package node

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/demo"
	"github.com/adamf/jetway/pkg/egress"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/ingress"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/spool"
	"github.com/adamf/jetway/pkg/store"
)

func openStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (store.Store, error) {
	switch cfg.Store.Backend {
	case "mem":
		m := store.NewMem()
		m.MaxMessages, m.MaxRecords = cfg.Store.MaxMessages, cfg.Store.MaxRecords
		if m.MaxMessages == 0 && m.MaxRecords == 0 {
			log.Warn("using the in-memory store unbounded; nothing survives a restart and memory grows with traffic")
		} else {
			log.Info("using the in-memory store", "max_messages", m.MaxMessages, "max_records", m.MaxRecords)
		}
		return m, nil
	case "postgres":
		pg, err := store.OpenPostgres(ctx, cfg.Store.DSN)
		if err != nil {
			return nil, err
		}
		if cfg.Store.Migrate {
			if err := store.MigrateSchema(ctx, pg); err != nil {
				pg.Close()
				return nil, fmt.Errorf("apply schema: %w", err)
			}
		}
		log.Info("connected to postgres")
		return pg, nil
	}
	return nil, fmt.Errorf("unknown store backend %q", cfg.Store.Backend)
}

// buildIngress constructs and binds every listener.
func (n *Node) buildIngress() ([]ingress.Ingress, map[string]*ingress.TCP, error) {
	cfg, log := n.Config, n.Log
	var out []ingress.Ingress
	tcps := map[string]*ingress.TCP{}
	matips := map[string]*ingress.MATIP{}
	for _, ic := range cfg.Ingress {
		switch ic.Type {
		case "tcp":
			t, err := ingress.NewTCP(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := t.Listen(); err != nil {
				return nil, nil, err
			}
			tcps[ic.Name] = t
			out = append(out, t)
		case "matip":
			mp, err := ingress.NewMATIP(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := mp.Listen(); err != nil {
				return nil, nil, err
			}
			n.matip[ic.Name] = mp
			out = append(out, mp)
		case "https":
			h, err := ingress.NewHTTPS(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := h.Listen(); err != nil {
				return nil, nil, err
			}
			out = append(out, h)
		case "filedrop":
			f, err := ingress.NewFileDrop(ic, log)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, f)
		}
	}
	// Replies on a MATIP session go back down that session, so those listeners
	// join the same reply path as the plain TCP ones.
	for name, mp := range matips {
		n.matip[name] = mp
	}
	return out, tcps, nil
}

// registerPeers wires each configured partner into routing and egress.
func (n *Node) registerPeers() error {
	for _, p := range n.Config.Peers {
		if err := n.registerPeer(p); err != nil {
			return err
		}
	}
	return nil
}

// ReloadPeers adds the peers in the new list that this node does not have
// yet, without a restart: a partner onboarded while the links stay up.
// Peers already configured are left as they are; removing one is a restart,
// because a link that is open is a promise.
func (n *Node) ReloadPeers(peers []config.Peer) (added int, err error) {
	for _, p := range peers {
		if n.Gateway.Peer(p.Name) != nil {
			continue
		}
		if err := n.registerPeer(p); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

func (n *Node) registerPeer(p config.Peer) error {
	if p.RateLimit > 0 {
		for _, in := range n.listeners {
			if l, ok := in.(interface{ SetPeerLimit(string, float64, int) }); ok {
				l.SetPeerLimit(p.Name, p.RateLimit, p.Burst)
			}
		}
	}
	gw, router, tcps, log := n.Gateway, n.Router, n.tcp, n.Log

	// Replies on an inbound link go back down whichever TCP listener currently
	// holds a session with that peer.
	sessions := func(ctx context.Context, peer string, raw []byte) error {
		for _, t := range tcps {
			if err := t.Send(ctx, peer, raw); err == nil {
				return nil
			}
		}
		for _, m := range n.matip {
			if err := m.Send(ctx, peer, raw); err == nil {
				return nil
			}
		}
		return fmt.Errorf("no open link to %q", peer)
	}

	format := store.FormatTypeB
	switch p.Format {
	case "edifact":
		format = store.FormatEDIFACT
	case "aftn":
		format = store.FormatAFTN
	}
	gw.AddPeer(&gateway.Peer{
		Name: p.Name, Carrier: p.Carrier, Format: format,
		TTYAddress: p.TTYAddress, Addresses: p.Addresses, CONTRL: p.CONTRL,
		ICAO: p.ICAO, AFTN: p.AFTN,
	})
	s, err := egress.BuildWith(p, sessions, router, log)
	if err != nil {
		return err
	}
	router.Register(p.Name, s, p.Egress.Retry, format)
	log.Info("peer configured", "name", p.Name, "carrier", p.Carrier,
		"format", p.Format, "egress", s.Describe())
	return nil
}

// makeHandler builds the function every ingress calls.
// Handler is the ingress callback: it is what turns bytes off a link into a
// stored, parsed, applied message.
func (n *Node) Handler() ingress.Handler {
	gw, sp := n.Gateway, n.Spool
	return func(ctx context.Context, m ingress.Message) (ingress.Receipt, error) {
		start := time.Now()
		defer func() {
			metrics.Observe("jetway_ingest_seconds", "time to accept an inbound message",
				metrics.Labels{"transport": m.Transport}, time.Since(start).Seconds())
		}()

		// A spool decouples acknowledging the partner from the store being up.
		// Synchronous exchanges cannot use it: the caller is holding the
		// connection open waiting for a reply that only processing produces.
		if sp != nil && !m.Synchronous {
			e := spool.Entry{
				ID: gateway.NewMessageID(), Peer: m.Peer, Transport: m.Transport,
				At: time.Now().UTC(), Raw: m.Raw,
			}
			if err := sp.Put(e); err != nil {
				return ingress.Receipt{}, fmt.Errorf("spool: %w", err)
			}
			return ingress.Receipt{ID: e.ID}, nil
		}

		res, err := gw.IngestWith(ctx, m.Peer, m.Raw, gateway.IngestOptions{
			Transport: m.Transport, Remote: m.Remote,
			HoldReply: m.Synchronous, FromFile: m.FromFile,
		})
		if err != nil {
			return ingress.Receipt{}, err
		}
		return ingress.Receipt{ID: res.MessageID, Reply: res.Reply}, nil
	}
}

// drainSpool moves spooled messages into the pipeline.
func (n *Node) drainSpool(ctx context.Context, interval time.Duration) {
	sp, gw, log := n.Spool, n.Gateway, n.Log
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ids, err := sp.List()
		if err != nil {
			log.Error("could not list the spool", "err", err)
		}
		progress := false
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			e, err := sp.Get(id)
			if err != nil {
				log.Error("could not read a spooled message", "id", id, "err", err)
				continue
			}
			if _, err := gw.IngestWith(ctx, e.Peer, e.Raw, gateway.IngestOptions{
				Transport: e.Transport, Remote: "spool",
			}); err != nil {
				// The store is still unhappy. Leave the entry where it is; the
				// bytes are safe and the next sweep tries again.
				log.Warn("spooled message not yet accepted, will retry",
					"id", id, "attempts", e.Attempts, "err", err)
				break
			}
			if err := sp.Done(id); err != nil {
				log.Error("could not clear a drained spool entry", "id", id, "err", err)
			}
			progress = true
		}
		if progress {
			continue // keep going while the backlog is moving
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (n *Node) startDemo(ctx context.Context) (*demo.RunningFleet, error) {
	cfg, listeners, bus, log := n.Config, n.listeners, n.Bus, n.Log
	if !cfg.Demo.Carriers {
		return nil, nil
	}
	// Each simulated carrier dials the listener configured to identify it,
	// which is what a real partner circuit looks like.
	byName := map[string]ingress.Ingress{}
	for _, in := range listeners {
		byName[in.Name()] = in
	}
	peerListener := map[string]string{}
	for _, ic := range cfg.Ingress {
		if ic.Type != "tcp" || ic.Identify.Peer == "" {
			continue
		}
		if in, ok := byName[ic.Name]; ok {
			peerListener[ic.Identify.Peer] = in.Addr()
		}
	}
	addrFor := func(c demo.Carrier) string {
		if a, ok := cfg.Demo.LinkAddrs[c.Designator]; ok {
			return a
		}
		return peerListener[c.Designator]
	}
	return demo.StartFleet(ctx, demo.Fleet, addrFor, bus, log)
}

// registerRuntimeMetrics exposes the numbers worth alerting on.
func (n *Node) registerRuntimeMetrics() {
	sp, router := n.Spool, n.Router
	metrics.Default.OnCollect(func() {
		metrics.Gauge("jetway_egress_retry_queue", "messages awaiting redelivery", nil,
			float64(router.QueueDepth()))
		if sp == nil {
			return
		}
		// A rising spool means the store is not keeping up, or is down. It is
		// the single most important number here.
		if n, err := sp.Depth(); err == nil {
			metrics.Gauge("jetway_spool_depth", "inbound messages not yet persisted", nil, float64(n))
		}
		if age, ok, err := sp.Oldest(); err == nil && ok {
			metrics.Gauge("jetway_spool_oldest_seconds", "age of the oldest unpersisted message",
				nil, age.Seconds())
		}
	})
}

// purgeAvailability drops beliefs that have gone stale or whose flight has
// departed, so the cache does not grow without bound.
func (n *Node) purgeAvailability(ctx context.Context) {
	gw, log := n.Gateway, n.Log
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := gw.Avail.Purge(); n > 0 {
				log.Debug("purged stale availability", "entries", n)
			}
		}
	}
}
