package broker

// Headers carried on every published event. They let an operator trace a
// document through the stream with `nats sub` alone, without decoding the
// body.
const (
	HeaderTraceID        = "X-Trace-Id"
	HeaderBusinessObject = "X-Business-Object"
	HeaderOperation      = "X-Operation"
	HeaderBusinessKey    = "X-Business-Key"
	HeaderEventTime      = "X-Event-Timestamp"
	HeaderReceivedAt     = "X-Received-At"
	HeaderSourceSystem   = "X-Source-System"
)
