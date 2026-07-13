// Package protocol is a namespace for Scheme B protocol adapters.
//
// Subpackages:
//   - openai: OpenAI Responses/Chat sanitize + handlers (copied from grokbuild-proxy)
//   - anthropic: Anthropic Messages translate + handlers
//   - upstream: Grok cli-chat-proxy HTTP client
//   - executor: lease-based PostResponses (replaces proxy.Executor + json store)
//   - config / lb: slim compatibility surfaces for ported handlers
//
// Prefer importing the concrete subpackage, not this root package.
package protocol
