package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ErrorDetail is the nested Anthropic error object.
type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrorEnvelope is the Claude/Anthropic error body shape.
type ErrorEnvelope struct {
	Type  string      `json:"type"`
	Error ErrorDetail `json:"error"`
}

// ErrorTypeFromStatus maps HTTP status to Anthropic error.type.
func ErrorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized: // 401
		return "authentication_error"
	case http.StatusPaymentRequired: // 402
		return "billing_error"
	case http.StatusForbidden: // 403
		return "permission_error"
	case http.StatusNotFound: // 404
		return "not_found_error"
	case http.StatusRequestEntityTooLarge: // 413
		return "request_too_large"
	case http.StatusTooManyRequests: // 429
		return "rate_limit_error"
	case http.StatusGatewayTimeout: // 504
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

// NewErrorEnvelope builds a Claude-style error body for a status + message.
func NewErrorEnvelope(status int, message string) ErrorEnvelope {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(status)
		if message == "" {
			message = "error"
		}
	}
	errType := ErrorTypeFromStatus(status)
	if json.Valid([]byte(message)) {
		var probe map[string]any
		if err := json.Unmarshal([]byte(message), &probe); err == nil {
			if e, ok := probe["error"].(map[string]any); ok {
				if t, ok := e["type"].(string); ok && strings.TrimSpace(t) != "" {
					errType = strings.TrimSpace(t)
				}
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
			}
		}
	}
	return ErrorEnvelope{
		Type: "error",
		Error: ErrorDetail{
			Type:    errType,
			Message: message,
		},
	}
}

// WriteError writes a Claude-style JSON error and status.
func WriteError(w http.ResponseWriter, status int, message string) {
	env := NewErrorEnvelope(status, message)
	body, err := json.Marshal(env)
	if err != nil {
		body = []byte(`{"type":"error","error":{"type":"api_error","message":"Internal Server Error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// FormatErrorMessage extracts a human message from upstream body/status.
func FormatErrorMessage(status int, raw []byte) string {
	if len(raw) == 0 {
		if t := http.StatusText(status); t != "" {
			return t
		}
		return fmt.Sprintf("upstream status %d", status)
	}
	s := strings.TrimSpace(string(raw))
	if json.Valid(raw) {
		var probe struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil {
			if m := strings.TrimSpace(probe.Error.Message); m != "" {
				return m
			}
			if m := strings.TrimSpace(probe.Message); m != "" {
				return m
			}
			if c := strings.TrimSpace(probe.Error.Code); c != "" {
				return c
			}
		}
	}
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
