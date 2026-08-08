package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// ProtocolVersion is the highest wire version this server speaks. v3 added the
	// media-header Layer byte and the BitrateHint control packet; it is negotiated
	// down to the client's version (see negotiateProtocol) because a strict v2
	// client rejects an unsolicited v3 Welcome.
	ProtocolVersion = 3

	// MediaHeaderSize is the fixed byte length of the binary media header. It is
	// also the AAD length for GCM (the whole header is authenticated).
	MediaHeaderSize = 24
	// FragHeaderSize is the byte length of the per-fragment header that follows the
	// media header on fragmented frames.
	FragHeaderSize = 12
	// MaxPacketSize is the largest datagram accepted/emitted (bytes on the wire).
	MaxPacketSize = 1500
	// MaxUDPPayload is the media payload budget per datagram before fragmentation,
	// leaving headroom under MaxPacketSize for headers and the GCM tag.
	MaxUDPPayload = 1200

	// The PacketType* values are the first byte of every packet and drive dispatch.
	PacketTypeHello           = 0x01
	PacketTypeWelcome         = 0x02
	PacketTypeAudio           = 0x03
	PacketTypeVideo           = 0x04
	PacketTypePing            = 0x05
	PacketTypePong            = 0x06
	PacketTypeBye             = 0x07
	PacketTypeSpeaking        = 0x08
	PacketTypeMediaState      = 0x09
	PacketTypeNack            = 0x0a // negative ack: request retransmit of sequences
	PacketTypePli             = 0x0b // picture-loss indication: request a keyframe
	PacketTypeRR              = 0x0c // receiver report: loss/jitter feedback
	PacketTypeParticipantLeft = 0x0d
	PacketTypeSubscribe       = 0x0e // receiver's set of SSRCs it wants forwarded
	PacketTypeQualityPref     = 0x0f // receiver's per-SSRC simulcast layer preference
	PacketTypeQualityReport   = 0x10
	PacketTypeBitrateHint     = 0x11 // v3: server tells a sender to change bitrate

	// Flag* are bits in the media-header Flags byte.
	FlagMarker   = 0x01 // frame boundary marker
	FlagKeyframe = 0x02 // video keyframe (pinned in the retransmit buffer)
	FlagMuted    = 0x04
	FlagSpeaking = 0x08

	// Codec* identify the payload codec in the media-header Codec byte.
	CodecOpus = 1
	CodecH264 = 2
	CodecVP8  = 3
	CodecVP9  = 4
	CodecAV1  = 5

	// Layer* are simulcast quality tiers carried in the media-header Layer byte,
	// ascending from lowest (Thumbnail) to highest (Large) resolution.
	LayerThumbnail = 0
	LayerSmall     = 1
	LayerMedium    = 2
	LayerLarge     = 3
)

var (
	// ErrInvalidPacket signals a structurally invalid packet.
	ErrInvalidPacket = errors.New("invalid packet")
	// ErrTooSmall is returned by the parse helpers when the buffer is shorter than
	// the fixed header/field they need.
	ErrTooSmall = errors.New("packet too small")
)

// MediaHeader is the fixed 24-byte binary header prefixing every audio/video
// packet. Counter is the per-SSRC GCM nonce counter, KeyID selects the crypto
// key, and Layer is the simulcast tier (v3). The whole marshaled header is the
// AEAD's AAD, so no field may change after encryption.
type MediaHeader struct {
	Type      uint8
	Flags     uint8
	KeyID     uint8
	Codec     uint8
	Sequence  uint16
	Timestamp uint32
	SSRC      uint32
	Counter   uint64
	Layer     uint8
}

// FragmentHeader describes one fragment of a frame split across datagrams.
// FrameID ties fragments together, FragIndex/FragCount give position and total,
// and FrameLength is the reassembled payload size.
type FragmentHeader struct {
	FrameID     uint32
	FragIndex   uint16
	FragCount   uint16
	FrameLength uint32
}

// Packet is a parsed packet. For media, RawAAD holds the exact header bytes to
// authenticate on decrypt and Payload is the ciphertext body; for control
// packets only Type and Payload (the JSON/binary body) are populated.
type Packet struct {
	Header   MediaHeader
	Fragment *FragmentHeader
	Payload  []byte
	RawAAD   []byte
	Type     uint8
}

// ParseMediaHeader decodes the fixed media header from data, returning ErrTooSmall
// if data is shorter than MediaHeaderSize. It does not copy: numeric fields are
// read big-endian.
func ParseMediaHeader(data []byte) (*MediaHeader, error) {
	if len(data) < MediaHeaderSize {
		return nil, ErrTooSmall
	}
	return &MediaHeader{
		Type:      data[0],
		Flags:     data[1],
		KeyID:     data[2],
		Codec:     data[3],
		Sequence:  binary.BigEndian.Uint16(data[4:6]),
		Timestamp: binary.BigEndian.Uint32(data[6:10]),
		SSRC:      binary.BigEndian.Uint32(data[10:14]),
		Counter:   binary.BigEndian.Uint64(data[14:22]),
		Layer:     data[22],
	}, nil
}

// Marshal serializes the header to a fresh MediaHeaderSize buffer (big-endian).
// The trailing byte (offset 23) is reserved and written as zero.
func (h *MediaHeader) Marshal() []byte {
	buf := make([]byte, MediaHeaderSize)
	buf[0] = h.Type
	buf[1] = h.Flags
	buf[2] = h.KeyID
	buf[3] = h.Codec
	binary.BigEndian.PutUint16(buf[4:6], h.Sequence)
	binary.BigEndian.PutUint32(buf[6:10], h.Timestamp)
	binary.BigEndian.PutUint32(buf[10:14], h.SSRC)
	binary.BigEndian.PutUint64(buf[14:22], h.Counter)
	buf[22] = h.Layer
	buf[23] = 0
	return buf
}

// IsKeyframe reports whether the FlagKeyframe bit is set (video keyframe).
func (h *MediaHeader) IsKeyframe() bool {
	return (h.Flags & FlagKeyframe) != 0
}

// ParseFragmentHeader decodes a fragment header, returning ErrTooSmall if data is
// shorter than FragHeaderSize.
func ParseFragmentHeader(data []byte) (*FragmentHeader, error) {
	if len(data) < FragHeaderSize {
		return nil, ErrTooSmall
	}

	return &FragmentHeader{
		FrameID:     binary.BigEndian.Uint32(data[0:4]),
		FragIndex:   binary.BigEndian.Uint16(data[4:6]),
		FragCount:   binary.BigEndian.Uint16(data[6:8]),
		FrameLength: binary.BigEndian.Uint32(data[8:12]),
	}, nil
}

// Marshal serializes the fragment header to a fresh FragHeaderSize buffer
// (big-endian).
func (f *FragmentHeader) Marshal() []byte {
	buf := make([]byte, FragHeaderSize)
	binary.BigEndian.PutUint32(buf[0:4], f.FrameID)
	binary.BigEndian.PutUint16(buf[4:6], f.FragIndex)
	binary.BigEndian.PutUint16(buf[6:8], f.FragCount)
	binary.BigEndian.PutUint32(buf[8:12], f.FrameLength)
	return buf
}

// ParsePacket dispatches on the first byte: audio/video are parsed as media
// packets (header + payload split out), and every other type is returned with the
// raw body in Payload for the caller to JSON-decode. Returns ErrTooSmall on empty
// input.
func ParsePacket(data []byte) (*Packet, error) {
	if len(data) < 1 {
		return nil, ErrTooSmall
	}

	switch data[0] {
	case PacketTypeAudio, PacketTypeVideo:
		return parseMediaPacket(data)
	default:
		return &Packet{
			Header:  MediaHeader{Type: data[0]},
			Payload: data[1:],
			Type:    data[0],
		}, nil
	}
}

// parseMediaPacket splits a media datagram into header and payload, copying the
// header bytes into RawAAD so they survive as the decrypt AAD even if data is
// later reused. Returns ErrTooSmall if the header is incomplete.
func parseMediaPacket(data []byte) (*Packet, error) {
	if len(data) < MediaHeaderSize {
		return nil, ErrTooSmall
	}

	header, err := ParseMediaHeader(data)
	if err != nil {
		return nil, err
	}

	packet := &Packet{
		Header: *header,
		RawAAD: make([]byte, MediaHeaderSize),
		Type:   header.Type,
	}
	copy(packet.RawAAD, data[:MediaHeaderSize])

	if len(data) > MediaHeaderSize {
		packet.Payload = data[MediaHeaderSize:]
	}

	return packet, nil
}

// Marshal serializes the packet as marshaled header followed by Payload. It does
// not emit the Fragment header; fragmented output is produced by
// FragmentMediaPacket instead.
func (p *Packet) Marshal() []byte {
	header := p.Header.Marshal()
	buf := make([]byte, len(header)+len(p.Payload))
	copy(buf, header)
	copy(buf[len(header):], p.Payload)
	return buf
}

// IsAudio reports whether the packet is an audio media packet.
func (p *Packet) IsAudio() bool { return p.Header.Type == PacketTypeAudio }

// IsVideo reports whether the packet is a video media packet.
func (p *Packet) IsVideo() bool { return p.Header.Type == PacketTypeVideo }

// IsKeyframe reports whether the header's keyframe flag is set.
func (p *Packet) IsKeyframe() bool {
	return (p.Header.Flags & FlagKeyframe) != 0
}

// String returns a compact, log-friendly summary of the header fields.
func (p *Packet) String() string {
	return fmt.Sprintf("Packet{Type:%d Seq:%d TS:%d SSRC:%d Counter:%d}",
		p.Header.Type, p.Header.Sequence, p.Header.Timestamp, p.Header.SSRC, p.Header.Counter)
}

// GetRoomIDString extracts just the "room_id" field from a JSON control payload,
// returning "" when the payload is empty or not decodable. Used to route a packet
// without fully unmarshaling it.
func (p *Packet) GetRoomIDString() string {
	if len(p.Payload) == 0 {
		return ""
	}
	var aux struct {
		RoomID string `json:"room_id"`
	}
	if err := json.Unmarshal(p.Payload, &aux); err != nil {
		return ""
	}
	return aux.RoomID
}

// QualityPrefPayload is a receiver's batch of per-SSRC simulcast layer
// preferences (PacketTypeQualityPref).
type QualityPrefPayload struct {
	Prefs []QualityPrefEntry `json:"prefs"`
}

// QualityPrefEntry requests forwarding of at most Tier (a Layer* value) for the
// given source SSRC.
type QualityPrefEntry struct {
	SSRC uint32 `json:"ssrc"`
	Tier uint8  `json:"tier"`
}

// HelloPayload is the client's handshake (PacketTypeHello): auth token, the
// protocol version it speaks, codec/media intent, and optional crypto material
// and capabilities. RoomID/UserID are advisory; the server trusts the token.
type HelloPayload struct {
	Token        string        `json:"token"`
	Protocol     uint8         `json:"protocol"`
	Codec        string        `json:"codec"`
	RoomID       string        `json:"room_id,omitempty"`
	UserID       string        `json:"user_id,omitempty"`
	VideoEnabled bool          `json:"video_enabled,omitempty"`
	VideoCodec   string        `json:"video_codec,omitempty"`
	Observer     bool          `json:"observer,omitempty"`
	Region       string        `json:"region,omitempty"`
	Crypto       *CryptoInfo   `json:"crypto,omitempty"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// WelcomePayload is the server's handshake reply (PacketTypeWelcome): the
// negotiated protocol, the session's assigned SSRCs, timing intervals, and the
// current participant roster. RRIntervalMs is only sent for protocol >= 3.
type WelcomePayload struct {
	Protocol       uint8             `json:"protocol"`
	SessionID      uint32            `json:"session_id"`
	RoomID         string            `json:"room_id,omitempty"`
	UserID         string            `json:"user_id,omitempty"`
	SSRC           uint32            `json:"ssrc"`
	VideoSSRC      uint32            `json:"video_ssrc,omitempty"`
	ScreenSSRC     uint32            `json:"screen_ssrc,omitempty"`
	PingIntervalMs uint32            `json:"ping_interval_ms,omitempty"`
	RRIntervalMs   uint32            `json:"rr_interval_ms,omitempty"`
	Observer       bool              `json:"observer,omitempty"`
	Participants   []ParticipantInfo `json:"participants"`
}

// CryptoInfo carries the client's media-encryption parameters in a Hello: the
// AEAD name, key id, and raw key material. NonceBase is accepted for legacy
// clients but the server derives its own per-SSRC nonce base instead.
type CryptoInfo struct {
	AEAD        string `json:"aead,omitempty"`
	KeyID       []byte `json:"key_id,omitempty"`
	KeyMaterial []byte `json:"key_material,omitempty"`
	NonceBase   []byte `json:"nonce_base,omitempty"`
}

// Capabilities advertises the client's Opus features (forward error correction,
// discontinuous transmission) and max bitrate. OpusFEC/OpusDTX are legacy field
// aliases still honored so pre-rename clients keep those signals.
type Capabilities struct {
	FEC        bool   `json:"fec,omitempty"`
	DTX        bool   `json:"dtx,omitempty"`
	MaxBitrate uint32 `json:"max_bitrate,omitempty"`
	// Legacy aliases tolerated for one release so pre-rename clients don't
	// silently lose FEC/DTX signaling.
	OpusFEC bool `json:"opus_fec,omitempty"`
	OpusDTX bool `json:"opus_dtx,omitempty"`
}

// ParticipantInfo is one peer's entry in the Welcome roster: identity, media
// SSRCs, current mute/video/screen/speaking state, and last-known quality stats.
type ParticipantInfo struct {
	UserID        string  `json:"user_id"`
	SSRC          uint32  `json:"ssrc"`
	VideoSSRC     uint32  `json:"video_ssrc,omitempty"`
	ScreenSSRC    uint32  `json:"screen_ssrc,omitempty"`
	Muted         bool    `json:"muted"`
	VideoEnabled  bool    `json:"video_enabled"`
	ScreenSharing bool    `json:"screen_sharing"`
	Speaking      bool    `json:"speaking,omitempty"`
	Quality       int     `json:"quality,omitempty"`
	RTTMs         float64 `json:"rtt_ms,omitempty"`
	PacketLoss    float64 `json:"packet_loss,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	FEC           bool    `json:"fec,omitempty"`
	DTX           bool    `json:"dtx,omitempty"`
	MaxBitrate    uint32  `json:"max_bitrate,omitempty"`
	DisplayName   string  `json:"display_name,omitempty"`
	AvatarURL     string  `json:"avatar_url,omitempty"`
}

// SpeakingPayload announces a change in a participant's speaking state
// (PacketTypeSpeaking), fanned out to the room.
type SpeakingPayload struct {
	SSRC      uint32 `json:"ssrc"`
	VideoSSRC uint32 `json:"video_ssrc,omitempty"`
	UserID    string `json:"user_id"`
	RoomID    string `json:"room_id"`
	Speaking  bool   `json:"speaking"`
}

// MediaStatePayload announces a participant's mute/video/screen-share state
// (PacketTypeMediaState), used both on join and on later toggles.
type MediaStatePayload struct {
	SSRC          uint32 `json:"ssrc"`
	VideoSSRC     uint32 `json:"video_ssrc,omitempty"`
	ScreenSSRC    uint32 `json:"screen_ssrc,omitempty"`
	UserID        string `json:"user_id"`
	RoomID        string `json:"room_id"`
	Muted         bool   `json:"muted"`
	VideoEnabled  bool   `json:"video_enabled"`
	ScreenSharing bool   `json:"screen_sharing"`
}

// NackPayload lists the sequence numbers of an SSRC that a receiver missed and
// wants retransmitted. (Wire form is binary, not JSON; see ParseNack/BuildNack.)
type NackPayload struct {
	SSRC      uint32   `json:"ssrc"`
	Sequences []uint16 `json:"sequences"`
}

// PliPayload names the video SSRC for which a receiver needs a fresh keyframe.
type PliPayload struct {
	SSRC uint32 `json:"ssrc"`
}

// ReceiverReport is RTCP-style feedback about a stream: fraction/total lost,
// highest sequence, jitter, and last-sender-report timing. Drives congestion
// control on the server. (Binary wire form; see ParseReceiverReport.)
type ReceiverReport struct {
	SSRC             uint32  `json:"ssrc"`
	ReporterSSRC     uint32  `json:"reporter_ssrc"`
	FractionLost     float64 `json:"fraction_lost"`
	TotalLost        uint32  `json:"total_lost"`
	HighestSeq       uint32  `json:"highest_seq"`
	Jitter           uint32  `json:"jitter"`
	LastSR           uint32  `json:"last_sr"`
	DelaySinceLastSR uint32  `json:"delay_since_last_sr"`
}

// ParticipantLeftPayload announces that a participant left or timed out
// (PacketTypeParticipantLeft), so peers can drop its streams from the UI.
type ParticipantLeftPayload struct {
	UserID     string `json:"user_id"`
	RoomID     string `json:"room_id"`
	SSRC       uint32 `json:"ssrc"`
	VideoSSRC  uint32 `json:"video_ssrc,omitempty"`
	ScreenSSRC uint32 `json:"screen_ssrc,omitempty"`
}

// SubscribePayload is the receiver's full set of source SSRCs it wants forwarded
// (PacketTypeSubscribe). An empty list is treated as premature/stale and ignored.
type SubscribePayload struct {
	Subscriptions []uint32 `json:"subscriptions"`
}

// QualityReportPayload is a client's self-reported connection quality
// (PacketTypeQualityReport), relayed to the room and folded into metrics.
type QualityReportPayload struct {
	SSRC       uint32  `json:"ssrc"`
	UserID     string  `json:"user_id"`
	RoomID     string  `json:"room_id"`
	Quality    int     `json:"quality"`
	RTTMs      float64 `json:"rtt_ms"`
	PacketLoss float64 `json:"packet_loss"`
	JitterMs   float64 `json:"jitter_ms"`
}

// ParseNack decodes a binary NACK: type, SSRC, a uint16 count, then that many
// big-endian sequence numbers. Returns ErrTooSmall if the buffer is shorter than
// the declared count, guarding against a malicious length.
func ParseNack(data []byte) (*NackPayload, error) {
	if len(data) < 7 {
		return nil, ErrTooSmall
	}

	ssrc := binary.BigEndian.Uint32(data[1:5])
	count := binary.BigEndian.Uint16(data[5:7])
	if len(data) < 7+int(count)*2 {
		return nil, ErrTooSmall
	}

	sequences := make([]uint16, count)
	for i := uint16(0); i < count; i++ {
		sequences[i] = binary.BigEndian.Uint16(data[7+i*2 : 9+i*2])
	}
	return &NackPayload{SSRC: ssrc, Sequences: sequences}, nil
}

// BuildNack serializes a NACK to its binary wire form (the inverse of ParseNack).
func BuildNack(ssrc uint32, sequences []uint16) []byte {
	buf := make([]byte, 7+len(sequences)*2)
	buf[0] = PacketTypeNack
	binary.BigEndian.PutUint32(buf[1:5], ssrc)
	binary.BigEndian.PutUint16(buf[5:7], uint16(len(sequences)))
	for i, seq := range sequences {
		binary.BigEndian.PutUint16(buf[7+i*2:9+i*2], seq)
	}
	return buf
}

// ParsePli decodes a 5-byte PLI (type + SSRC), returning ErrTooSmall if short.
func ParsePli(data []byte) (*PliPayload, error) {
	if len(data) < 5 {
		return nil, ErrTooSmall
	}
	return &PliPayload{SSRC: binary.BigEndian.Uint32(data[1:5])}, nil
}

// BuildPli serializes a keyframe request (PLI) for ssrc to its 5-byte wire form.
func BuildPli(ssrc uint32) []byte {
	buf := make([]byte, 5)
	buf[0] = PacketTypePli
	binary.BigEndian.PutUint32(buf[1:5], ssrc)
	return buf
}

// BitrateHintPayload (v3) tells a sender to retarget its bitrate for SSRC.
// TargetBps is the requested bits/sec and Reason is an opaque code explaining why
// (e.g. loss-driven). Binary wire form; see BuildBitrateHint/ParseBitrateHint.
type BitrateHintPayload struct {
	SSRC      uint32
	TargetBps uint32
	Reason    uint8
}

// BuildBitrateHint serializes a 10-byte bitrate hint (type, SSRC, targetBps,
// reason) for delivery to a sender.
func BuildBitrateHint(ssrc, targetBps uint32, reason uint8) []byte {
	buf := make([]byte, 10)
	buf[0] = PacketTypeBitrateHint
	binary.BigEndian.PutUint32(buf[1:5], ssrc)
	binary.BigEndian.PutUint32(buf[5:9], targetBps)
	buf[9] = reason
	return buf
}

// ParseBitrateHint decodes a bitrate hint, returning ErrTooSmall if under 10
// bytes.
func ParseBitrateHint(data []byte) (*BitrateHintPayload, error) {
	if len(data) < 10 {
		return nil, ErrTooSmall
	}
	return &BitrateHintPayload{
		SSRC:      binary.BigEndian.Uint32(data[1:5]),
		TargetBps: binary.BigEndian.Uint32(data[5:9]),
		Reason:    data[9],
	}, nil
}

// ParseReceiverReport decodes the 25-byte RR wire form. FractionLost is scaled
// from a 0..255 byte to 0..1, TotalLost is a 24-bit field, and DelaySinceLastSR
// is not carried on the wire so it is always zero here. Returns ErrTooSmall if
// short.
func ParseReceiverReport(data []byte) (*ReceiverReport, error) {
	if len(data) < 25 {
		return nil, ErrTooSmall
	}
	return &ReceiverReport{
		SSRC:             binary.BigEndian.Uint32(data[1:5]),
		ReporterSSRC:     binary.BigEndian.Uint32(data[5:9]),
		FractionLost:     float64(data[9]) / 255.0,
		TotalLost:        uint32(data[10])<<16 | uint32(data[11])<<8 | uint32(data[12]),
		HighestSeq:       binary.BigEndian.Uint32(data[13:17]),
		Jitter:           binary.BigEndian.Uint32(data[17:21]),
		LastSR:           binary.BigEndian.Uint32(data[21:25]),
		DelaySinceLastSR: 0,
	}, nil
}

// BuildReceiverReport is the inverse of ParseReceiverReport: it serializes a
// report to the 25-byte wire form (type, ssrc, reporter_ssrc, fraction_lost·/255,
// total_lost 24-bit, highest_seq, jitter, last_sr). Clients — and the shared voice
// test harness — build RR here so the wire layout lives in one place.
func BuildReceiverReport(rr ReceiverReport) []byte {
	buf := make([]byte, 25)
	buf[0] = PacketTypeRR
	binary.BigEndian.PutUint32(buf[1:5], rr.SSRC)
	binary.BigEndian.PutUint32(buf[5:9], rr.ReporterSSRC)
	frac := rr.FractionLost
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	buf[9] = uint8(frac * 255)
	buf[10] = byte(rr.TotalLost >> 16)
	buf[11] = byte(rr.TotalLost >> 8)
	buf[12] = byte(rr.TotalLost)
	binary.BigEndian.PutUint32(buf[13:17], rr.HighestSeq)
	binary.BigEndian.PutUint32(buf[17:21], rr.Jitter)
	binary.BigEndian.PutUint32(buf[21:25], rr.LastSR)
	return buf
}

// CreateAudioPacket assembles an Opus audio Packet with the given header fields
// and payload, ready to encrypt and Marshal. Used by clients and the test harness.
func CreateAudioPacket(ssrc uint32, sequence uint16, timestamp uint32, keyID uint8, counter uint64, audioData []byte) *Packet {
	return &Packet{
		Header: MediaHeader{
			Type:      PacketTypeAudio,
			Flags:     0,
			KeyID:     keyID,
			Codec:     CodecOpus,
			Sequence:  sequence,
			Timestamp: timestamp,
			SSRC:      ssrc,
			Counter:   counter,
		},
		Payload: audioData,
	}
}

// CreateVideoPacket assembles a video Packet, setting FlagKeyframe when keyframe
// is true so the receiver's retransmit buffer pins it.
func CreateVideoPacket(ssrc uint32, sequence uint16, timestamp uint32, keyID uint8, counter uint64, codec uint8, videoData []byte, keyframe bool) *Packet {
	flags := uint8(0)
	if keyframe {
		flags |= FlagKeyframe
	}
	return &Packet{
		Header: MediaHeader{
			Type:      PacketTypeVideo,
			Flags:     flags,
			KeyID:     keyID,
			Codec:     codec,
			Sequence:  sequence,
			Timestamp: timestamp,
			SSRC:      ssrc,
			Counter:   counter,
		},
		Payload: videoData,
	}
}

// ParseJSON unmarshals a control payload body (the bytes after the type byte)
// into a T, returning the decode error on failure.
func ParseJSON[T any](data []byte) (*T, error) {
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// BuildJSONPacket prefixes packetType to the JSON encoding of payload, producing
// a control packet. Returns the marshal error if payload can't be encoded.
func BuildJSONPacket(packetType uint8, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(data))
	out[0] = packetType
	copy(out[1:], data)
	return out, nil
}

// FragmentMediaPacket splits an over-large payload into datagrams of at most
// maxPayload bytes each. A payload that already fits returns a single unfragmented
// packet; otherwise each output is header + FragmentHeader + chunk, with the frame
// timestamp used as the shared FrameID so the receiver can reassemble.
func FragmentMediaPacket(header MediaHeader, payload []byte, maxPayload int) [][]byte {
	if len(payload) <= maxPayload {
		pkt := &Packet{Header: header, Payload: payload}
		return [][]byte{pkt.Marshal()}
	}

	frameID := header.Timestamp
	fragCount := (len(payload) + maxPayload - 1) / maxPayload
	fragments := make([][]byte, 0, fragCount)

	for i := 0; i < fragCount; i++ {
		start := i * maxPayload
		end := start + maxPayload
		if end > len(payload) {
			end = len(payload)
		}

		fragHeader := FragmentHeader{
			FrameID:     frameID,
			FragIndex:   uint16(i),
			FragCount:   uint16(fragCount),
			FrameLength: uint32(len(payload)),
		}

		hdrBytes := header.Marshal()
		fragBytes := fragHeader.Marshal()
		chunk := payload[start:end]

		buf := make([]byte, len(hdrBytes)+len(fragBytes)+len(chunk))
		copy(buf, hdrBytes)
		copy(buf[len(hdrBytes):], fragBytes)
		copy(buf[len(hdrBytes)+len(fragBytes):], chunk)

		fragments = append(fragments, buf)
	}

	return fragments
}
