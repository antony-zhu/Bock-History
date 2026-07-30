package sshbootstrap

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"
)

const certificatePath = "/v1/ssh/cert"

type nonceRegistrar interface {
	Register(
		ctx context.Context,
		kid string,
		nonce string,
		tokenTimestamp int64,
		serverSeconds int64,
	) (bool, error)
}

type certificateIssuer interface {
	Issue(
		publicKey ED25519AuthorizedKey,
		principal string,
		requestID string,
		validAfter time.Time,
	) (IssuedCertificate, error)
}

type Handler struct {
	config    Config
	adminKey  ed25519.PublicKey
	nonces    nonceRegistrar
	issuer    certificateIssuer
	now       func() time.Time
	requestID func() (string, error)
}

func NewHandler(
	config Config,
	adminKey ed25519.PublicKey,
	nonces nonceRegistrar,
	issuer certificateIssuer,
) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if len(adminKey) != ed25519.PublicKeySize {
		return nil, errors.New("administrator public key must be ED25519")
	}
	if nonces == nil || issuer == nil {
		return nil, errors.New("nonce store and certificate issuer are required")
	}
	return &Handler{
		config:    config,
		adminKey:  adminKey,
		nonces:    nonces,
		issuer:    issuer,
		now:       time.Now,
		requestID: NewRequestID,
	}, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		if request.URL.Path == "/" && request.URL.RawQuery == "" {
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(writer, renderStatusPage(h.config))
			return
		}
		http.NotFound(writer, request)
		return
	}

	requestID, err := h.requestID()
	if err != nil {
		panic(err)
	}
	if request.URL.Path != certificatePath || request.URL.RawQuery != "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "The request path is invalid.", requestID)
		return
	}
	if request.Method != http.MethodPost {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "The request method is invalid.", requestID)
		return
	}
	if request.Header.Get("Content-Encoding") != "" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "Content-Encoding is not supported.", requestID)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "Content-Type must be application/json.", requestID)
		return
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, 16385))
	if err != nil || len(body) > 16384 || !utf8.Valid(body) {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.", requestID)
		return
	}
	certificateRequest, err := DecodeCertificateRequest(body)
	if err != nil {
		if errors.Is(err, ErrUnsupportedPublicKey) {
			h.writeError(writer, http.StatusBadRequest, "UNSUPPORTED_PUBLIC_KEY", "The SSH public key must be ED25519.", requestID)
			return
		}
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.", requestID)
		return
	}

	authorizationValues := request.Header.Values("Authorization")
	if len(authorizationValues) == 0 {
		h.writeError(writer, http.StatusUnauthorized, "AUTHORIZATION_REQUIRED", "Authorization is required.", requestID)
		return
	}
	if len(authorizationValues) != 1 {
		h.writeError(writer, http.StatusUnauthorized, "AUTHORIZATION_INVALID", "Authorization is invalid.", requestID)
		return
	}
	token, err := ParseSuperToken(authorizationValues[0])
	if err != nil || token.KID != h.config.AdministratorKID {
		h.writeError(writer, http.StatusUnauthorized, "AUTHORIZATION_INVALID", "Authorization is invalid.", requestID)
		return
	}

	if certificateRequest.TargetNode != h.config.TargetNode ||
		certificateRequest.SiteID != h.config.SiteID ||
		certificateRequest.BlockID != h.config.BlockID ||
		certificateRequest.DeviceID != h.config.DeviceID {
		h.writeError(writer, http.StatusForbidden, "TARGET_MISMATCH", "The target identity does not match this node.", requestID)
		return
	}

	now := h.now().UTC().Truncate(time.Second)
	serverSeconds := now.Unix()
	if token.Timestamp < serverSeconds-60 || token.Timestamp > serverSeconds+60 {
		h.writeError(writer, http.StatusUnauthorized, "TIMESTAMP_OUT_OF_WINDOW", "The timestamp is outside the accepted window.", requestID)
		return
	}
	if !VerifySuperToken(
		h.adminKey,
		token,
		request.Method,
		request.URL.Path,
		body,
		certificateRequest.Identity(),
	) {
		h.writeError(writer, http.StatusUnauthorized, "AUTHORIZATION_INVALID", "Authorization is invalid.", requestID)
		return
	}

	inserted, err := h.nonces.Register(
		request.Context(),
		token.KID,
		token.Nonce,
		token.Timestamp,
		serverSeconds,
	)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "CERTIFICATE_ISSUE_FAILED", "The certificate could not be issued.", requestID)
		return
	}
	if !inserted {
		h.writeError(writer, http.StatusConflict, "NONCE_REPLAYED", "The signed nonce has already been used.", requestID)
		return
	}

	publicKey, err := ParseED25519AuthorizedKey(certificateRequest.PublicKey)
	if err != nil {
		h.writeError(writer, http.StatusBadRequest, "UNSUPPORTED_PUBLIC_KEY", "The SSH public key must be ED25519.", requestID)
		return
	}
	username, ok := h.config.Username(certificateRequest.AccessProfile)
	if !ok {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST", "The access profile is invalid.", requestID)
		return
	}
	certificate, err := h.issuer.Issue(publicKey, certificateRequest.AccessProfile, requestID, now)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "CERTIFICATE_ISSUE_FAILED", "The certificate could not be issued.", requestID)
		return
	}

	h.writeJSON(writer, http.StatusOK, NewCertificateResponse(h.config, certificate, username, requestID))
}

func (h *Handler) writeError(writer http.ResponseWriter, status int, code, message, requestID string) {
	h.writeJSON(writer, status, ErrorResponse{Code: code, Message: message, RequestID: requestID})
}

func (h *Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
