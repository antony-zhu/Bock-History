package sshbootstrap

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

var authorizationPattern = regexp.MustCompile(
	`^SuperToken v1 kid=([A-Za-z0-9._-]{1,64}),timestamp=(0|[1-9][0-9]*),nonce=([A-Za-z0-9_-]+),signature=([A-Za-z0-9_-]+)$`,
)

type SuperToken struct {
	KID             string
	Timestamp       int64
	TimestampString string
	Nonce           string
	Signature       []byte
}

func ParseSuperToken(value string) (SuperToken, error) {
	match := authorizationPattern.FindStringSubmatch(value)
	if match == nil {
		return SuperToken{}, errors.New("authorization does not match SuperToken v1")
	}

	timestamp, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return SuperToken{}, errors.New("authorization timestamp is invalid")
	}

	nonce, err := base64.RawURLEncoding.DecodeString(match[3])
	if err != nil || len(nonce) < 16 || len(nonce) > 64 {
		return SuperToken{}, errors.New("authorization nonce is invalid")
	}

	signature, err := base64.RawURLEncoding.DecodeString(match[4])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return SuperToken{}, errors.New("authorization signature is invalid")
	}

	return SuperToken{
		KID:             match[1],
		Timestamp:       timestamp,
		TimestampString: match[2],
		Nonce:           match[3],
		Signature:       signature,
	}, nil
}

type SignedIdentity struct {
	SiteID   string
	BlockID  string
	DeviceID string
}

func CanonicalBytes(token SuperToken, method, path string, body []byte, identity SignedIdentity) []byte {
	bodyDigest := sha256.Sum256(body)
	return []byte(fmt.Sprintf(
		"SUPERTOKEN-V1\n"+
			"version=v1\n"+
			"kid=%s\n"+
			"timestamp=%s\n"+
			"nonce=%s\n"+
			"method=%s\n"+
			"path=%s\n"+
			"bodySHA256=%s\n"+
			"siteId=%s\n"+
			"blockId=%s\n"+
			"deviceId=%s\n",
		token.KID,
		token.TimestampString,
		token.Nonce,
		method,
		path,
		hex.EncodeToString(bodyDigest[:]),
		identity.SiteID,
		identity.BlockID,
		identity.DeviceID,
	))
}

func VerifySuperToken(
	publicKey ed25519.PublicKey,
	token SuperToken,
	method string,
	path string,
	body []byte,
	identity SignedIdentity,
) bool {
	return ed25519.Verify(publicKey, CanonicalBytes(token, method, path, body, identity), token.Signature)
}
