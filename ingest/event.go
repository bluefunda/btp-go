package ingest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Event is the generic business-event envelope any upstream caller posts to
// /ingest.
//
//	{
//	  "business_object": "BILLING_DOC",
//	  "operation": "CREATE",
//	  "key": "SAP-ISU-1928340192",
//	  "timestamp": "2023-10-27T10:00:00Z",
//	  "payload": { ... }
//	}
//
// Payload is kept as raw JSON: the gateway is a transport, and re-marshalling
// a business document risks reordering keys or losing the numeric precision
// that a downstream system may need to reconcile.
type Event struct {
	// BusinessObject is the upstream object type, e.g. "BILLING_DOC".
	BusinessObject string `json:"business_object"`

	// Operation is the lifecycle action, e.g. "CREATE", "UPDATE", "DELETE".
	Operation string `json:"operation"`

	// Key is the business key of the record, unique within BusinessObject.
	// It is the deduplication identity.
	Key string `json:"key"`

	// Timestamp is when the event occurred in the source system (RFC 3339).
	Timestamp time.Time `json:"timestamp"`

	// Payload is the business document, passed through untouched.
	Payload json.RawMessage `json:"payload"`
}

// Envelope limits. Generous enough for real-world business keys, tight
// enough that a malformed caller cannot poison a subject name or a KV bucket.
const (
	MaxBusinessObjectLen = 64
	MaxOperationLen      = 32
	MaxKeyLen            = 256
)

// Normalize upper-cases BusinessObject and Operation and trims surrounding
// whitespace from all identity fields.
//
// Without this, "BILLING_DOC" and "billing_doc" would deduplicate as two
// distinct events and land on two distinct subjects. Call it before
// [Event.Validate].
func (e *Event) Normalize() {
	e.BusinessObject = strings.ToUpper(strings.TrimSpace(e.BusinessObject))
	e.Operation = strings.ToUpper(strings.TrimSpace(e.Operation))
	e.Key = strings.TrimSpace(e.Key)
}

// Validate reports every problem with the envelope at once, so an upstream
// developer fixes one round of mistakes instead of one mistake per round.
// It returns nil or a non-empty [ValidationErrors].
func (e *Event) Validate() error {
	var errs ValidationErrors

	switch {
	case e.BusinessObject == "":
		errs = append(errs, ValidationError{"business_object", "is required"})
	case len(e.BusinessObject) > MaxBusinessObjectLen:
		errs = append(errs, ValidationError{"business_object", "exceeds 64 characters"})
	case !isSubjectToken(e.BusinessObject):
		errs = append(errs, ValidationError{"business_object", "must contain only letters, digits, '_' and '-'"})
	}

	switch {
	case e.Operation == "":
		errs = append(errs, ValidationError{"operation", "is required"})
	case len(e.Operation) > MaxOperationLen:
		errs = append(errs, ValidationError{"operation", "exceeds 32 characters"})
	case !isSubjectToken(e.Operation):
		errs = append(errs, ValidationError{"operation", "must contain only letters, digits, '_' and '-'"})
	}

	switch {
	case e.Key == "":
		errs = append(errs, ValidationError{"key", "is required"})
	case len(e.Key) > MaxKeyLen:
		errs = append(errs, ValidationError{"key", "exceeds 256 characters"})
	case !isPrintableNoSpace(e.Key):
		errs = append(errs, ValidationError{"key", "must be printable ASCII without whitespace"})
	}

	if e.Timestamp.IsZero() {
		errs = append(errs, ValidationError{"timestamp", "is required and must be RFC 3339"})
	}

	switch {
	case len(e.Payload) == 0:
		errs = append(errs, ValidationError{"payload", "is required"})
	case !json.Valid(e.Payload):
		errs = append(errs, ValidationError{"payload", "is not valid JSON"})
	case string(e.Payload) == "null":
		errs = append(errs, ValidationError{"payload", "must not be null"})
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Subject returns the JetStream subject the event publishes to:
// "<prefix>.<business_object>.<operation>", with both tokens lower-cased.
//
// Lower case is a convention, not a requirement — NATS subjects are
// case-sensitive, and picking one case keeps hand-written consumer filters
// from silently matching nothing.
func (e *Event) Subject(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	bo := strings.ToLower(sanitizeToken(e.BusinessObject))
	op := strings.ToLower(sanitizeToken(e.Operation))
	if prefix == "" {
		return bo + "." + op
	}
	return prefix + "." + bo + "." + op
}

// DedupeKey is the identity used for duplicate suppression: business object
// plus business key, per the gateway contract that a given record may only be
// ingested once per TTL window regardless of how often the caller retries.
//
// The result is always a legal NATS KV key (alphanumerics, '-', '_', '=',
// '.'). Keys that are not already legal are replaced by their SHA-256 digest
// rather than mangled, so two different keys can never collapse into one.
func (e *Event) DedupeKey() string {
	k := e.Key
	if !isKVSafe(k) {
		sum := sha256.Sum256([]byte(k))
		k = "sha256-" + hex.EncodeToString(sum[:])
	}
	return sanitizeToken(e.BusinessObject) + "." + k
}

// MsgID is the value sent as Nats-Msg-Id. It gives JetStream a second,
// server-side deduplication window that is independent of the KV store, so a
// publish retried after an ambiguous timeout cannot double-store.
func (e *Event) MsgID() string {
	return e.DedupeKey() + "." + strings.ToLower(sanitizeToken(e.Operation))
}

// ValidationError is a single rejected field.
type ValidationError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e ValidationError) Error() string { return e.Field + " " + e.Reason }

// ValidationErrors is the set of problems found in one envelope.
type ValidationErrors []ValidationError

func (errs ValidationErrors) Error() string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return "invalid event: " + strings.Join(parts, "; ")
}

// NewTraceID returns a random 128-bit identifier as 32 lowercase hex
// characters — the same shape as a W3C trace-context trace-id, so it drops
// into an OpenTelemetry pipeline later without a format change.
func NewTraceID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error as of Go 1.24; it panics
	// internally if the OS entropy source fails.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func isSubjectToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return s != ""
}

func isKVSafe(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '=', r == '.':
		default:
			return false
		}
	}
	return s != ""
}

func isPrintableNoSpace(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// sanitizeToken is a defence in depth for callers that build a subject from
// an unvalidated Event: anything that is not subject-safe becomes '_'.
func sanitizeToken(s string) string {
	if isSubjectToken(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
