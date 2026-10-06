package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/sdr"
	"github.com/MattCheramie/GopherTrunk/internal/sdr/iqtap"
)

// closingStreamFake streams constant IQ and, like a real driver, closes its
// channel once the stream's ctx is cancelled — so a stopped stream is
// observable as the broker no longer Streaming.
type closingStreamFake struct{ serial string }

func (f *closingStreamFake) Info() sdr.Info             { return sdr.Info{Driver: "fake", Serial: f.serial} }
func (f *closingStreamFake) SetCenterFreq(uint32) error { return nil }
func (f *closingStreamFake) SetSampleRate(uint32) error { return nil }
func (f *closingStreamFake) SetGain(int) error          { return nil }
func (f *closingStreamFake) SetPPM(int) error           { return nil }
func (f *closingStreamFake) SetBiasTee(bool) error      { return nil }
func (f *closingStreamFake) Close() error               { return nil }
func (f *closingStreamFake) StreamIQ(ctx context.Context) (<-chan []complex64, error) {
	out := make(chan []complex64, 4)
	go func() {
		defer close(out)
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				chunk := make([]complex64, 64)
				for i := range chunk {
					chunk[i] = complex(1, 0)
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// newViewerTestDaemon builds the minimal Daemon acquireViewerIQForRole needs:
// one idle broker (nothing has called StreamIQ on it) for serial.
func newViewerTestDaemon(serial string) (*Daemon, *iqtap.Broker) {
	br := iqtap.New(&closingStreamFake{serial: serial}, 0, nil)
	_ = br.SetCenterFreq(162_550_000)
	_ = br.SetSampleRate(2_400_000)
	d := &Daemon{
		log:            slog.New(slog.DiscardHandler),
		iqPrimary:      map[string]bool{},
		scannerBrokers: map[string]*iqtap.Broker{},
		iqBrokers:      map[string]*iqtap.Broker{serial: br},
	}
	return d, br
}

func waitStreaming(t *testing.T, br *iqtap.Broker, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if br.Stats().Streaming == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("broker Streaming = %v, want %v", br.Stats().Streaming, want)
}

func firstFrame(t *testing.T, p *spectrumProvider, serial string, within time.Duration) (bool, func()) {
	t.Helper()
	frames, cleanup, err := p.OpenStream(context.Background(), serial, 64, 50)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	select {
	case _, ok := <-frames:
		return ok, cleanup
	case <-time.After(within):
		return false, cleanup
	}
}

// TestSpectrumViewStreamsIdleSDR is the Discord v1.2.3 report: a config with
// no trunking systems (what the config builder saves when none are set up)
// leaves the SDRs with no decoder, so nothing ever calls StreamIQ and the
// Spectrum waterfall sat empty — while the stock example config "worked"
// only because its example P25 system made the CC decoder stream the
// control SDR. A live view on an idle non-voice SDR must now get frames.
func TestSpectrumViewStreamsIdleSDR(t *testing.T) {
	const serial = "00000001"
	d, br := newViewerTestDaemon(serial)

	// Without the hook (the old wiring) an idle SDR produces nothing.
	bare := &spectrumProvider{brokers: d.iqBrokers, log: d.log}
	if got, cleanup := firstFrame(t, bare, serial, 300*time.Millisecond); got {
		cleanup()
		t.Fatal("precondition: an idle broker delivered a frame with no primary stream")
	} else {
		cleanup()
	}

	p := &spectrumProvider{brokers: d.iqBrokers, log: d.log,
		viewerIQ: func(s string) func() { return d.acquireViewerIQForRole(s, sdr.RoleControl) }}
	got, cleanup := firstFrame(t, p, serial, 2*time.Second)
	if !got {
		cleanup()
		t.Fatal("no spectrum frame from an idle control SDR — the live view did not start its IQ stream")
	}
	waitStreaming(t, br, true)

	// Closing the last view stops the stream again.
	cleanup()
	waitStreaming(t, br, false)
	if n := len(d.viewerPumps); n != 0 {
		t.Fatalf("viewer pumps left after the last view closed: %d", n)
	}
}

// TestViewerIQRefcountsViews: the pump runs while ANY view is open.
func TestViewerIQRefcountsViews(t *testing.T) {
	const serial = "dev-rc"
	d, br := newViewerTestDaemon(serial)
	r1 := d.acquireViewerIQForRole(serial, sdr.RoleControl)
	r2 := d.acquireViewerIQForRole(serial, sdr.RoleAuto)
	waitStreaming(t, br, true)
	r1()
	r1() // idempotent: must not drop the second view's reference
	time.Sleep(50 * time.Millisecond)
	if !br.Stats().Streaming {
		t.Fatal("stream stopped while a second view is still open")
	}
	r2()
	waitStreaming(t, br, false)
}

// TestViewerIQLeavesOwnedSDRsAlone: never open a stream on an SDR a
// decoder already owns, on a voice SDR (the composer streams those through
// the raw device, so a viewer stream would collide with the next call), or
// on the conventional scanner's SDR.
func TestViewerIQLeavesOwnedSDRsAlone(t *testing.T) {
	cases := []struct {
		name  string
		role  sdr.Role
		setup func(d *Daemon, serial string, br *iqtap.Broker)
	}{
		{"voice role", sdr.RoleVoice, func(*Daemon, string, *iqtap.Broker) {}},
		{"decoder primary", sdr.RoleControl, func(d *Daemon, s string, _ *iqtap.Broker) { d.iqPrimary[s] = true }},
		{"conventional scanner", sdr.RoleControl, func(d *Daemon, s string, br *iqtap.Broker) { d.scannerBrokers[s] = br }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const serial = "dev-owned"
			d, br := newViewerTestDaemon(serial)
			tc.setup(d, serial, br)
			release := d.acquireViewerIQForRole(serial, tc.role)
			defer release()
			time.Sleep(30 * time.Millisecond)
			if br.Stats().Streaming {
				t.Fatal("viewer opened a stream on an SDR it must not touch")
			}
			if len(d.viewerPumps) != 0 {
				t.Fatal("viewer pump registered for an owned SDR")
			}
		})
	}
}

// TestSingleChannelDecoderPreemptsViewerPump: a decoder that claims the
// SDR after a view started its stream takes the stream over (no "stream
// already active" collision), and the view keeps receiving IQ through the
// decoder's fan-out.
func TestSingleChannelDecoderPreemptsViewerPump(t *testing.T) {
	const serial = "dev-preempt"
	d, br := newViewerTestDaemon(serial)
	sub := br.Subscribe()
	defer sub.Close()
	release := d.acquireViewerIQForRole(serial, sdr.RoleControl)
	waitStreaming(t, br, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, cleanup, err := d.openSingleChannelIQ(ctx, br, serial)
	if err != nil {
		t.Fatalf("openSingleChannelIQ after a viewer pump: %v", err)
	}
	defer cleanup()
	if len(d.viewerPumps) != 0 {
		t.Fatal("viewer pump still registered after the decoder claimed the SDR")
	}
	select {
	case c := <-ch:
		if len(c) == 0 {
			t.Fatal("decoder got an empty chunk")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("decoder received no IQ after preempting the viewer pump")
	}

	// The view's release is now a no-op: it must not stop the decoder's stream.
	release()
	for len(sub.C) > 0 {
		<-sub.C
	}
	select {
	case <-sub.C:
	case <-time.After(2 * time.Second):
		t.Fatal("the open view stopped receiving IQ after the decoder took the stream over")
	}
	if !br.Stats().Streaming {
		t.Fatal("releasing the view stopped the decoder's stream")
	}
	// A new view on the now-owned SDR must not open a second stream.
	r2 := d.acquireViewerIQForRole(serial, sdr.RoleControl)
	defer r2()
	if len(d.viewerPumps) != 0 {
		t.Fatal("viewer pump started on an SDR a decoder owns")
	}
}
