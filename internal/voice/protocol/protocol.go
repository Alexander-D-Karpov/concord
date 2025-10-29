package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	RTPHeaderSize    = 12
	PacketHeaderSize = 16
	MaxPacketSize    = 1500

	PacketTypeHello    = 0x01
	PacketTypeWelcome  = 0x02
	PacketTypeAudio    = 0x03
	PacketTypeVideo    = 0x04
	PacketTypePing     = 0x05
	PacketTypePong     = 0x06
	PacketTypeBye      = 0x07
	PacketTypeSpeaking = 0x08
)

const (
	FlagMarker   = 0x01
	FlagKeyframe = 0x02
	FlagMuted    = 0x04
	FlagSpeaking = 0x08
)

var (
	ErrInvalidPacket = errors.New("invalid packet")
	ErrTooSmall      = errors.New("packet too small")
)

type RTPHeader struct {
	Version     uint8
	Padding     bool
	Extension   bool
	CSRC        uint8
	Marker      bool
	PayloadType uint8
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
}

type Packet struct {
	Type      uint8
	Flags     uint8
	Sequence  uint16
	Timestamp uint32
	SSRC      uint32
	RoomID    []byte // 16 bytes UUID
	UserID    []byte // 16 bytes UUID
	Payload   []byte
}

func ParsePacket(data []byte) (*Packet, error) {
	if len(data) < PacketHeaderSize {
		return nil, ErrTooSmall
	}

	p := &Packet{
		Type:      data[0],
		Flags:     data[1],
		Sequence:  binary.BigEndian.Uint16(data[2:4]),
		Timestamp: binary.BigEndian.Uint32(data[4:8]),
		SSRC:      binary.BigEndian.Uint32(data[8:12]),
		RoomID:    data[12:16],
		UserID:    data[16:20],
		Payload:   data[20:],
	}

	return p, nil
}

func (p *Packet) Marshal() []byte {
	buf := make([]byte, 20+len(p.Payload))

	buf[0] = p.Type
	buf[1] = p.Flags
	binary.BigEndian.PutUint16(buf[2:4], p.Sequence)
	binary.BigEndian.PutUint32(buf[4:8], p.Timestamp)
	binary.BigEndian.PutUint32(buf[8:12], p.SSRC)
	copy(buf[12:16], p.RoomID)
	copy(buf[16:20], p.UserID)
	copy(buf[20:], p.Payload)

	return buf
}

func (p *Packet) GetNonce() []byte {
	nonce := make([]byte, 24)
	binary.BigEndian.PutUint32(nonce[0:4], p.SSRC)
	binary.BigEndian.PutUint32(nonce[4:8], p.Timestamp)
	binary.BigEndian.PutUint16(nonce[8:10], p.Sequence)
	return nonce
}

type HelloPayload struct {
	Token    string `json:"token"`
	Protocol uint8  `json:"protocol"`
	Codec    string `json:"codec"`
}

type WelcomePayload struct {
	SessionID    uint32            `json:"session_id"`
	SSRC         uint32            `json:"ssrc"`
	Participants []ParticipantInfo `json:"participants"`
}

type ParticipantInfo struct {
	UserID       string `json:"user_id"`
	SSRC         uint32 `json:"ssrc"`
	Muted        bool   `json:"muted"`
	VideoEnabled bool   `json:"video_enabled"`
}

type SpeakingPayload struct {
	SSRC     uint32 `json:"ssrc"`
	UserID   string `json:"user_id"`
	Speaking bool   `json:"speaking"`
}

func CreateAudioPacket(ssrc uint32, sequence uint16, timestamp uint32, audioData []byte) *Packet {
	return &Packet{
		Type:      PacketTypeAudio,
		Flags:     0,
		Sequence:  sequence,
		Timestamp: timestamp,
		SSRC:      ssrc,
		Payload:   audioData,
	}
}

func CreateVideoPacket(ssrc uint32, sequence uint16, timestamp uint32, videoData []byte, keyframe bool) *Packet {
	flags := uint8(0)
	if keyframe {
		flags |= FlagKeyframe
	}

	return &Packet{
		Type:      PacketTypeVideo,
		Flags:     flags,
		Sequence:  sequence,
		Timestamp: timestamp,
		SSRC:      ssrc,
		Payload:   videoData,
	}
}

func (p *Packet) IsAudio() bool {
	return p.Type == PacketTypeAudio
}

func (p *Packet) IsVideo() bool {
	return p.Type == PacketTypeVideo
}

func (p *Packet) IsKeyframe() bool {
	return (p.Flags & FlagKeyframe) != 0
}

func (p *Packet) String() string {
	return fmt.Sprintf("Packet{Type: %d, Seq: %d, TS: %d, SSRC: %d, PayloadSize: %d}",
		p.Type, p.Sequence, p.Timestamp, p.SSRC, len(p.Payload))
}
