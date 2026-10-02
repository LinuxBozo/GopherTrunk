package phase1

import (
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

// tsbkBits builds the 8-byte TSBK payload from (bitIndex, width, value)
// fields given in SDRTrunk's 96-bit TSBK bit numbering (payload = bits
// 16..79), MSB first — an encoder INDEPENDENT of the Assemble* inverses, so
// a layout mistake shared by parser and assembler cannot hide.
func tsbkBits(fields ...[3]uint64) [8]byte {
	var bits [96]uint8
	for _, f := range fields {
		start, width, val := f[0], f[1], f[2]
		for i := uint64(0); i < width; i++ {
			bit := (val >> (width - 1 - i)) & 1
			bits[start+i] = uint8(bit)
		}
	}
	var p [8]byte
	for i := 16; i < 80; i++ {
		p[(i-16)/8] |= bits[i] << (7 - uint((i-16)%8))
	}
	return p
}

// TestUnitSignallingLayoutsMatchSDRTrunk pins every parser against literal
// field positions from SDRTrunk's osp/*.java bit arrays.
func TestUnitSignallingLayoutsMatchSDRTrunk(t *testing.T) {
	// StatusUpdate: UNIT_STATUS 16-23, USER_STATUS 24-31, TARGET 32-55, SOURCE 56-79.
	su := ParseStatusUpdate(tsbkBits([3]uint64{16, 8, 0xA5}, [3]uint64{24, 8, 0x3C}, [3]uint64{32, 24, 0x00BEEF}, [3]uint64{56, 24, 0x0ABCDE}))
	if su != (StatusUpdate{UnitStatus: 0xA5, UserStatus: 0x3C, TargetID: 0x00BEEF, SourceID: 0x0ABCDE}) {
		t.Errorf("StatusUpdate = %+v", su)
	}
	// MessageUpdate: MESSAGE 16-31, TARGET 32-55, SOURCE 56-79.
	mu := ParseMessageUpdate(tsbkBits([3]uint64{16, 16, 0x1234}, [3]uint64{32, 24, 1001}, [3]uint64{56, 24, 70001}))
	if mu != (MessageUpdate{Message: 0x1234, TargetID: 1001, SourceID: 70001}) {
		t.Errorf("MessageUpdate = %+v", mu)
	}
	// CallAlert: RESERVED 16-31, TARGET 32-55, SOURCE 56-79.
	ca := ParseCallAlert(tsbkBits([3]uint64{16, 16, 0xFFFF}, [3]uint64{32, 24, 0x111111}, [3]uint64{56, 24, 0x222222}))
	if ca != (CallAlert{TargetID: 0x111111, SourceID: 0x222222}) {
		t.Errorf("CallAlert = %+v", ca)
	}
	// Deny / Queued: ADDITIONAL_INFORMATION_FLAG 16, SERVICE_TYPE 18-23, REASON 24-31, ADDITIONAL_INFO 32-55, TARGET 56-79.
	dr := ParseServiceResponse(tsbkBits([3]uint64{16, 1, 1}, [3]uint64{18, 6, 0x00}, [3]uint64{24, 8, 0x60}, [3]uint64{32, 24, 0x00ABCD}, [3]uint64{56, 24, 0x0F0F0F}))
	if dr != (ServiceResponse{AdditionalInfoValid: true, ServiceType: OpGroupVoiceChannelGrant, Reason: 0x60, AdditionalInfo: 0x00ABCD, TargetID: 0x0F0F0F}) {
		t.Errorf("Deny = %+v", dr)
	}
	if DenyReasonName(dr.Reason) != "site access denial" || QueuedReasonName(0x40) != "channel resources unavailable" {
		t.Errorf("reason names: %q / %q", DenyReasonName(dr.Reason), QueuedReasonName(0x40))
	}
	// The flag clear ⇒ additional info not valid; service type 0x04 = UU_V_CH_GRANT.
	qr := ParseServiceResponse(tsbkBits([3]uint64{18, 6, 0x04}, [3]uint64{24, 8, 0x2F}, [3]uint64{56, 24, 7}))
	if qr.AdditionalInfoValid || qr.ServiceType != OpUnitToUnitVoiceChannelGrant || qr.Reason != 0x2F || qr.TargetID != 7 {
		t.Errorf("Queued = %+v", qr)
	}
	// Ack extended: flags 16 & 17 set, SERVICE 18-23, WACN 24-43, SYSTEM 44-55, TARGET 56-79.
	ack := ParseAcknowledgeResponse(tsbkBits([3]uint64{16, 1, 1}, [3]uint64{17, 1, 1}, [3]uint64{18, 6, 0x2C}, [3]uint64{24, 20, 0xBEE00}, [3]uint64{44, 12, 0x123}, [3]uint64{56, 24, 0x00ABCD}))
	if !ack.AdditionalInfoValid || !ack.Extended || ack.ServiceType != OpUnitRegistrationResponse || ack.WACN != 0xBEE00 || ack.SystemID != 0x123 || ack.TargetID != 0x00ABCD || ack.SourceID != 0 {
		t.Errorf("Ack (extended) = %+v", ack)
	}
	// Ack additional-info only: SOURCE 32-55.
	ack2 := ParseAcknowledgeResponse(tsbkBits([3]uint64{16, 1, 1}, [3]uint64{18, 6, 0x28}, [3]uint64{32, 24, 0x0ABCDE}, [3]uint64{56, 24, 1}))
	if ack2.Extended || ack2.SourceID != 0x0ABCDE || ack2.WACN != 0 || ack2.TargetID != 1 {
		t.Errorf("Ack (source) = %+v", ack2)
	}
	// Ack plain: nothing in 32-55 decoded.
	ack3 := ParseAcknowledgeResponse(tsbkBits([3]uint64{18, 6, 0x28}, [3]uint64{32, 24, 0x0ABCDE}, [3]uint64{56, 24, 1}))
	if ack3.AdditionalInfoValid || ack3.SourceID != 0 || ack3.WACN != 0 {
		t.Errorf("Ack (plain) = %+v", ack3)
	}
	// ExtendedFunction: FUNCTION 16-31, ARGUMENTS 32-55, TARGET 56-79.
	ef := ParseExtendedFunctionCommand(tsbkBits([3]uint64{16, 16, 0x007F}, [3]uint64{32, 24, 0x0DDDDD}, [3]uint64{56, 24, 0x0EEEEE}))
	if ef != (ExtendedFunctionCommand{Function: ExtFnRadioInhibit, Arguments: 0x0DDDDD, TargetID: 0x0EEEEE}) {
		t.Errorf("ExtendedFunction = %+v", ef)
	}
	if ExtendedFunctionName(ef.Function) != "radio inhibit" || ExtendedFunctionName(0x0280) != "group regroup create ack" || ExtendedFunctionName(0x4242) != "function 0x4242" {
		t.Errorf("function names wrong")
	}
	// RadioUnitMonitor: RESERVED 16-29, TX_MULTIPLIER 30-31, SOURCE 32-55, TARGET 56-79.
	rm := ParseRadioUnitMonitorCommand(tsbkBits([3]uint64{16, 14, 0x3FFF}, [3]uint64{30, 2, 2}, [3]uint64{32, 24, 0x010203}, [3]uint64{56, 24, 0x040506}))
	if rm != (RadioUnitMonitorCommand{TxMultiplier: 2, SourceID: 0x010203, TargetID: 0x040506}) {
		t.Errorf("RadioUnitMonitor = %+v", rm)
	}
}

// TestUnitSignallingRoundTrips: Assemble* are exact inverses of Parse*.
func TestUnitSignallingRoundTrips(t *testing.T) {
	if in := (StatusUpdate{UnitStatus: 1, UserStatus: 2, TargetID: 3, SourceID: 4}); ParseStatusUpdate(AssembleStatusUpdate(in)) != in {
		t.Error("StatusUpdate round-trip")
	}
	if in := (MessageUpdate{Message: 0xBEEF, TargetID: 5, SourceID: 6}); ParseMessageUpdate(AssembleMessageUpdate(in)) != in {
		t.Error("MessageUpdate round-trip")
	}
	if in := (CallAlert{TargetID: 7, SourceID: 8}); ParseCallAlert(AssembleCallAlert(in)) != in {
		t.Error("CallAlert round-trip")
	}
	for _, in := range []ServiceResponse{{AdditionalInfoValid: true, ServiceType: 0x3F, Reason: 0xFF, AdditionalInfo: 0xFFFFFF, TargetID: 0xFFFFFF}, {ServiceType: 0x05, Reason: 1, TargetID: 9}} {
		if ParseServiceResponse(AssembleServiceResponse(in)) != in {
			t.Errorf("ServiceResponse round-trip %+v", in)
		}
	}
	for _, in := range []AcknowledgeResponse{
		{AdditionalInfoValid: true, Extended: true, ServiceType: 0x2C, WACN: 0xFFFFF, SystemID: 0xFFF, TargetID: 1},
		{AdditionalInfoValid: true, ServiceType: 0x28, SourceID: 0xABCDEF, TargetID: 2},
		{ServiceType: 0x00, TargetID: 3},
	} {
		if ParseAcknowledgeResponse(AssembleAcknowledgeResponse(in)) != in {
			t.Errorf("AcknowledgeResponse round-trip %+v", in)
		}
	}
	if in := (ExtendedFunctionCommand{Function: 0x0200, Arguments: 0x123456, TargetID: 0x654321}); ParseExtendedFunctionCommand(AssembleExtendedFunctionCommand(in)) != in {
		t.Error("ExtendedFunction round-trip")
	}
	if in := (RadioUnitMonitorCommand{TxMultiplier: 3, SourceID: 1, TargetID: 2}); ParseRadioUnitMonitorCommand(AssembleRadioUnitMonitorCommand(in)) != in {
		t.Error("RadioUnitMonitor round-trip")
	}
}

// TestControlChannelPublishesUnitSignalling drives each unit-signalling
// TSBK through the control channel and checks the bus event it publishes.
func TestControlChannelPublishesUnitSignalling(t *testing.T) {
	cases := []struct {
		name  string
		tsbk  TSBK
		kind  events.Kind
		check func(any) bool
	}{
		{"status", TSBK{Opcode: OpStatusUpdate, MFID: MFIDStandard, Payload: AssembleStatusUpdate(StatusUpdate{UnitStatus: 9, UserStatus: 4, SourceID: 70001, TargetID: 1})}, events.KindUnitStatus,
			func(p any) bool {
				u, ok := p.(trunking.UnitStatus)
				return ok && u.SourceID == 70001 && u.UnitStatus == 9 && u.UserStatus == 4 && u.System == "SIG"
			}},
		{"message", TSBK{Opcode: OpMessageUpdate, MFID: MFIDStandard, Payload: AssembleMessageUpdate(MessageUpdate{Message: 0x0102, TargetID: 1001, SourceID: 70002})}, events.KindUnitMessage,
			func(p any) bool {
				u, ok := p.(trunking.UnitMessage)
				return ok && u.GroupID == 1001 && u.Message == 0x0102 && u.SourceID == 70002
			}},
		{"call alert", TSBK{Opcode: OpCallAlert, MFID: MFIDStandard, Payload: AssembleCallAlert(CallAlert{TargetID: 70003, SourceID: 70004})}, events.KindCallAlert,
			func(p any) bool {
				u, ok := p.(trunking.CallAlert)
				return ok && u.TargetID == 70003 && u.SourceID == 70004
			}},
		{"ack", TSBK{Opcode: OpAcknowledgeResponse, MFID: MFIDStandard, Payload: AssembleAcknowledgeResponse(AcknowledgeResponse{AdditionalInfoValid: true, Extended: true, ServiceType: OpUnitRegistrationResponse, WACN: 0xBEE00, SystemID: 0x123, TargetID: 70005})}, events.KindUnitAck,
			func(p any) bool {
				u, ok := p.(trunking.UnitResponse)
				return ok && u.Response == "ack" && u.WACN == 0xBEE00 && u.SystemID == 0x123 && u.TargetID == 70005
			}},
		{"queued", TSBK{Opcode: OpQueuedResponse, MFID: MFIDStandard, Payload: AssembleServiceResponse(ServiceResponse{ServiceType: OpGroupVoiceChannelGrant, Reason: 0x40, TargetID: 70006})}, events.KindUnitQueued,
			func(p any) bool {
				u, ok := p.(trunking.UnitResponse)
				return ok && u.Response == "queued" && u.ReasonName == "channel resources unavailable" && u.TargetID == 70006
			}},
		{"deny", TSBK{Opcode: OpDenyResponse, MFID: MFIDStandard, Payload: AssembleServiceResponse(ServiceResponse{AdditionalInfoValid: true, ServiceType: OpGroupVoiceChannelGrant, Reason: 0x31, AdditionalInfo: 0x0003E9, TargetID: 70007})}, events.KindUnitDeny,
			func(p any) bool {
				u, ok := p.(trunking.UnitResponse)
				return ok && u.Response == "deny" && u.ReasonName == "target group not authorized for service" && u.AdditionalHx == "0003E9" && u.TargetID == 70007
			}},
		{"inhibit", TSBK{Opcode: OpExtendedFunctionCommand, MFID: MFIDStandard, Payload: AssembleExtendedFunctionCommand(ExtendedFunctionCommand{Function: ExtFnRadioInhibit, Arguments: 1, TargetID: 70008})}, events.KindUnitFunction,
			func(p any) bool {
				u, ok := p.(trunking.UnitFunction)
				return ok && u.FunctionName == "radio inhibit" && u.TargetID == 70008 && u.SourceID == 1
			}},
		{"monitor", TSBK{Opcode: OpRadioUnitMonitor, MFID: MFIDStandard, Payload: AssembleRadioUnitMonitorCommand(RadioUnitMonitorCommand{TxMultiplier: 1, SourceID: 70009, TargetID: 70010})}, events.KindUnitMonitor,
			func(p any) bool {
				u, ok := p.(trunking.UnitMonitor)
				return ok && u.SourceID == 70009 && u.TargetID == 70010 && u.TxMultiplier == 1
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := events.NewBus(16)
			defer bus.Close()
			sub := bus.Subscribe()
			defer sub.Close()
			cc := New(Options{Bus: bus, SystemName: "SIG"})
			cc.Process(buildLockedStreamWithTSBK(10, 0x705, DUIDTrunkingSignaling, c.tsbk), 0)
			deadline := time.After(3 * time.Second)
			for {
				select {
				case ev := <-sub.C:
					if ev.Kind != c.kind {
						continue
					}
					if !c.check(ev.Payload) {
						t.Fatalf("%s payload = %+v", c.kind, ev.Payload)
					}
					return
				case <-deadline:
					t.Fatalf("no %s published", c.kind)
				}
			}
		})
	}
}
