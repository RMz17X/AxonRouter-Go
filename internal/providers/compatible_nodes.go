package providers

import "strings"

// NodeWire is the wire contract of one custom provider node type: the lane a
// node's baseUrl prefix completes to, and the format its upstream speaks.
type NodeWire struct {
	// Lane is the path appended to a node's baseUrl prefix ("" = the handler
	// derives its own route from this lane, e.g. embeddings).
	Lane string
	// Format is the providers format constant the upstream answers in
	// ("" = OpenAI Chat Completions). Stamped onto the resolved config by
	// getProviderConfig so the request/response translators know the wire.
	Format string
}

// CompatibleNodeWires is the single source of truth for what every custom
// node type speaks — the same contract upstream's executors/base.js buildUrl
// and services/provider.js getTargetFormat carry (isAnthropicCompatible →
// "claude"). Lane and Format are declared together on purpose: they describe
// one wire and must not drift apart. The drift was the "no choices in OpenAI
// response" bug (PR #797) — compatibleNodeLane sent the request to /messages
// while the format machinery still treated the upstream as Chat Completions,
// so a Claude-format reply died in the OpenAI→Claude translator.
//
// Tests pin this table from both directions:
//   - internal/handlers/chat: lane, format and config stamping per node type;
//   - internal/handlers/dashboard: nodeIDPrefixes covers exactly these keys.
//
// Adding a node type means adding one entry here; both test suites then tell
// you what the request path still has to learn about it.
var CompatibleNodeWires = map[string]NodeWire{
	// An OpenAI-compatible node speaks Chat Completions by default; a stored
	// apiType of "responses" moves the lane to /responses (see
	// compatibleNodeLane), the format stays OpenAI either way.
	"openai-compatible": {Lane: "/chat/completions"},
	// An anthropic-compatible node fronts an Anthropic Messages endpoint.
	"anthropic-compatible": {Lane: "/messages", Format: FormatClaude},
	// A custom-embedding node is dialled on the chat lane; the embeddings
	// handler swaps the route to /embeddings off this same URL
	// (buildEmbeddingsURL), which is why no /embeddings lane is declared here.
	"custom-embedding": {Lane: "/chat/completions"},
}

// APITypeResponses is the stored apiType of an openai-compatible node whose
// upstream speaks the Responses API (upstream resolveOpenAICompatibleApiType).
const APITypeResponses = "responses"

// IsCompatibleNode reports whether a provider id belongs to a custom
// endpoint the operator registered. The match is by the node-type prefix the
// create handler composes ("openai-compatible-<suffix>"), so it is the same
// shape internal/handlers/chat.nodeWireOf recognises — duplicated here
// because internal/thinking must not import the handler package.
func IsCompatibleNode(provider string) bool {
	for nodeType := range CompatibleNodeWires {
		if strings.HasPrefix(provider, nodeType+"-") {
			return true
		}
	}
	return false
}

// IsOpenAICompatibleNode reports whether a provider id names an
// openai-compatible custom node (upstream isOpenAICompatibleNode).
func IsOpenAICompatibleNode(provider string) bool {
	return strings.HasPrefix(provider, "openai-compatible-")
}

// CompatibleNodeSpeaksResponses reports whether a custom node's upstream
// serves the OpenAI Responses API.
//
// Upstream decides this from the node's DECLARED apiType, not from the URL it
// happens to dial: getTargetFormat (open-sse/services/provider.js:134) reads
// resolveOpenAICompatibleApiType(provider, credentials) and answers
// "openai-responses" for a node whose apiType is "responses". A node whose
// id says "responses" but whose stored apiType was never filled in falls back
// to the id, the same fallback upstream applies.
//
// The URL is not a usable signal here: the lane is appended to the node's
// baseUrl prefix (compatibleNodeLaneURL), so the resolved URL ends in
// "/responses" only as long as the connection row kept the apiType in sync.
// A connection written before the apiType sync, or one whose baseUrl already
// names a route, resolves to a URL that says nothing about the wire — and a
// request that then thinks it is on the Chat lane sends a top-level
// reasoning_effort to an endpoint that rejects it with 400 ("moved to
// reasoning.effort").
//
// The apiType travels on the connection (providerSpecificData.apiType), so
// the caller passes what it read there. An empty apiType means the node
// speaks Chat Completions, which is the default upstream applies.
func CompatibleNodeSpeaksResponses(provider, apiType string) bool {
	if !IsOpenAICompatibleNode(provider) {
		return false
	}
	if apiType == APITypeResponses {
		return true
	}
	// Upstream's fallback: a node created before the dashboard began syncing
	// apiType carries none, and its id is the only thing left that says so.
	return apiType == "" && strings.Contains(provider, APITypeResponses)
}
