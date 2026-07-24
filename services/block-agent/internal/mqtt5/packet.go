package mqtt5

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	DefaultMaximumPacketSize = 512 * 1024
	maxRemainingLength       = 268435455
)

type packet struct {
	header byte
	body   []byte
}

func readPacket(reader io.Reader, maximum int) (packet, error) {
	if maximum <= 0 {
		maximum = DefaultMaximumPacketSize
	}
	var fixed [1]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return packet{}, err
	}
	remaining, encodedBytes, err := readVarInt(reader)
	if err != nil {
		return packet{}, fmt.Errorf("decode MQTT Remaining Length: %w", err)
	}
	if 1+encodedBytes+remaining > maximum {
		return packet{}, fmt.Errorf("MQTT packet is %d bytes, maximum is %d", 1+encodedBytes+remaining, maximum)
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(reader, body); err != nil {
		return packet{}, err
	}
	return packet{header: fixed[0], body: body}, nil
}

func writePacket(writer io.Writer, header byte, body []byte, maximum int) error {
	if maximum <= 0 {
		maximum = DefaultMaximumPacketSize
	}
	remaining, err := encodeVarInt(len(body))
	if err != nil {
		return err
	}
	if 1+len(remaining)+len(body) > maximum {
		return fmt.Errorf("MQTT packet is %d bytes, maximum is %d", 1+len(remaining)+len(body), maximum)
	}
	frame := make([]byte, 0, 1+len(remaining)+len(body))
	frame = append(frame, header)
	frame = append(frame, remaining...)
	frame = append(frame, body...)
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func encodeVarInt(value int) ([]byte, error) {
	if value < 0 || value > maxRemainingLength {
		return nil, errors.New("MQTT variable integer is outside 0..268435455")
	}
	var encoded []byte
	for {
		digit := byte(value % 128)
		value /= 128
		if value > 0 {
			digit |= 0x80
		}
		encoded = append(encoded, digit)
		if value == 0 {
			return encoded, nil
		}
	}
}

func readVarInt(reader io.Reader) (value int, bytesRead int, err error) {
	multiplier := 1
	for bytesRead < 4 {
		var raw [1]byte
		if _, err = io.ReadFull(reader, raw[:]); err != nil {
			return 0, bytesRead, err
		}
		bytesRead++
		value += int(raw[0]&0x7f) * multiplier
		if raw[0]&0x80 == 0 {
			if bytesRead > 1 && raw[0] == 0 {
				return 0, bytesRead, errors.New("non-canonical MQTT variable integer")
			}
			return value, bytesRead, nil
		}
		multiplier *= 128
	}
	return 0, bytesRead, errors.New("MQTT variable integer exceeds four bytes")
}

func consumeVarInt(contents []byte) (value int, rest []byte, err error) {
	reader := bytes.NewReader(contents)
	value, read, err := readVarInt(reader)
	if err != nil {
		return 0, nil, err
	}
	return value, contents[read:], nil
}

func appendUTF(target []byte, value string) ([]byte, error) {
	if !utf8.ValidString(value) || len(value) > 65535 {
		return nil, errors.New("MQTT UTF-8 string is invalid or too long")
	}
	for _, character := range value {
		if character == 0 || (character >= 0xd800 && character <= 0xdfff) {
			return nil, errors.New("MQTT UTF-8 string contains a forbidden code point")
		}
	}
	target = binary.BigEndian.AppendUint16(target, uint16(len(value)))
	return append(target, value...), nil
}

func consumeUTF(contents []byte) (string, []byte, error) {
	if len(contents) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	length := int(binary.BigEndian.Uint16(contents[:2]))
	if len(contents) < 2+length {
		return "", nil, io.ErrUnexpectedEOF
	}
	value := string(contents[2 : 2+length])
	if _, err := appendUTF(nil, value); err != nil {
		return "", nil, err
	}
	return value, contents[2+length:], nil
}

type propertyKind byte

const (
	propertyByte propertyKind = iota
	propertyTwoByte
	propertyFourByte
	propertyVarInt
	propertyBinary
	propertyUTF
	propertyUTFPair
)

var propertyKinds = map[byte]propertyKind{
	0x01: propertyByte, 0x02: propertyFourByte, 0x03: propertyUTF,
	0x08: propertyUTF, 0x09: propertyBinary, 0x0b: propertyVarInt,
	0x11: propertyFourByte, 0x12: propertyUTF, 0x13: propertyTwoByte,
	0x15: propertyUTF, 0x16: propertyBinary, 0x17: propertyByte,
	0x18: propertyFourByte, 0x19: propertyByte, 0x1a: propertyUTF,
	0x1c: propertyUTF, 0x1f: propertyUTF, 0x21: propertyTwoByte,
	0x22: propertyTwoByte, 0x23: propertyTwoByte, 0x24: propertyByte,
	0x25: propertyByte, 0x26: propertyUTFPair, 0x27: propertyFourByte,
	0x28: propertyByte, 0x29: propertyByte, 0x2a: propertyByte,
}

func consumeProperties(contents []byte, allowed map[byte]bool) ([]byte, error) {
	length, rest, err := consumeVarInt(contents)
	if err != nil {
		return nil, fmt.Errorf("decode MQTT property length: %w", err)
	}
	if len(rest) < length {
		return nil, io.ErrUnexpectedEOF
	}
	properties := rest[:length]
	seen := make(map[byte]bool)
	for len(properties) > 0 {
		identifier := properties[0]
		properties = properties[1:]
		kind, known := propertyKinds[identifier]
		if !known || (allowed != nil && !allowed[identifier]) {
			return nil, fmt.Errorf("MQTT property 0x%02x is not allowed in this packet", identifier)
		}
		if seen[identifier] && identifier != 0x26 && identifier != 0x0b {
			return nil, fmt.Errorf("MQTT singleton property 0x%02x is duplicated", identifier)
		}
		seen[identifier] = true
		switch kind {
		case propertyByte:
			if len(properties) < 1 {
				return nil, io.ErrUnexpectedEOF
			}
			if propertyMustBeBoolean(identifier) && properties[0] > 1 {
				return nil, fmt.Errorf("MQTT property 0x%02x must be 0 or 1", identifier)
			}
			properties = properties[1:]
		case propertyTwoByte:
			if len(properties) < 2 {
				return nil, io.ErrUnexpectedEOF
			}
			value := binary.BigEndian.Uint16(properties[:2])
			if (identifier == 0x21 || identifier == 0x23) && value == 0 {
				return nil, fmt.Errorf("MQTT property 0x%02x must be non-zero", identifier)
			}
			properties = properties[2:]
		case propertyFourByte:
			if len(properties) < 4 {
				return nil, io.ErrUnexpectedEOF
			}
			if identifier == 0x27 && binary.BigEndian.Uint32(properties[:4]) == 0 {
				return nil, errors.New("MQTT Maximum Packet Size must be non-zero")
			}
			properties = properties[4:]
		case propertyVarInt:
			var value int
			value, properties, err = consumeVarInt(properties)
			if err != nil {
				return nil, err
			}
			if identifier == 0x0b && value == 0 {
				return nil, errors.New("MQTT Subscription Identifier must be non-zero")
			}
		case propertyBinary:
			if len(properties) < 2 {
				return nil, io.ErrUnexpectedEOF
			}
			size := int(binary.BigEndian.Uint16(properties[:2]))
			if len(properties) < 2+size {
				return nil, io.ErrUnexpectedEOF
			}
			properties = properties[2+size:]
		case propertyUTF:
			_, properties, err = consumeUTF(properties)
			if err != nil {
				return nil, err
			}
		case propertyUTFPair:
			_, properties, err = consumeUTF(properties)
			if err == nil {
				_, properties, err = consumeUTF(properties)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	return rest[length:], nil
}

func propertyMustBeBoolean(identifier byte) bool {
	switch identifier {
	case 0x01, 0x17, 0x19, 0x24, 0x25, 0x28, 0x29, 0x2a:
		return true
	default:
		return false
	}
}

var (
	connackProperties = propertySet(0x11, 0x12, 0x13, 0x15, 0x16, 0x1a, 0x1c, 0x1f, 0x21, 0x22, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a)
	subackProperties  = propertySet(0x1f, 0x26)
	pubackProperties  = propertySet(0x1f, 0x26)
	publishProperties = propertySet(0x01, 0x02, 0x03, 0x08, 0x09, 0x0b, 0x23, 0x26)
)

func propertySet(values ...byte) map[byte]bool {
	result := make(map[byte]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
