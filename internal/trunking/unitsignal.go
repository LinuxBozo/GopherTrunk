package trunking

import "time"

// Unit-signalling events — the per-radio control-channel traffic beyond
// grants and affiliations, decoded from P25 TSBKs (phase1/opcodes_unit.go).
// They are what SDRTrunk's Events view shows as STATUS / MESSAGE / PAGE /
// ACKNOWLEDGE / QUEUED / DENY / RADIO CHECK / INHIBIT rows and what an
// alert rule can watch (a radio inhibit or a deny on a dispatch talkgroup is
// news). Field names are snake_case JSON so the web event feeds render them
// with the grant formatter.

// UnitStatus is the events.KindUnitStatus payload (P25 STS_UPDT).
type UnitStatus struct {
	System     string    `json:"system"`
	Protocol   string    `json:"protocol"`
	SourceID   uint32    `json:"source_id"`
	TargetID   uint32    `json:"target_id,omitempty"`
	UnitStatus uint8     `json:"unit_status"`
	UserStatus uint8     `json:"user_status"`
	At         time.Time `json:"at"`
}

// UnitMessage is the events.KindUnitMessage payload (P25 MSG_UPDT): a
// 16-bit short data message from a radio to a talkgroup.
type UnitMessage struct {
	System   string    `json:"system"`
	Protocol string    `json:"protocol"`
	SourceID uint32    `json:"source_id"`
	GroupID  uint32    `json:"group_id"`
	Message  uint16    `json:"message"`
	At       time.Time `json:"at"`
}

// CallAlert is the events.KindCallAlert payload (P25 CALL_ALRT): a page
// from one radio to another.
type CallAlert struct {
	System   string    `json:"system"`
	Protocol string    `json:"protocol"`
	SourceID uint32    `json:"source_id"`
	TargetID uint32    `json:"target_id"`
	At       time.Time `json:"at"`
}

// UnitResponse is the shared payload of events.KindUnitAck /
// KindUnitQueued / KindUnitDeny: the site's answer to a radio's request.
// Reason / ReasonName are set for queued and deny; WACN / SystemID for an
// extended acknowledge of a roaming unit.
type UnitResponse struct {
	System       string    `json:"system"`
	Protocol     string    `json:"protocol"`
	Response     string    `json:"response"` // "ack" | "queued" | "deny"
	TargetID     uint32    `json:"target_id"`
	SourceID     uint32    `json:"source_id,omitempty"`
	ServiceType  uint8     `json:"service_type"`
	ServiceName  string    `json:"service_name,omitempty"`
	Reason       uint8     `json:"reason,omitempty"`
	ReasonName   string    `json:"reason_name,omitempty"`
	WACN         uint32    `json:"wacn,omitempty"`
	SystemID     uint16    `json:"system_id,omitempty"`
	AdditionalHx string    `json:"additional_info_hex,omitempty"`
	At           time.Time `json:"at"`
}

// UnitFunction is the events.KindUnitFunction payload (P25 EXT_FNCT_CMD):
// radio check / inhibit / uninhibit / detach and their acks.
type UnitFunction struct {
	System       string    `json:"system"`
	Protocol     string    `json:"protocol"`
	Function     uint16    `json:"function"`
	FunctionName string    `json:"function_name"`
	Arguments    uint32    `json:"arguments"`
	SourceID     uint32    `json:"source_id,omitempty"` // the commanding unit (arguments) for radio-control functions
	TargetID     uint32    `json:"target_id"`
	At           time.Time `json:"at"`
}

// UnitMonitor is the events.KindUnitMonitor payload (P25 RAD_MON_CMD): a
// radio ordered to transmit (remote monitor).
type UnitMonitor struct {
	System       string    `json:"system"`
	Protocol     string    `json:"protocol"`
	SourceID     uint32    `json:"source_id"`
	TargetID     uint32    `json:"target_id"`
	TxMultiplier uint8     `json:"tx_multiplier"`
	At           time.Time `json:"at"`
}

// CallTranscript is the events.KindCallTranscript payload: the speech-to-text
// of one finished recording (internal/transcribe). Segment / CallStartedAt
// identify the over within a multi-transmission call.
type CallTranscript struct {
	System        string    `json:"system"`
	Protocol      string    `json:"protocol"`
	GroupID       uint32    `json:"group_id"`
	SourceID      uint32    `json:"source_id,omitempty"`
	FrequencyHz   uint32    `json:"frequency_hz,omitempty"`
	DeviceSerial  string    `json:"device_serial,omitempty"`
	CallStartedAt time.Time `json:"call_started_at"`
	Segment       int       `json:"segment"`
	AudioPath     string    `json:"audio_path"`
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
}
