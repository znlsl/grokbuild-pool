package openai

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ErrorBody is the OpenAI-compatible error envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the nested error object.
type ErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Code    *string `json:"code"`
	Param   *string `json:"param,omitempty"`
}

// WriteError writes an OpenAI-like JSON error response.
func WriteError(w http.ResponseWriter, status int, message, errType, code string) {
	if w == nil {
		return
	}
	if strings.TrimSpace(errType) == "" {
		errType = statusToType(status)
	}
	var codePtr *string
	if strings.TrimSpace(code) != "" {
		c := code
		codePtr = &c
	}
	body := ErrorBody{Error: ErrorDetail{
		Message: message,
		Type:    errType,
		Code:    codePtr,
	}}
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"error":{"message":"internal error","type":"server_error","code":null}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// MapUpstreamError maps an upstream non-2xx body into an OpenAI error envelope
// and writes it with the same status (clamped to a sensible range).
func MapUpstreamError(w http.ResponseWriter, status int, raw []byte) {
	if status < 400 {
		status = http.StatusBadGateway
	}
	// Prefer preserving upstream OpenAI-shaped errors.
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &probe) == nil && len(probe.Error) > 0 {
		var detail ErrorDetail
		if json.Unmarshal(probe.Error, &detail) == nil && strings.TrimSpace(detail.Message) != "" {
			if detail.Type == "" {
				detail.Type = statusToType(status)
			}
			out, err := json.Marshal(ErrorBody{Error: detail})
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(out)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}

	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	WriteError(w, status, msg, statusToType(status), "")
}

func statusToType(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return "invalid_request_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		if status >= 500 {
			return "server_error"
		}
		return "invalid_request_error"
	}
}
