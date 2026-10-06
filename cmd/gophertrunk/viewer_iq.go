package main

import (
	"context"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/sdr"
	"github.com/MattCheramie/GopherTrunk/internal/sdr/iqtap"
)

// viewerIQFunc is the hook the live-view providers (spectrum, symbol,
// diag, mixer, siglab capture) call before Subscribing to an SDR's iqtap
// broker. It returns a release func the provider MUST call when the view
// ends. nil means "no hook" (tests, CLI tools): the provider just
// Subscribes, as before.
type viewerIQFunc func(serial string) (release func())

// viewerPump is one on-demand StreamIQ session the daemon drives on behalf
// of live viewers. refs counts the attached viewers; the pump stops when the
// last one leaves.
type viewerPump struct {
	refs   int
	cancel context.CancelFunc
	done   chan struct{}
}

// viewerPumpPreemptWait bounds how long a decoder claiming a serial waits
// for a viewer pump's stream to wind down before opening its own.
const viewerPumpPreemptWait = 2 * time.Second

// acquireViewerIQ makes sure an SDR a live viewer is about to Subscribe to
// is actually streaming.
//
// An iqtap broker only fans IQ out to subscribers while a primary StreamIQ
// session runs. Trunking, wideband and single-channel decoders provide one,
// but an SDR nothing decodes from — e.g. a config with no trunking systems,
// which is what a fresh config-builder config looks like — never streams, so
// the Spectrum waterfall, the constellation/symbol scopes and Signal Lab
// capture all sat on a silent subscription with no frames and no error. The
// stock example config hid this because its example P25 system makes the CC
// decoder stream the control SDR.
//
// When the serial has no primary, the daemon drives StreamIQ itself and
// discards the primary channel (the broker's fan-out feeds the viewers)
// until the last viewer releases. It deliberately leaves alone:
//   - serials with a primary (iqPrimary): the viewer already gets frames;
//   - role: voice SDRs: the composer streams those through the raw pool
//     device, not the broker, so a viewer pump would collide with the next
//     voice call ("stream already active");
//   - the conventional scanner's SDR: the scanner owns that stream.
//
// A single-channel decoder that claims the serial later preempts the pump
// (openSingleChannelIQ) and the viewers keep receiving its fan-out.
func (d *Daemon) acquireViewerIQ(serial string) func() {
	if d.pool == nil {
		return func() {}
	}
	e := d.pool.FindBySerial(serial)
	if e == nil {
		return func() {}
	}
	return d.acquireViewerIQForRole(serial, e.Role)
}

// acquireViewerIQForRole is acquireViewerIQ with the pool lookup resolved.
func (d *Daemon) acquireViewerIQForRole(serial string, role sdr.Role) func() {
	noop := func() {}
	br := d.iqBrokers[serial]
	if br == nil || role == sdr.RoleVoice {
		return noop
	}

	d.iqPrimaryMu.Lock()
	defer d.iqPrimaryMu.Unlock()
	if d.iqPrimary[serial] || d.scannerBrokers[serial] != nil {
		return noop
	}
	if d.viewerPumps == nil {
		d.viewerPumps = make(map[string]*viewerPump)
	}
	p := d.viewerPumps[serial]
	if p == nil {
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := br.StreamIQ(ctx)
		if err != nil {
			cancel()
			d.log.Warn("sdr: could not start the IQ stream for a live view (no decoder is streaming this SDR)",
				"serial", serial, "err", err)
			return noop
		}
		p = &viewerPump{cancel: cancel, done: make(chan struct{})}
		go func() {
			defer close(p.done)
			for range ch {
				// The broker's fan-out already copied this chunk to every
				// subscriber; the primary copy has no consumer.
			}
			// The stream ended on its own (device error / USB unplug) or was
			// cancelled. Drop a still-registered entry so the next view
			// starts a fresh stream instead of joining a dead one.
			d.iqPrimaryMu.Lock()
			if d.viewerPumps[serial] == p {
				delete(d.viewerPumps, serial)
				p.cancel()
			}
			d.iqPrimaryMu.Unlock()
		}()
		d.viewerPumps[serial] = p
		d.log.Info("sdr: no decoder is streaming this SDR — streaming it for the live view",
			"serial", serial, "center_hz", br.CenterHz())
	}
	p.refs++
	released := false
	return func() {
		d.iqPrimaryMu.Lock()
		defer d.iqPrimaryMu.Unlock()
		if released {
			return
		}
		released = true
		// The pump may already have been preempted by a decoder (or stopped
		// at shutdown); only the live entry is refcounted.
		if d.viewerPumps[serial] != p {
			return
		}
		p.refs--
		if p.refs <= 0 {
			delete(d.viewerPumps, serial)
			p.cancel()
			d.log.Info("sdr: last live view closed — stopping its IQ stream", "serial", serial)
		}
	}
}

// takeViewerPumpLocked removes and cancels serial's viewer pump, if any, and
// returns it so the caller can wait for it outside the lock. Caller holds
// iqPrimaryMu.
func (d *Daemon) takeViewerPumpLocked(serial string) *viewerPump {
	p := d.viewerPumps[serial]
	if p == nil {
		return nil
	}
	delete(d.viewerPumps, serial)
	p.cancel()
	return p
}

// waitViewerPump waits (bounded) for a cancelled pump's stream to end.
func waitViewerPump(p *viewerPump) {
	if p == nil {
		return
	}
	select {
	case <-p.done:
	case <-time.After(viewerPumpPreemptWait):
	}
}

// streamIQAfterPreempt opens br's primary stream after a viewer pump on the
// same device was cancelled. The broker's goroutine can return on ctx
// cancel a moment before the driver has released the device, so the first
// open may still see the old stream; retry briefly before giving up.
func streamIQAfterPreempt(ctx context.Context, br *iqtap.Broker) (<-chan []complex64, error) {
	var lastErr error
	for i := 0; i < 10; i++ {
		ch, err := br.StreamIQ(ctx)
		if err == nil {
			return ch, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, lastErr
}

// stopViewerPumps cancels every viewer pump (daemon shutdown).
func (d *Daemon) stopViewerPumps() {
	d.iqPrimaryMu.Lock()
	pumps := make([]*viewerPump, 0, len(d.viewerPumps))
	for serial := range d.viewerPumps {
		pumps = append(pumps, d.takeViewerPumpLocked(serial))
	}
	d.iqPrimaryMu.Unlock()
	for _, p := range pumps {
		waitViewerPump(p)
	}
}

// acquire runs the hook when one is wired; otherwise it is a no-op.
func (f viewerIQFunc) acquire(serial string) func() {
	if f == nil {
		return func() {}
	}
	return f(serial)
}
