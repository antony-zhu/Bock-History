package agent

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"block.local/block-agent/internal/auth"
	"golang.org/x/net/websocket"
)

type credentialsRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
}

type passwordRequest struct {
	Username        string `json:"username"`
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type sessionPolicyRequest struct {
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
}

type bootstrapStatusResponse struct {
	BootstrapRequired bool `json:"bootstrapRequired"`
}

type idleTimeoutResponse struct {
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
}

type identityResponse struct {
	Username    string           `json:"username"`
	Role        auth.Role        `json:"role"`
	Permissions auth.Permissions `json:"permissions"`
}

// bootstrap serves both the fresh-install status and the one-time initial
// administrator creation. It never establishes a server-side login session.
func (r *Runtime) bootstrap(writer http.ResponseWriter, request *http.Request) {
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		hasAdmin, err := r.auth.HasAdmin(request.Context())
		if err != nil {
			writeAuthError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, bootstrapStatusResponse{BootstrapRequired: !hasAdmin})
	case http.MethodPost:
		var body credentialsRequest
		if !decodeJSON(writer, request, &body) {
			return
		}
		identity, err := r.auth.FirstSetup(request.Context(), body.Username, body.Password, body.ConfirmPassword)
		if err != nil {
			writeAuthError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, responseIdentity(identity))
	default:
		writer.Header().Set("Allow", "GET, POST")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Runtime) login(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	identity, err := r.auth.Login(request.Context(), body.Username, body.Password)
	if err != nil {
		writeAuthError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, responseIdentity(identity))
}

func (r *Runtime) changePassword(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	var body passwordRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := r.auth.ChangePassword(request.Context(), body.Username, body.CurrentPassword, body.NewPassword); err != nil {
		writeAuthError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// sessionPolicy persists the browser's local idle timeout. The value is not a
// backend session deadline and no role or cookie is required to read or write it.
func (r *Runtime) sessionPolicy(writer http.ResponseWriter, request *http.Request) {
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		timeout, err := r.auth.IdleTimeout(request.Context())
		if err != nil {
			writeAuthError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, idleTimeoutResponse{IdleTimeoutSeconds: int(timeout / time.Second)})
	case http.MethodPut:
		var body sessionPolicyRequest
		if !decodeJSON(writer, request, &body) {
			return
		}
		if err := r.auth.SetIdleTimeout(request.Context(), time.Duration(body.IdleTimeoutSeconds)*time.Second); err != nil {
			writeAuthError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, idleTimeoutResponse{IdleTimeoutSeconds: body.IdleTimeoutSeconds})
	default:
		writer.Header().Set("Allow", "GET, PUT")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func responseIdentity(identity auth.Identity) identityResponse {
	return identityResponse{
		Username:    identity.Username,
		Role:        identity.Role,
		Permissions: identity.Permissions(),
	}
}

func (r *Runtime) checkHandshake(config *websocket.Config, request *http.Request) error {
	return checkLocalOrigin(config, request, r.websocketOriginScheme())
}

func staticHMI(files fs.FS) http.Handler {
	if files == nil {
		return http.NotFoundHandler()
	}
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") || request.URL.Path == "/ws" {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if isHMIStaticResource(request.URL.Path) {
			writer.Header().Set("Cache-Control", "no-store")
		}
		server.ServeHTTP(writer, request)
	})
}

func isHMIStaticResource(requestPath string) bool {
	if requestPath == "/" || requestPath == "/index.html" {
		return true
	}
	if strings.HasPrefix(requestPath, "/api/") || requestPath == "/ws" {
		return false
	}
	switch path.Ext(requestPath) {
	case ".css", ".html", ".ico", ".jpeg", ".jpg", ".js", ".json", ".mjs", ".png", ".svg", ".webp", ".woff", ".woff2":
		return true
	default:
		return false
	}
}

func requireAuthMethod(writer http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	writer.Header().Set("Allow", method)
	writer.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	return true
}

func writeAuthError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrSetupCompleted), errors.Is(err, auth.ErrAccountExists):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrPasswordMismatch), errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrInvalidPassword), errors.Is(err, auth.ErrInvalidRole), errors.Is(err, auth.ErrInvalidIdleTimeout):
		status = http.StatusBadRequest
	case errors.Is(err, auth.ErrAccountNotFound):
		status = http.StatusNotFound
	}
	writeJSON(writer, status, map[string]string{"error": strings.ReplaceAll(err.Error(), "\n", " ")})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
