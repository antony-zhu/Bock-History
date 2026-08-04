package sshbootstrap

import (
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"block.local/block-agent/internal/auth"
	"golang.org/x/crypto/ssh"
)

const keyEndpoint = "/api/maintenance/ssh-key"

type KeyConfig struct {
	SuperKeyHash       string
	AuthorizedKeysPath string
	DeviceID           string
	AdvertisedHost     string
}

func (c KeyConfig) Validate() error {
	if strings.TrimSpace(c.SuperKeyHash) == "" {
		return errors.New("super key hash is required")
	}
	if c.AuthorizedKeysPath == "" || !filepath.IsAbs(c.AuthorizedKeysPath) {
		return errors.New("authorized keys path must be absolute")
	}
	if !identifierPattern.MatchString(c.DeviceID) {
		return errors.New("deviceId is invalid")
	}
	if !hostPattern.MatchString(c.AdvertisedHost) {
		return errors.New("advertised host is invalid")
	}
	return nil
}

// KeyHandler is the v2 one-time maintenance credential endpoint. It retains
// only the configured super-key hash and the current public key on disk; the
// generated private key exists only until this HTTP response is written.
type KeyHandler struct {
	config KeyConfig
	now    func() time.Time

	mu            sync.Mutex
	busy          bool
	cooldownUntil time.Time
	failures      int
	blockedUntil  time.Time
}

func NewKeyHandler(config KeyConfig) (*KeyHandler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &KeyHandler{config: config, now: time.Now}, nil
}

func (h *KeyHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != keyEndpoint || request.URL.RawQuery != "" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.NotFound(writer, request)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var body struct {
		SuperKey string `json:"super_key"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.SuperKey == "" || decoder.Decode(&struct{}{}) != io.EOF {
		h.writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if h.rateLimited() {
		h.writeError(writer, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
		return
	}
	if !auth.VerifyPassword(h.config.SuperKeyHash, body.SuperKey) {
		h.recordFailure()
		h.writeError(writer, http.StatusUnauthorized, "INVALID_SUPER_KEY")
		return
	}
	if !h.beginGeneration() {
		h.writeError(writer, http.StatusConflict, "KEY_GENERATION_IN_PROGRESS")
		return
	}
	completed := false
	defer func() { h.finishGeneration(completed) }()

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "KEY_INSTALL_FAILED")
		return
	}
	defer clearPrivateKey(privateKey)
	public, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "KEY_INSTALL_FAILED")
		return
	}
	privateBlock, err := ssh.MarshalPrivateKey(privateKey, h.config.DeviceID)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, "KEY_INSTALL_FAILED")
		return
	}
	privateBytes := pem.EncodeToMemory(privateBlock)
	if err := installAuthorizedKey(h.config.AuthorizedKeysPath, ssh.MarshalAuthorizedKey(public)); err != nil {
		h.writeError(writer, http.StatusInternalServerError, "KEY_INSTALL_FAILED")
		return
	}
	completed = true

	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+h.config.DeviceID+`_ed25519"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("X-SSH-Host", h.config.AdvertisedHost)
	writer.Header().Set("X-SSH-Port", "22")
	writer.Header().Set("X-SSH-Username", "block")
	writer.Header().Set("X-SSH-Key-Fingerprint", ssh.FingerprintSHA256(public))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(privateBytes)
}

func (h *KeyHandler) rateLimited() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now().Before(h.blockedUntil)
}

func (h *KeyHandler) recordFailure() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures++
	if h.failures >= 5 {
		h.blockedUntil = h.now().Add(time.Minute)
		h.failures = 0
	}
}

func (h *KeyHandler) beginGeneration() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.busy || now.Before(h.cooldownUntil) {
		return false
	}
	h.busy = true
	h.failures = 0
	return true
}

func (h *KeyHandler) finishGeneration(completed bool) {
	h.mu.Lock()
	h.busy = false
	if completed {
		h.cooldownUntil = h.now().Add(10 * time.Second)
	}
	h.mu.Unlock()
}

func (h *KeyHandler) writeError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"code": code})
}

func installAuthorizedKey(path string, publicKey []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".authorized_keys-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(publicKey); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func clearPrivateKey(key ed25519.PrivateKey) {
	for index := range key {
		key[index] = 0
	}
}
