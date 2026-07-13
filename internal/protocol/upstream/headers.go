package upstream

import (
	"net/http"
	"strings"
)

// HeaderInput is the set of values injected into every upstream request.
type HeaderInput struct {
	AccessToken      string
	Model            string
	ConvID           string
	ClientVersion    string
	ClientIdentifier string
	TokenAuth        string
	UserAgent        string
	Accept           string
	ContentType      string
	Extra            http.Header
}

// ApplyHeaders sets the Grok CLI / cli-chat-proxy headers on req.
// Existing headers on req are preserved unless overwritten by this function
// or by Extra (Extra wins last).
func ApplyHeaders(req *http.Request, in HeaderInput) {
	if req == nil {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	tokenAuth := firstNonEmpty(in.TokenAuth, DefaultTokenAuth)
	version := firstNonEmpty(in.ClientVersion, DefaultClientVersion)
	identifier := firstNonEmpty(in.ClientIdentifier, DefaultClientIdentifier)
	ua := firstNonEmpty(in.UserAgent, DefaultUserAgent)

	if tok := strings.TrimSpace(in.AccessToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("X-XAI-Token-Auth", tokenAuth)
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("x-grok-client-identifier", identifier)
	req.Header.Set("User-Agent", ua)

	if model := strings.TrimSpace(in.Model); model != "" {
		req.Header.Set("x-grok-model-override", model)
	}
	if conv := strings.TrimSpace(in.ConvID); conv != "" {
		req.Header.Set("x-grok-conv-id", conv)
	}
	if accept := strings.TrimSpace(in.Accept); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if ct := strings.TrimSpace(in.ContentType); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	for k, vals := range in.Extra {
		for i, v := range vals {
			if i == 0 {
				req.Header.Set(k, v)
			} else {
				req.Header.Add(k, v)
			}
		}
	}
}

// RequiredHeaderNames lists the headers every authenticated upstream call should carry.
func RequiredHeaderNames() []string {
	return []string{
		"Authorization",
		"X-XAI-Token-Auth",
		"x-grok-client-version",
		"x-grok-client-identifier",
		"User-Agent",
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
