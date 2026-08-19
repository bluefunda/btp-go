package ingest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		BusinessObject: "BILLING_DOC",
		Operation:      "CREATE",
		Key:            "SAP-ISU-1928340192",
		Timestamp:      time.Date(2023, 10, 27, 10, 0, 0, 0, time.UTC),
		Payload:        json.RawMessage(`{"amount":"1234.56"}`),
	}
}

func TestEventValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Event)
		wantField string
	}{
		{name: "valid", mutate: func(*Event) {}},
		{
			name:      "missing business object",
			mutate:    func(e *Event) { e.BusinessObject = "" },
			wantField: "business_object",
		},
		{
			name:      "business object with dot would split the subject",
			mutate:    func(e *Event) { e.BusinessObject = "BILLING.DOC" },
			wantField: "business_object",
		},
		{
			name:      "business object too long",
			mutate:    func(e *Event) { e.BusinessObject = strings.Repeat("A", MaxBusinessObjectLen+1) },
			wantField: "business_object",
		},
		{
			name:      "missing operation",
			mutate:    func(e *Event) { e.Operation = "" },
			wantField: "operation",
		},
		{
			name:      "operation with wildcard character",
			mutate:    func(e *Event) { e.Operation = "CRE*TE" },
			wantField: "operation",
		},
		{
			name:      "missing key",
			mutate:    func(e *Event) { e.Key = "" },
			wantField: "key",
		},
		{
			name:      "key with whitespace",
			mutate:    func(e *Event) { e.Key = "SAP ISU 123" },
			wantField: "key",
		},
		{
			name:      "key too long",
			mutate:    func(e *Event) { e.Key = strings.Repeat("K", MaxKeyLen+1) },
			wantField: "key",
		},
		{
			name:      "zero timestamp",
			mutate:    func(e *Event) { e.Timestamp = time.Time{} },
			wantField: "timestamp",
		},
		{
			name:      "missing payload",
			mutate:    func(e *Event) { e.Payload = nil },
			wantField: "payload",
		},
		{
			name:      "null payload",
			mutate:    func(e *Event) { e.Payload = json.RawMessage(`null`) },
			wantField: "payload",
		},
		{
			name:      "malformed payload",
			mutate:    func(e *Event) { e.Payload = json.RawMessage(`{"a":`) },
			wantField: "payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := validEvent()
			tt.mutate(&ev)
			err := ev.Validate()

			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error on %q", tt.wantField)
			}

			var verrs ValidationErrors
			if !errors.As(err, &verrs) {
				t.Fatalf("Validate() error is %T, want ValidationErrors", err)
			}
			for _, ve := range verrs {
				if ve.Field == tt.wantField {
					return
				}
			}
			t.Fatalf("Validate() = %v, want an error on field %q", verrs, tt.wantField)
		})
	}
}

func TestEventValidateReportsEveryProblem(t *testing.T) {
	ev := Event{}
	err := ev.Validate()

	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("Validate() error is %T, want ValidationErrors", err)
	}
	// business_object, operation, key, timestamp, payload
	if len(verrs) != 5 {
		t.Fatalf("Validate() reported %d problems (%v), want 5", len(verrs), verrs)
	}
}

func TestEventNormalize(t *testing.T) {
	ev := Event{
		BusinessObject: "  billing_doc ",
		Operation:      "create",
		Key:            "  SAP-1  ",
	}
	ev.Normalize()

	if ev.BusinessObject != "BILLING_DOC" {
		t.Errorf("BusinessObject = %q, want %q", ev.BusinessObject, "BILLING_DOC")
	}
	if ev.Operation != "CREATE" {
		t.Errorf("Operation = %q, want %q", ev.Operation, "CREATE")
	}
	if ev.Key != "SAP-1" {
		t.Errorf("Key = %q, want %q", ev.Key, "SAP-1")
	}
}

// Normalizing before deduplication is what stops "billing_doc" and
// "BILLING_DOC" from being treated as two different documents.
func TestNormalizeMakesDedupeKeyCaseInsensitive(t *testing.T) {
	lower := Event{BusinessObject: "billing_doc", Key: "SAP-1"}
	upper := Event{BusinessObject: "BILLING_DOC", Key: "SAP-1"}
	lower.Normalize()
	upper.Normalize()

	if lower.DedupeKey() != upper.DedupeKey() {
		t.Fatalf("DedupeKey mismatch after Normalize: %q vs %q", lower.DedupeKey(), upper.DedupeKey())
	}
}

func TestEventSubject(t *testing.T) {
	tests := []struct {
		name   string
		event  Event
		prefix string
		want   string
	}{
		{
			name:   "lower-cases tokens",
			event:  validEvent(),
			prefix: "events",
			want:   "events.billing_doc.create",
		},
		{
			name:   "trims stray dots on the prefix",
			event:  validEvent(),
			prefix: ".events.",
			want:   "events.billing_doc.create",
		},
		{
			name:   "empty prefix",
			event:  validEvent(),
			prefix: "",
			want:   "billing_doc.create",
		},
		{
			name:   "sanitizes an unvalidated token rather than emitting a wildcard",
			event:  Event{BusinessObject: "BILL*DOC", Operation: "CRE>ATE"},
			prefix: "p",
			want:   "p.bill_doc.cre_ate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Subject(tt.prefix); got != tt.want {
				t.Errorf("Subject(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestDedupeKey(t *testing.T) {
	ev := validEvent()
	if got, want := ev.DedupeKey(), "BILLING_DOC.SAP-ISU-1928340192"; got != want {
		t.Errorf("DedupeKey() = %q, want %q", got, want)
	}

	// A key that is legal in the envelope but not a legal KV key must be
	// hashed, not mangled: mangling could collapse two documents into one.
	weird := validEvent()
	weird.Key = "doc/2023#42"
	got := weird.DedupeKey()
	if !strings.HasPrefix(got, "BILLING_DOC.sha256-") {
		t.Fatalf("DedupeKey() = %q, want a sha256- prefixed key", got)
	}
	if !isKVSafe(strings.TrimPrefix(got, "BILLING_DOC.")) {
		t.Errorf("DedupeKey() = %q, which is not a legal KV key", got)
	}

	other := validEvent()
	other.Key = "doc/2023#43"
	if other.DedupeKey() == got {
		t.Error("distinct keys produced the same DedupeKey")
	}
}

func TestMsgIDIncludesOperation(t *testing.T) {
	create := validEvent()
	update := validEvent()
	update.Operation = "UPDATE"

	if create.MsgID() == update.MsgID() {
		t.Fatal("MsgID() is equal for CREATE and UPDATE of the same document; " +
			"JetStream would suppress the update as a duplicate")
	}
}

func TestNewTraceID(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := NewTraceID()
		if len(id) != 32 {
			t.Fatalf("NewTraceID() = %q, want 32 hex characters", id)
		}
		if seen[id] {
			t.Fatalf("NewTraceID() returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

// The payload must survive the round trip byte for byte: SAP decimals are
// sent as JSON numbers and re-marshalling through float64 would silently
// change them.
func TestPayloadIsPreservedVerbatim(t *testing.T) {
	const body = `{"business_object":"BILLING_DOC","operation":"CREATE","key":"K1",` +
		`"timestamp":"2023-10-27T10:00:00Z","payload":{"amount":123456789012345678901234567890.5}}`

	var ev Event
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, want := string(ev.Payload), `{"amount":123456789012345678901234567890.5}`; got != want {
		t.Errorf("Payload = %s, want %s", got, want)
	}
}
