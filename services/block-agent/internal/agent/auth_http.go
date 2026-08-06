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

const sessionCookieName = "block_session"

type credentialsRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
}

type passwordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	ConfirmPassword string `json:"confirmPassword"`
}

type sessionPolicyRequest struct {
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
}

func (r *Runtime) bootstrap(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	var body credentialsRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	result, err := r.auth.FirstSetup(request.Context(), body.Username, body.Password, body.ConfirmPassword)
	if err != nil {
		writeAuthError(writer, err)
		return
	}
	r.setSessionCookie(writer, result)
	writeJSON(writer, http.StatusCreated, sessionResponse(result.Session))
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
	result, err := r.auth.Login(request.Context(), body.Username, body.Password)
	if err != nil {
		writeAuthError(writer, err)
		return
	}
	r.setSessionCookie(writer, result)
	writeJSON(writer, http.StatusOK, sessionResponse(result.Session))
}

func (r *Runtime) activity(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeAuthError(writer, auth.ErrUnauthenticated)
		return
	}
	session, err := r.auth.Activity(token)
	if err != nil {
		writeAuthError(writer, err)
		return
	}
	r.setSessionCookie(writer, auth.LoginResult{Token: token, Session: session})
	writer.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) logout(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	if token, ok := sessionToken(request); ok {
		r.auth.Logout(token)
	}
	http.SetCookie(writer, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writer.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) changePassword(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPost) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeAuthError(writer, auth.ErrUnauthenticated)
		return
	}
	var body passwordRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := r.auth.ChangePassword(request.Context(), token, body.CurrentPassword, body.NewPassword, body.ConfirmPassword); err != nil {
		writeAuthError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) setSessionPolicy(writer http.ResponseWriter, request *http.Request) {
	if !requireAuthMethod(writer, request, http.MethodPut) {
		return
	}
	if r.auth == nil {
		http.NotFound(writer, request)
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeAuthError(writer, auth.ErrUnauthenticated)
		return
	}
	var body sessionPolicyRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := r.auth.SetIdleTimeout(request.Context(), token, time.Duration(body.IdleTimeoutSeconds)*time.Second); err != nil {
		writeAuthError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) setSessionCookie(writer http.ResponseWriter, result auth.LoginResult) {
	maxAge := int(time.Until(result.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: result.Token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (r *Runtime) checkHandshake(config *websocket.Config, request *http.Request) error {
	return checkLocalOrigin(config, request)
}

func staticHMI(files fs.FS) http.Handler {
	if files == nil {
		return http.NotFoundHandler()
	}
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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

func sessionToken(request *http.Request) (string, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	returnValue := ""
	if err == nil {
		returnValue = cookie.Value
	}
	return returnValue, returnValue != ""
}

func sessionResponse(session auth.Session) map[string]any {
	return map[string]any{
		"username":  session.Username,
		"role":      session.Role,
		"expiresAt": session.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func writeAuthError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrUnauthenticated):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrForbidden):
		status = http.StatusForbidden
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
