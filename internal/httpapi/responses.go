package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"novel-bot/internal/auth"
)

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error     errorDetail `json:"error"`
	RequestID string      `json:"request_id,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	id, _ := r.Context().Value(requestIDKey).(string)
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}, RequestID: id})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func serviceError(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		writeError(w, r, 400, "invalid_input", err.Error())
	case errors.Is(err, auth.ErrConflict):
		writeError(w, r, 409, "email_registered", "Email already registered")
	case errors.Is(err, auth.ErrCredentials):
		writeError(w, r, 401, "invalid_credentials", "Invalid email or password")
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(w, r, 401, "unauthorized", "A valid login session is required")
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, 404, "not_found", "Resource not found")
	default:
		logger.ErrorContext(r.Context(), "account operation failed", "request_id", r.Context().Value(requestIDKey))
		writeError(w, r, 503, "service_unavailable", "Authentication service temporarily unavailable")
	}
}

func sessionUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="account"`)
	writeError(w, r, 401, "unauthorized", "A valid login session is required")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, r, 415, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil {
		var extra any
		if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
			err = extraErr
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, 413, "body_too_large", "Request body exceeds 16 KiB")
		} else {
			writeError(w, r, 400, "invalid_json", "Provide one valid JSON object with supported fields")
		}
		return false
	}
	// null decodes into an empty struct and is rejected by domain validation.
	return true
}
