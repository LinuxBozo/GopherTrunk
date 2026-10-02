package phase1

import "fmt"

// Unit-signalling TSBKs — the per-radio control-channel traffic beyond
// grants and affiliations: a radio's status / short message, a call alert
// (page), the site's acknowledge / queued / deny responses, radio-check /
// inhibit extended-function commands and the radio-unit-monitor command.
// SDRTrunk decodes and logs every one of these; GopherTrunk only counted
// them in the "unhandled tsbk" census.
//
// Layouts are pinned against SDRTrunk's bit-field constants
// (module/decode/p25/phase1/message/tsbk/standard/osp/*.java — bit indexes
// into the 96-bit TSBK; the 8-byte payload here is TSBK bits 16..79, so
// payload byte k, MSB first, is TSBK bits 16+8k .. 23+8k). The reason and
// function code tables are SDRTrunk's DenyReason / QueuedResponseReason /
// ExtendedFunction enums (TIA-102.AABC). Capture-UNCONFIRMED on GopherTrunk's
// own air (#764/#771): the parsers are reference-pinned by literal vectors
// built from those bit positions, not from a capture.

// StatusUpdate (opcode 0x18, STS_UPDT): a radio's unit status and the
// user-selected status it announced.
//
//	byte 0    : unit status (bits 16-23)
//	byte 1    : user status (bits 24-31)
//	bytes 2-4 : target address (bits 32-55)
//	bytes 5-7 : source address (bits 56-79)
type StatusUpdate struct {
	UnitStatus uint8
	UserStatus uint8
	TargetID   uint32
	SourceID   uint32
}

func ParseStatusUpdate(p [8]byte) StatusUpdate {
	return StatusUpdate{UnitStatus: p[0], UserStatus: p[1], TargetID: u24(p[2:5]), SourceID: u24(p[5:8])}
}

// AssembleStatusUpdate is the inverse; used by tests.
func AssembleStatusUpdate(s StatusUpdate) [8]byte {
	var p [8]byte
	p[0], p[1] = s.UnitStatus, s.UserStatus
	put24(p[2:5], s.TargetID)
	put24(p[5:8], s.SourceID)
	return p
}

// MessageUpdate (opcode 0x1C, MSG_UPDT): a 16-bit short data message from
// a radio to a talkgroup (SDRTrunk reads the target as a talkgroup).
//
//	bytes 0-1 : message (bits 16-31)
//	bytes 2-4 : target address (bits 32-55)
//	bytes 5-7 : source address (bits 56-79)
type MessageUpdate struct {
	Message  uint16
	TargetID uint32
	SourceID uint32
}

func ParseMessageUpdate(p [8]byte) MessageUpdate {
	return MessageUpdate{Message: uint16(p[0])<<8 | uint16(p[1]), TargetID: u24(p[2:5]), SourceID: u24(p[5:8])}
}

func AssembleMessageUpdate(m MessageUpdate) [8]byte {
	var p [8]byte
	p[0], p[1] = byte(m.Message>>8), byte(m.Message)
	put24(p[2:5], m.TargetID)
	put24(p[5:8], m.SourceID)
	return p
}

// CallAlert (opcode 0x1F, CALL_ALRT): a page from one radio to another
// ("call me back").
//
//	bytes 0-1 : reserved (bits 16-31)
//	bytes 2-4 : target address (bits 32-55)
//	bytes 5-7 : source id (bits 56-79)
type CallAlert struct {
	TargetID uint32
	SourceID uint32
}

func ParseCallAlert(p [8]byte) CallAlert {
	return CallAlert{TargetID: u24(p[2:5]), SourceID: u24(p[5:8])}
}

func AssembleCallAlert(c CallAlert) [8]byte {
	var p [8]byte
	put24(p[2:5], c.TargetID)
	put24(p[5:8], c.SourceID)
	return p
}

// ServiceResponse is the shared layout of the Queued (0x21) and Deny
// (0x27) responses: a flag saying whether the 24-bit additional-info field
// is present, the 6-bit service type (the opcode of the request being
// answered), an 8-bit reason, and the target radio.
//
//	byte 0    : bit 7 additional-info flag (bit 16), bit 6 reserved (17),
//	            bits 5-0 service type (bits 18-23)
//	byte 1    : reason (bits 24-31)
//	bytes 2-4 : additional info (bits 32-55; meaningful when the flag is set)
//	bytes 5-7 : target address (bits 56-79)
type ServiceResponse struct {
	AdditionalInfoValid bool
	ServiceType         Opcode
	Reason              uint8
	AdditionalInfo      uint32
	TargetID            uint32
}

func ParseServiceResponse(p [8]byte) ServiceResponse {
	return ServiceResponse{
		AdditionalInfoValid: p[0]&0x80 != 0,
		ServiceType:         Opcode(p[0] & 0x3F),
		Reason:              p[1],
		AdditionalInfo:      u24(p[2:5]),
		TargetID:            u24(p[5:8]),
	}
}

func AssembleServiceResponse(r ServiceResponse) [8]byte {
	var p [8]byte
	p[0] = byte(r.ServiceType) & 0x3F
	if r.AdditionalInfoValid {
		p[0] |= 0x80
	}
	p[1] = r.Reason
	put24(p[2:5], r.AdditionalInfo)
	put24(p[5:8], r.TargetID)
	return p
}

// DenyReasonName names a Deny Response reason code (TIA-102.AABC; the
// SDRTrunk DenyReason table). Unknown codes render as "reason 0xNN".
func DenyReasonName(code uint8) string {
	switch code {
	case 0x00:
		return "reserved"
	case 0x10:
		return "requesting unit not valid"
	case 0x11:
		return "requesting unit not authorized for service"
	case 0x20:
		return "target unit not valid"
	case 0x21:
		return "target unit not authorized for service"
	case 0x2F:
		return "target unit refused call"
	case 0x30:
		return "target group not valid"
	case 0x31:
		return "target group not authorized for service"
	case 0x40:
		return "invalid dialing"
	case 0x41:
		return "telephone number not authorized"
	case 0x42:
		return "PSTN not valid"
	case 0x50:
		return "call timeout"
	case 0x51:
		return "landline terminated call"
	case 0x52:
		return "subscriber unit terminated call"
	case 0x5F:
		return "call preempted"
	case 0x60:
		return "site access denial"
	case 0x61:
		return "user or system defined"
	case 0x67:
		return "PTT collide"
	case 0x77:
		return "PTT bonk"
	case 0xF0:
		return "call options not valid for service"
	case 0xF1:
		return "protection service option not valid"
	case 0xF2:
		return "duplex service option not valid"
	case 0xF3:
		return "circuit or packet mode option not valid"
	case 0xFF:
		return "system does not support service"
	}
	return fmt.Sprintf("reason 0x%02X", code)
}

// QueuedReasonName names a Queued Response reason code (SDRTrunk
// QueuedResponseReason table).
func QueuedReasonName(code uint8) string {
	switch code {
	case 0x00:
		return "reserved"
	case 0x10:
		return "requesting unit busy (other service)"
	case 0x20:
		return "target unit busy (other service)"
	case 0x2F:
		return "target unit queued for this call"
	case 0x30:
		return "target group currently active"
	case 0x40:
		return "channel resources unavailable"
	case 0x41:
		return "telephone resources unavailable"
	case 0x42:
		return "data resources unavailable"
	case 0x50:
		return "superseding service currently active"
	case 0x80:
		return "user or system defined"
	}
	return fmt.Sprintf("reason 0x%02X", code)
}

// AcknowledgeResponse (opcode 0x20, ACK_RSP_FNE): the site acknowledging a
// radio's request. Two flags select what bits 32-55 carry: with both the
// additional-info and extended flags set they hold the WACN (20 bits) and
// System ID (12 bits) of a roaming unit; with only the additional-info flag
// they hold a source address; otherwise nothing.
//
//	byte 0    : bit 7 additional-info flag (16), bit 6 extended flag (17),
//	            bits 5-0 service type (bits 18-23)
//	bytes 1-4 : WACN bits 24-43 + System bits 44-55 (extended) or
//	            bits 2-4 source address (bits 32-55)
//	bytes 5-7 : target address (bits 56-79)
type AcknowledgeResponse struct {
	AdditionalInfoValid bool
	Extended            bool
	ServiceType         Opcode
	WACN                uint32 // extended form
	SystemID            uint16 // extended form
	SourceID            uint32 // additional-info-only form
	TargetID            uint32
}

func ParseAcknowledgeResponse(p [8]byte) AcknowledgeResponse {
	a := AcknowledgeResponse{
		AdditionalInfoValid: p[0]&0x80 != 0,
		Extended:            p[0]&0x40 != 0,
		ServiceType:         Opcode(p[0] & 0x3F),
		TargetID:            u24(p[5:8]),
	}
	switch {
	case a.AdditionalInfoValid && a.Extended:
		a.WACN = uint32(p[1])<<12 | uint32(p[2])<<4 | uint32(p[3])>>4
		a.SystemID = uint16(p[3]&0x0F)<<8 | uint16(p[4])
	case a.AdditionalInfoValid:
		a.SourceID = u24(p[2:5])
	}
	return a
}

func AssembleAcknowledgeResponse(a AcknowledgeResponse) [8]byte {
	var p [8]byte
	p[0] = byte(a.ServiceType) & 0x3F
	if a.AdditionalInfoValid {
		p[0] |= 0x80
	}
	if a.Extended {
		p[0] |= 0x40
	}
	switch {
	case a.AdditionalInfoValid && a.Extended:
		p[1] = byte(a.WACN >> 12)
		p[2] = byte(a.WACN >> 4)
		p[3] = byte(a.WACN<<4)&0xF0 | byte(a.SystemID>>8)&0x0F
		p[4] = byte(a.SystemID)
	case a.AdditionalInfoValid:
		put24(p[2:5], a.SourceID)
	}
	put24(p[5:8], a.TargetID)
	return p
}

// ExtendedFunctionCommand (opcode 0x24, EXT_FNCT_CMD): radio check /
// inhibit / uninhibit / detach and their acks, plus group-regroup
// supergroup create / cancel.
//
//	bytes 0-1 : function (bits 16-31)
//	bytes 2-4 : arguments (bits 32-55) — the commanding unit's address for
//	            the radio-control functions
//	bytes 5-7 : target address (bits 56-79)
type ExtendedFunctionCommand struct {
	Function  uint16
	Arguments uint32
	TargetID  uint32
}

func ParseExtendedFunctionCommand(p [8]byte) ExtendedFunctionCommand {
	return ExtendedFunctionCommand{Function: uint16(p[0])<<8 | uint16(p[1]), Arguments: u24(p[2:5]), TargetID: u24(p[5:8])}
}

func AssembleExtendedFunctionCommand(e ExtendedFunctionCommand) [8]byte {
	var p [8]byte
	p[0], p[1] = byte(e.Function>>8), byte(e.Function)
	put24(p[2:5], e.Arguments)
	put24(p[5:8], e.TargetID)
	return p
}

// Extended function codes (TIA-102.AABC; SDRTrunk ExtendedFunction).
const (
	ExtFnRadioCheck        uint16 = 0x0000
	ExtFnRadioDetach       uint16 = 0x007D
	ExtFnRadioUninhibit    uint16 = 0x007E
	ExtFnRadioInhibit      uint16 = 0x007F
	ExtFnRadioCheckAck     uint16 = 0x0080
	ExtFnRadioDetachAck    uint16 = 0x00FD
	ExtFnRadioUninhibitAck uint16 = 0x00FE
	ExtFnRadioInhibitAck   uint16 = 0x00FF
	ExtFnRegroupCreate     uint16 = 0x0200
	ExtFnRegroupCancel     uint16 = 0x0201
	ExtFnRegroupCreateAck  uint16 = 0x0280
	ExtFnRegroupCancelAck  uint16 = 0x0281
)

// ExtendedFunctionName names an extended-function code.
func ExtendedFunctionName(fn uint16) string {
	switch fn {
	case ExtFnRadioCheck:
		return "radio check"
	case ExtFnRadioDetach:
		return "radio detach"
	case ExtFnRadioUninhibit:
		return "radio uninhibit"
	case ExtFnRadioInhibit:
		return "radio inhibit"
	case ExtFnRadioCheckAck:
		return "radio check ack"
	case ExtFnRadioDetachAck:
		return "radio detach ack"
	case ExtFnRadioUninhibitAck:
		return "radio uninhibit ack"
	case ExtFnRadioInhibitAck:
		return "radio inhibit ack"
	case ExtFnRegroupCreate:
		return "group regroup create supergroup"
	case ExtFnRegroupCancel:
		return "group regroup cancel supergroup"
	case ExtFnRegroupCreateAck:
		return "group regroup create ack"
	case ExtFnRegroupCancelAck:
		return "group regroup cancel ack"
	}
	return fmt.Sprintf("function 0x%04X", fn)
}

// RadioUnitMonitorCommand (opcode 0x1D, RAD_MON_CMD): the site orders a
// radio to key up (remote monitor); tx multiplier scales the transmit time.
//
//	bytes 0-1 : bits 16-29 reserved, bits 30-31 tx multiplier
//	bytes 2-4 : source address (bits 32-55) — the monitoring unit
//	bytes 5-7 : target address (bits 56-79) — the radio being opened
type RadioUnitMonitorCommand struct {
	TxMultiplier uint8
	SourceID     uint32
	TargetID     uint32
}

func ParseRadioUnitMonitorCommand(p [8]byte) RadioUnitMonitorCommand {
	return RadioUnitMonitorCommand{TxMultiplier: p[1] & 0x03, SourceID: u24(p[2:5]), TargetID: u24(p[5:8])}
}

func AssembleRadioUnitMonitorCommand(r RadioUnitMonitorCommand) [8]byte {
	var p [8]byte
	p[1] = r.TxMultiplier & 0x03
	put24(p[2:5], r.SourceID)
	put24(p[5:8], r.TargetID)
	return p
}

func u24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

func put24(b []byte, v uint32) { b[0], b[1], b[2] = byte(v>>16), byte(v>>8), byte(v) }
