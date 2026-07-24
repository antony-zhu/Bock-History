package uplink

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxSafeInteger = int64(9007199254740991)

type jcsMember struct {
	name  string
	value any
}

// CanonicalizeJSON implements the RFC 8785 rules needed by the MQTT v1
// contract. The reliable tree deliberately permits only integers in the
// interoperable IEEE-754 range; floats are rejected instead of being rounded.
func CanonicalizeJSON(contents []byte) ([]byte, error) {
	if err := validateJSONTextEncoding(contents); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	value, err := decodeJCSValue(decoder)
	if err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := writeJCS(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// ReliableDigest returns the contract identity digest. sentAt and replayed are
// transport-attempt fields and therefore do not participate in the identity.
func ReliableDigest(contents []byte) (string, error) {
	if err := validateJSONTextEncoding(contents); err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	value, err := decodeJCSValue(decoder)
	if err != nil {
		return "", err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", err
	}
	object, ok := value.([]jcsMember)
	if !ok {
		return "", errors.New("reliable message must be a JSON object")
	}
	filtered := object[:0]
	for _, member := range object {
		if member.name != "sentAt" && member.name != "replayed" {
			filtered = append(filtered, member)
		}
	}
	var canonical bytes.Buffer
	if err := writeJCS(&canonical, filtered); err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(digest[:]), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("read trailing JSON: %w", err)
	}
	return nil
}

func validateJSONTextEncoding(contents []byte) error {
	if !utf8.Valid(contents) {
		return errors.New("JSON text is not valid UTF-8")
	}
	inString := false
	for index := 0; index < len(contents); index++ {
		switch contents[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(contents) {
				continue
			}
			index++
			if contents[index] != 'u' {
				continue
			}
			first, ok := decodeHexQuad(contents, index+1)
			if !ok {
				// The JSON decoder will report malformed hexadecimal syntax.
				continue
			}
			index += 4
			switch {
			case first >= 0xd800 && first <= 0xdbff:
				if index+6 >= len(contents) || contents[index+1] != '\\' || contents[index+2] != 'u' {
					return errors.New("JSON string contains an isolated high surrogate")
				}
				second, ok := decodeHexQuad(contents, index+3)
				if !ok || second < 0xdc00 || second > 0xdfff {
					return errors.New("JSON string contains an invalid surrogate pair")
				}
				index += 6
			case first >= 0xdc00 && first <= 0xdfff:
				return errors.New("JSON string contains an isolated low surrogate")
			}
		}
	}
	return nil
}

func decodeHexQuad(contents []byte, start int) (uint16, bool) {
	if start+4 > len(contents) {
		return 0, false
	}
	var value uint16
	for _, raw := range contents[start : start+4] {
		value <<= 4
		switch {
		case raw >= '0' && raw <= '9':
			value += uint16(raw - '0')
		case raw >= 'a' && raw <= 'f':
			value += uint16(raw-'a') + 10
		case raw >= 'A' && raw <= 'F':
			value += uint16(raw-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func decodeJCSValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			seen := make(map[string]struct{})
			members := make([]jcsMember, 0)
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, errors.New("JSON object member name is not a string")
				}
				if _, duplicate := seen[name]; duplicate {
					return nil, fmt.Errorf("duplicate JSON object member %q", name)
				}
				seen[name] = struct{}{}
				value, err := decodeJCSValue(decoder)
				if err != nil {
					return nil, err
				}
				members = append(members, jcsMember{name: name, value: value})
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated JSON object")
			}
			return members, nil
		case '[':
			values := make([]any, 0)
			for decoder.More() {
				value, err := decodeJCSValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated JSON array")
			}
			return values, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", typed)
		}
	case nil, bool, string, json.Number:
		return typed, nil
	default:
		return nil, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func writeJCS(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		return writeJCSString(output, typed)
	case json.Number:
		raw := string(typed)
		if strings.ContainsAny(raw, ".eE") {
			return fmt.Errorf("non-integer JSON number %q is forbidden in a reliable message", raw)
		}
		number, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || number < -maxSafeInteger || number > maxSafeInteger {
			return fmt.Errorf("JSON integer %q is outside the safe interoperable range", raw)
		}
		output.WriteString(strconv.FormatInt(number, 10))
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeJCS(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case []jcsMember:
		sort.Slice(typed, func(left, right int) bool {
			return lessUTF16(typed[left].name, typed[right].name)
		})
		output.WriteByte('{')
		for index, member := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeJCSString(output, member.name); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := writeJCS(output, member.value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func writeJCSString(output *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return errors.New("invalid UTF-8 JSON string")
	}
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				fmt.Fprintf(output, `\u%04x`, character)
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
	return nil
}

func lessUTF16(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}
