package api

import (
	"context"
	"net/http"
	"time"
)

// Check is one readiness probe.
type Check struct {
	// Name appears in the /readyz response body.
	Name string

	// Probe returns nil when the dependency is usable.
	Probe func(ctx context.Context) error
}

// HealthResponse is the body of /healthz and /readyz.
type HealthResponse struct {
	// Status is "ok" or "degraded".
	Status string `json:"status"`

	// Checks maps each dependency to "ok" or to its failure message.
	// Present on /readyz only.
	Checks map[string]string `json:"checks,omitempty"`

	// Idempotency names the active duplicate-suppression backend, so an
	// operator can tell at a glance whether a deployment is accidentally
	// running the single-replica memory store.
	Idempotency string `json:"idempotency,omitempty"`
}

// Health statuses.
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
)

// readyProbeTimeout bounds the whole readiness evaluation. A probe that
// hangs is a failed probe: a kubelet will not wait, and neither should this.
const readyProbeTimeout = 3 * time.Second

// healthz is the liveness probe. It answers 200 as long as the process can
// serve HTTP.
//
// It deliberately does not check NATS. Liveness failures get the container
// killed, and killing a pod because a shared broker is briefly unreachable
// converts one outage into a fleet-wide restart storm.
func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: HealthOK})
}

// readyz is the readiness probe. It reports whether this instance can
// usefully accept traffic right now, which is the question a load balancer
// is actually asking.
func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyProbeTimeout)
	defer cancel()

	resp := HealthResponse{
		Status:      HealthOK,
		Checks:      make(map[string]string, len(h.opts.Checks)+1),
		Idempotency: h.opts.Idempotency.Kind(),
	}

	for _, c := range h.opts.Checks {
		if err := c.Probe(ctx); err != nil {
			resp.Status = HealthDegraded
			resp.Checks[c.Name] = err.Error()
			continue
		}
		resp.Checks[c.Name] = HealthOK
	}

	// Saturation is a readiness signal, not an error: dropping out of
	// rotation while the queue drains beats answering 503 to every caller.
	if h.opts.Publisher.Saturated() {
		resp.Status = HealthDegraded
		resp.Checks["publish_queue"] = "saturated"
	} else {
		resp.Checks["publish_queue"] = HealthOK
	}

	status := http.StatusOK
	if resp.Status != HealthOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}
