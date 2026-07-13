// Package lb exposes sentinel errors that protocol handlers map to HTTP status codes.
// Scheme B selection lives in internal/selector + internal/lease; this package is only
// a compatibility surface for ported OpenAI/Anthropic handlers.
package lb

import "errors"

// ErrNoCredential is returned when no eligible account/lease is available.
// Handlers map it to HTTP 503.
var ErrNoCredential = errors.New("lb: no available credential")
