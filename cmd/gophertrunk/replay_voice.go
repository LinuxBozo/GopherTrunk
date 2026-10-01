package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
	"github.com/MattCheramie/GopherTrunk/internal/voice"
	"github.com/MattCheramie/GopherTrunk/internal/voice/composer"
)

// replayVoiceSerial is the single voice device serial the replay rig exposes.
const replayVoiceSerial = "replay-voice:1"

// replayVoiceSource is the same-carrier voice device the replay rig binds every
// grant to. It streams the control carrier's own channelized IQ (fed from
// siglab's OnChannelIQ tap) to each following voice chain — the offline analogue
// of ccdecoder.CCVoiceSource, which the daemon uses for the same job. Because a
// conventional capture decodes ONE carrier, every grant belongs to it, so the
// source tunes anywhere (CanTune always true).
//
// Backpressure: push blocks on each subscriber's channel, so a fast file replay
// is paced to the (real-time-cost) voice chain instead of overrunning it.
//
// Pre-roll (issue #1187): a file decodes far faster than real time, so in the
// wall-clock moment between the grant and the composer's chain subscribing,
// the decode used to race on and every chunk pushed in between was DROPPED —
// the call's opening (the headers that triggered the grant, DMR's Privacy
// Indicator header that names the key, P25's first LDUs) and, on a short over,
// the whole call. Live that window is milliseconds; in replay it was the call.
// Two things close it: the source keeps the last replayVoicePrerollSeconds of
// IQ and hands it to each new subscriber first (the daemon's DMO voice tap does
// the same, ccdecoder.voiceFanout), and once the engine has started a call on
// this device (CallStart) push waits — bounded by replayVoiceSubscribeWait —
// for that call's chain to subscribe instead of racing on. A grant the engine
// does not serve starts no call and so never holds the decode. A new chain's
// pre-roll can carry up to a second of the previous over's tail when calls
// follow each other that closely.
type replayVoiceSource struct {
	rateBits atomic.Uint64 // float64 DDC rate, learned from the first push

	mu     sync.Mutex
	next   int
	subs   map[int]*replayVoiceSub
	ring   []complex64   // last replayVoicePrerollSeconds of IQ, oldest first
	starts uint64        // calls the engine started on this device
	joins  uint64        // chains that subscribed (or were given up on)
	subbed chan struct{} // closed (and replaced) whenever a chain subscribes
}

const (
	// replayVoicePrerollSeconds is how much IQ a new chain receives from
	// before its subscription: 1 s, the length the DMO pre-roll measured best.
	replayVoicePrerollSeconds = 1.0
	// replayVoiceSubscribeWait bounds how long push holds the decode after a
	// call start waiting for its chain to subscribe (a protocol with no
	// IQ-consuming chain never subscribes; the decode then carries on).
	replayVoiceSubscribeWait = time.Second
)

type replayVoiceSub struct {
	ch   chan []complex64
	done chan struct{}
}

func newReplayVoiceSource(rateHz float64) *replayVoiceSource {
	s := &replayVoiceSource{subs: make(map[int]*replayVoiceSub), subbed: make(chan struct{})}
	s.rateBits.Store(math.Float64bits(rateHz))
	return s
}

func (s *replayVoiceSource) Serial() string { return replayVoiceSerial }

// SetCenterFreq is a no-op: the carrier is already tuned by the replayed capture.
func (s *replayVoiceSource) SetCenterFreq(uint32) error { return nil }

// CanTune accepts any grant — a replay decodes a single carrier, so every grant
// it produces belongs to that carrier.
func (s *replayVoiceSource) CanTune(uint32) bool { return true }

func (s *replayVoiceSource) rate() float64 { return math.Float64frombits(s.rateBits.Load()) }

func (s *replayVoiceSource) SampleRateHz() uint32       { return uint32(s.rate() + 0.5) }
func (s *replayVoiceSource) SampleRateExactHz() float64 { return s.rate() }

// StreamIQ registers a subscriber for the carrier IQ until ctx ends (the composer
// cancels it at call end).
func (s *replayVoiceSource) StreamIQ(ctx context.Context) (<-chan []complex64, error) {
	sub := &replayVoiceSub{ch: make(chan []complex64, 16), done: make(chan struct{})}
	s.mu.Lock()
	id := s.next
	s.next++
	if len(s.ring) > 0 {
		// The pre-roll goes first; the channel is fresh, so this never blocks.
		sub.ch <- append([]complex64(nil), s.ring...)
	}
	s.subs[id] = sub
	s.joins++
	close(s.subbed)
	s.subbed = make(chan struct{})
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
		close(sub.done) // unblocks any in-flight push targeting this sub
	}()
	return sub.ch, nil
}

// expectSubscriber notes that the engine started a call on this device: until
// its chain subscribes (or replayVoiceSubscribeWait passes) push holds the
// decode rather than letting chunks fall through with no consumer. Counting
// starts against joins keeps the order of the two events irrelevant — the
// chain may subscribe before the bus listener reports the start.
func (s *replayVoiceSource) expectSubscriber() {
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
}

// push fans one channelized IQ chunk to every following voice chain, copying it
// (siglab reuses the backing array) and blocking until each consumer takes it
// (backpressure — see the type comment). A sub cancelled mid-send is skipped via
// its done channel rather than deadlocking. With no subscriber the chunk only
// joins the pre-roll ring — after a call start, once its chain has subscribed
// or the wait expired.
func (s *replayVoiceSource) push(iq []complex64, rateHz float64) {
	if rateHz > 0 {
		s.rateBits.Store(math.Float64bits(rateHz))
	}
	s.mu.Lock()
	if len(s.subs) == 0 && s.starts > s.joins {
		subbed := s.subbed
		s.mu.Unlock()
		select {
		case <-subbed:
		case <-time.After(replayVoiceSubscribeWait):
		}
		s.mu.Lock()
		if s.joins < s.starts {
			s.joins = s.starts // give up on the missing chain(s)
		}
	}
	if len(s.subs) == 0 {
		s.appendRing(iq)
		s.mu.Unlock()
		return
	}
	s.appendRing(iq)
	subs := make([]*replayVoiceSub, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	cp := make([]complex64, len(iq))
	copy(cp, iq)
	for _, sub := range subs {
		select {
		case sub.ch <- cp:
		case <-sub.done:
		}
	}
}

// appendRing keeps the newest replayVoicePrerollSeconds of IQ. Caller holds mu.
func (s *replayVoiceSource) appendRing(iq []complex64) {
	limit := int(s.rate() * replayVoicePrerollSeconds)
	if limit <= 0 {
		return
	}
	s.ring = append(s.ring, iq...)
	if over := len(s.ring) - limit; over > 0 {
		s.ring = append(s.ring[:0], s.ring[over:]...)
	}
}

// replayVoiceDevices resolves the composer's device lookups to the single replay
// voice source.
type replayVoiceDevices struct{ src *replayVoiceSource }

func (d replayVoiceDevices) FindBySerial(serial string) composer.IQSource {
	if serial == d.src.Serial() {
		return d.src
	}
	return nil
}

// replayVoiceRig wires a trunking engine + voice composer + recorder onto a
// siglab decode's grant bus and channelized-IQ tap, so `replay -record-voice`
// turns a capture's grants into .wav/.raw/.json recordings using the exact
// production voice path (VoicePool.Bind → composer voice chains → recorder).
type replayVoiceRig struct {
	bus      *events.Bus
	src      *replayVoiceSource
	engine   *trunking.Engine
	composer *composer.Composer
	recorder *voice.Recorder
	cancel   context.CancelFunc
	hangtime time.Duration
	wg       sync.WaitGroup
}

// setupReplayVoice builds and starts the engine/composer/recorder rig writing to
// outDir. rateHz is the expected channelized (DDC output) rate; the source also
// refines it from the first IQ chunk.
//
// An empty outDir runs the recorder in decode-only mode (no files are written)
// — the `-audio-out`-without-`-record-voice` shape, where the caller only wants
// the live PCM stream. audio, when non-nil, receives every decoded call's PCM
// as a continuous raw s16le stream (issue #314): it is fanned into the
// composer's main sink (analog FM chains write PCM there) AND wired as the
// recorder's decoded-PCM tap (digital protocols emit raw vocoder frames that
// only the recorder decodes — without the tap, digital audio never reaches a
// WritePCM-only sink; the same wiring the daemon uses for live browser audio).
//
// keys, when non-nil, is the decryption KeyResolver (`replay -key`, issue
// #1187): the composer then descrambles DMR Enhanced Privacy and P25 ADP
// calls exactly as the daemon does with trunking.systems[].encryption_keys.
func setupReplayVoice(outDir string, rateHz float64, hangtime time.Duration, audio *pcmStreamWriter, keys composer.KeyResolver, log *slog.Logger) (*replayVoiceRig, error) {
	bus := events.NewBus(1024)
	src := newReplayVoiceSource(rateHz)

	pool := trunking.NewVoicePool([]*trunking.VoiceDevice{{Tuner: src, Serial: src.Serial()}})
	engine, err := trunking.NewEngine(trunking.EngineOptions{
		Bus:        bus,
		Log:        log,
		VoicePool:  pool,
		Talkgroups: trunking.NewTalkgroupDB(),
		ScanMode:   trunking.ScanModeAll,
	})
	if err != nil {
		bus.Close()
		return nil, fmt.Errorf("replay voice: engine: %w", err)
	}
	// An empty outDir puts the recorder in its decode-only mode: every call
	// still builds its per-protocol vocoder (digital voice still decodes and
	// reaches the decoded-PCM tap below), it just never writes WAV/raw/json.
	rec, err := voice.NewRecorder(voice.RecorderOptions{
		Bus:                bus,
		Log:                log,
		OutDir:             outDir,
		SampleRate:         8000,
		WriteRaw:           outDir != "",
		WriteCallJSON:      outDir != "",
		VocoderForProtocol: voice.DefaultVocoderForProtocol(),
	})
	if err != nil {
		bus.Close()
		return nil, fmt.Errorf("replay voice: recorder: %w", err)
	}
	// The composer type-asserts its sink for the raw-frame / drain-coordination
	// extensions, so the multi-sink shape must be the daemon's fanoutSink (which
	// forwards them to the recorder) — a naive tee would silently drop every
	// IMBE/AMBE frame (the issue #356 failure class).
	var sink composer.PCMSink = rec
	if audio != nil {
		sink = fanoutSink{rec, audio}
		rec.SetDecodedPCMSink(audio)
	}
	comp, err := composer.New(composer.Options{
		Bus:           bus,
		Devices:       replayVoiceDevices{src: src},
		Sink:          sink,
		Engine:        engine,
		Log:           log,
		IQSampleRate:  uint32(rateHz + 0.5),
		PCMSampleRate: 8000,
		VoiceHangtime: hangtime,
		KeyResolver:   keys,
	})
	if err != nil {
		bus.Close()
		return nil, fmt.Errorf("replay voice: composer: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rig := &replayVoiceRig{
		bus: bus, src: src, engine: engine, composer: comp, recorder: rec,
		cancel: cancel, hangtime: hangtime,
	}
	// Every call the engine starts on the replay device arms the source's
	// wait-for-subscriber (see replayVoiceSource): the decode holds until the
	// call's chain is listening.
	grants := bus.Subscribe()
	rig.wg.Add(1)
	go func() {
		defer rig.wg.Done()
		defer grants.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-grants.C:
				if !ok {
					return
				}
				if cs, isStart := ev.Payload.(trunking.CallStart); ev.Kind == events.KindCallStart && isStart && cs.DeviceSerial == src.Serial() {
					src.expectSubscriber()
				}
			}
		}
	}()
	for _, r := range []func(context.Context) error{rec.Run, comp.Run, engine.Run} {
		run := r
		rig.wg.Add(1)
		go func() { defer rig.wg.Done(); _ = run(ctx) }()
	}
	return rig, nil
}

// onChannelIQ is the siglab tap: it forwards the channelized IQ to the voice
// source (blocking, so decode is paced to the voice chain).
func (r *replayVoiceRig) onChannelIQ(iq []complex64, rateHz float64) { r.src.push(iq, rateHz) }

// finalize lets any in-flight call reach its hangtime end (so its recording is
// finalized), then tears the rig down and flushes.
func (r *replayVoiceRig) finalize() {
	// No more IQ arrives after decode EOF; the composer's boundary tracker ends
	// the active call one hangtime later (wall clock), which publishes CallEnd and
	// finalizes the recording. Wait that out with margin before tearing down.
	time.Sleep(r.hangtime + 2*time.Second)
	_ = r.composer.Close() // cancels chains, draining tails (drain coordination)
	r.cancel()             // stop engine/composer/recorder Run loops
	r.bus.Close()
	_ = r.recorder.Close() // flushPendingFinalize writes any still-pending recording
	r.wg.Wait()
}
