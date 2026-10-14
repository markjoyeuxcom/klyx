package ai

import "time"

// Deadline is the whole-call budget for a purpose: the caller's context
// deadline. It bounds the vendor call, any SDK retries and their backoff
// together (spec §1.2).
func Deadline(p Purpose) time.Duration {
	if p == PurposeProbe {
		return 45 * time.Second
	}
	return 2 * time.Minute
}

// apiMaxOutputTokens is the output cap for the hosted vendors. Current models
// think before answering and the thinking counts against this cap (spec §0
// defect 2), so it is generous; the prompt, not the cap, keeps answers short.
// Non-streaming requests stay within the SDKs' HTTP-timeout guidance.
const apiMaxOutputTokens = 16000

// compatMaxTokens is the output cap for an openaiCompatible server: local
// models are slower and rarely think at length.
func compatMaxTokens(p Purpose) int {
	switch p {
	case PurposeFix:
		return 4096
	case PurposeProbe:
		return 256
	case PurposeChat:
		return 4096
	default:
		return 2048
	}
}

// sdkMaxRetries bounds an SDK's own retries; the call's deadline bounds them
// in time.
const sdkMaxRetries = 2
