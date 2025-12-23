package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	MediaHeaderSize = 24
	FragHeaderSize  = 12
	MaxPacketSize   = 1500
	MaxUDPPayload   = 1200

	PacketTypeHello           = 0x01
	PacketTypeWelcome         = 0x02
	PacketTypeAudio           = 0x03
	PacketTypeVideo           = 0x04
	PacketTypePing            = 0x05
	PacketTypePong            = 0x06
	PacketTypeBye             = 0x07
	PacketTypeSpeaking        = 0x08
	PacketTypeMediaState      = 0x09
	PacketTypeNack            = 0x0a
	PacketTypePli             = 0x0b
	PacketTypeRR              = 0x0c
	PacketTypeParticipantLeft = 0x0d

	FlagMarker   = 0x01
	FlagKeyframe = 0x02
	FlagMuted    = 0x04
	FlagSpeaking = 0x08

	CodecOpus = 1
	CodecH264 = 2
	CodecVP8  = 3
)

var (
	ErrInvalidPacket = errors.New("invalid packet")
	ErrTooSmall      = errors.New("packet too small")
)

type MediaHeader struct {
	Type      uint8
	Flags     uint8
	KeyID     uint8
	Codec     uint8
	Sequence  uint16
	Timestamp uint32
	SSRC      uint32
	Counter   uint64
}

type FragmentHeader struct {
	FrameID     uint32
	FragIndex   uint16
	FragCount   uint16
	FrameLength uint32
}

type Packet struct {
	Header   MediaHeader
	Fragment *FragmentHeader
	Payload  []byte
	RawAAD   []byte
	Type     uint8
}

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
	}, nil
}

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
	binary.BigEndian.PutUint16(buf[22:24], 0)
	return buf
}

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

func (f *FragmentHeader) Marshal() []byte {
	buf := make([]byte, FragHeaderSize)
	binary.BigEndian.PutUint32(buf[0:4], f.FrameID)
	binary.BigEndian.PutUint16(buf[4:6], f.FragIndex)
	binary.BigEndian.PutUint16(buf[6:8], f.FragCount)
	binary.BigEndian.PutUint32(buf[8:12], f.FrameLength)
	return buf
}

func ParsePacket(data []byte) (*Packet, error) {
	if len(data) < 1 {
		return nil, ErrTooSmall
	}

	packetType := data[0]

	switch packetType {
	case PacketTypeAudio, PacketTypeVideo:
		return parseMediaPacket(data)
	default:
		return &Packet{
			Header:  MediaHeader{Type: packetType},
			Payload: data[1:],
		}, nil
	}
}

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
	}
	copy(packet.RawAAD, data[:MediaHeaderSize])

	if len(data) > MediaHeaderSize {
		packet.Payload = data[MediaHeaderSize:]
	}

	return packet, nil
}

func (p *Packet) Marshal() []byte {
	header := p.Header.Marshal()
	buf := make([]byte, len(header)+len(p.Payload))
	copy(buf, header)
	copy(buf[len(header):], p.Payload)
	return buf
}

func (p *Packet) IsAudio() bool {
	return p.Header.Type == PacketTypeAudio
}

func (p *Packet) IsVideo() bool {
	return p.Header.Type == PacketTypeVideo
}

func (p *Packet) IsKeyframe() bool {
	return (p.Header.Flags & FlagKeyframe) != 0
}

func (p *Packet) String() string {
	return fmt.Sprintf("Packet{Type: %d, Seq: %d, TS: %d, SSRC: %d, Counter: %d}",
		p.Header.Type, p.Header.Sequence, p.Header.Timestamp, p.Header.SSRC, p.Header.Counter)
}

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

type HelloPayload struct {
	Token        string      `json:"token"`
	Protocol     uint8       `json:"protocol"`
	Codec        string      `json:"codec"`
	RoomID       string      `json:"room_id,omitempty"`
	UserID       string      `json:"user_id,omitempty"`
	VideoEnabled bool        `json:"video_enabled,omitempty"`
	VideoCodec   string      `json:"video_codec,omitempty"`
	Crypto       *CryptoInfo `json:"crypto,omitempty"`
}

type CryptoInfo struct {
	AEAD        string `json:"aead,omitempty"`
	KeyID       []byte `json:"key_id"`
	KeyMaterial []byte `json:"key_material"`
	NonceBase   []byte `json:"nonce_base"`
}

type WelcomePayload struct {
	SessionID    uint32            `json:"session_id"`
	SSRC         uint32            `json:"ssrc"`
	VideoSSRC    uint32            `json:"video_ssrc,omitempty"`
	Participants []ParticipantInfo `json:"participants"`
}

type ParticipantInfo struct {
	UserID       string `json:"user_id"`
	SSRC         uint32 `json:"ssrc"`
	VideoSSRC    uint32 `json:"video_ssrc,omitempty"`
	Muted        bool   `json:"muted"`
	VideoEnabled bool   `json:"video_enabled"`
}

type SpeakingPayload struct {
	SSRC      uint32 `json:"ssrc"`
	VideoSSRC uint32 `json:"video_ssrc,omitempty"`
	UserID    string `json:"user_id"`
	RoomID    string `json:"room_id"`
	Speaking  bool   `json:"speaking"`
}

type MediaStatePayload struct {
	SSRC         uint32 `json:"ssrc"`
	VideoSSRC    uint32 `json:"video_ssrc,omitempty"`
	UserID       string `json:"user_id"`
	RoomID       string `json:"room_id"`
	Muted        bool   `json:"muted"`
	VideoEnabled bool   `json:"video_enabled"`
}

type NackPayload struct {
	SSRC      uint32   `json:"ssrc"`
	Sequences []uint16 `json:"sequences"`
}

type PliPayload struct {
	SSRC uint32 `json:"ssrc"`
}

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

type ParticipantLeftPayload struct {
	UserID    string `json:"user_id"`
	RoomID    string `json:"room_id"`
	SSRC      uint32 `json:"ssrc"`
	VideoSSRC uint32 `json:"video_ssrc,omitempty"`
}

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

func ParsePli(data []byte) (*PliPayload, error) {
	if len(data) < 5 {
		return nil, ErrTooSmall
	}
	return &PliPayload{SSRC: binary.BigEndian.Uint32(data[1:5])}, nil
}

func BuildPli(ssrc uint32) []byte {
	buf := make([]byte, 5)
	buf[0] = PacketTypePli
	binary.BigEndian.PutUint32(buf[1:5], ssrc)
	return buf
}

func ParseReceiverReport(data []byte) (*ReceiverReport, error) {
	if len(data) < 25 {
		return nil, ErrTooSmall
	}

	return &ReceiverReport{
		SSRC:             binary.BigEndian.Uint32(data[1:5]),
		ReporterSSRC:     binary.BigEndian.Uint32(data[5:9]),
		FractionLost:     float64(data[9]) / 255.0,
		TotalLost:        binary.BigEndian.Uint32(data[10:14]) & 0xffffff,
		HighestSeq:       binary.BigEndian.Uint32(data[13:17]),
		Jitter:           binary.BigEndian.Uint32(data[17:21]),
		LastSR:           binary.BigEndian.Uint32(data[21:25]),
		DelaySinceLastSR: 0,
	}, nil
}

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

func ParseJSON[T any](data []byte) (*T, error) {
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
