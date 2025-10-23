package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	PacketHeaderSize = 16

	PacketTypeHello   = 0x01
	PacketTypeWelcome = 0x02
	PacketTypeMedia   = 0x03
	PacketTypePing    = 0x04
	PacketTypePong    = 0x05
	PacketTypeBye     = 0x06
)

var (
	ErrInvalidPacket = errors.New("invalid packet")
	ErrTooSmall      = errors.New("packet too small")
)

type PacketHeader struct {
	Type      uint8
	Flags     uint8
	Sequence  uint16
	Timestamp uint32
	SSRC      uint32
	RoomID    uint32
	UserID    uint32
}

type Packet struct {
	Header  PacketHeader
	Payload []byte
}

func ParsePacket(data []byte) (*Packet, error) {
	if len(data) < PacketHeaderSize {
		return nil, ErrTooSmall
	}

	p := &Packet{
		Header: PacketHeader{
			Type:      data[0],
			Flags:     data[1],
			Sequence:  binary.BigEndian.Uint16(data[2:4]),
			Timestamp: binary.BigEndian.Uint32(data[4:8]),
			SSRC:      binary.BigEndian.Uint32(data[8:12]),
			RoomID:    binary.BigEndian.Uint32(data[12:16]),
		},
		Payload: data[PacketHeaderSize:],
	}

	return p, nil
}

func (p *Packet) Marshal() []byte {
	buf := make([]byte, PacketHeaderSize+len(p.Payload))

	buf[0] = p.Header.Type
	buf[1] = p.Header.Flags
	binary.BigEndian.PutUint16(buf[2:4], p.Header.Sequence)
	binary.BigEndian.PutUint32(buf[4:8], p.Header.Timestamp)
	binary.BigEndian.PutUint32(buf[8:12], p.Header.SSRC)
	binary.BigEndian.PutUint32(buf[12:16], p.Header.RoomID)

	copy(buf[PacketHeaderSize:], p.Payload)

	return buf
}

type HelloPayload struct {
	Token    string
	Protocol uint8
}

type WelcomePayload struct {
	SessionID    uint32
	SSRC         uint32
	Participants []ParticipantInfo
}

type ParticipantInfo struct {
	UserID       uint32
	SSRC         uint32
	Muted        bool
	VideoEnabled bool
}
