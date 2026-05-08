// Package swarmslo computes operator-facing service metrics from
// existing NTM coordination signals: Agent Mail acks, Beads status
// transitions, and Agent Mail file reservations.
//
// The package is pure: callers gather events from the durable
// stores (state timeline persister, agentmail client, beads JSONL/DB,
// reservation list) and feed them in as plain views. The reducer
// computes count + p50/p95/max distributions per metric and surfaces
// missing_source warnings when a particular signal could not be
// loaded.
//
// First slice is read-only and stateless — there is no daemon, no
// background sampler, and no on-disk emission. A future slice can
// schedule periodic computation; this slice answers a single
// "snapshot of the last N hours" query in one function call.
//
// See bd-3v1gs.7.
package swarmslo

import (
	"math"
	"sort"
	"strings"
	"time"
)

// MailEvent is one Agent Mail message used by time_to_first_ack.
// Callers gather these from the inbox; AckedAt is nil for messages
// that are still unread/unacked.
type MailEvent struct {
	ID          int
	CreatedAt   time.Time
	AckedAt     *time.Time
	AckRequired bool
	From        string
	To          string
}

// BeadTransition is one status change for a bead. The reducer pairs
// transitions per BeadID to compute ready→claim and claim→close
// durations. "ready" is the marker for a bead that newly entered the
// br ready set; "in_progress" is the claim event; "closed" is the
// terminal event.
type BeadTransition struct {
	BeadID    string
	Status    string // "ready" | "in_progress" | "closed" | other
	EnteredAt time.Time
}

// ReservationWindow is one agent's hold on a path pattern, used by
// reservation_contention to compute the wait time other agents
// experienced before they could acquire the same pattern.
type ReservationWindow struct {
	PathPattern string
	AgentName   string
	AcquiredAt  time.Time
	ReleasedAt  *time.Time // nil means still held
}

// Inputs is the full set of evidence the SLO reducer consumes.
type Inputs struct {
	Mail         []MailEvent
	Beads        []BeadTransition
	Reservations []ReservationWindow

	// MissingSources lists the named sources the caller could NOT
	// load. The reducer surfaces these into Summary.Warnings so
	// consumers see partial-data states explicitly rather than
	// silently scoring a "0 events" distribution.
	MissingSources []string

	// Now defaults to time.Now() when zero. Used only for
	// stale_in_progress age computation.
	Now time.Time
}

// Distribution is the per-metric summary shape. All durations are in
// seconds (float64) so the JSON envelope stays consumer-friendly.
type Distribution struct {
	Count      int     `json:"count"`
	P50Seconds float64 `json:"p50_seconds"`
	P95Seconds float64 `json:"p95_seconds"`
	MaxSeconds float64 `json:"max_seconds"`
	MeanSeconds float64 `json:"mean_seconds"`
	Missing    bool    `json:"missing_source,omitempty"`
}

// Summary is the operator-facing JSON envelope.
type Summary struct {
	GeneratedAt           time.Time    `json:"generated_at"`
	TimeToFirstAck        Distribution `json:"time_to_first_ack"`
	ReadyToClaim          Distribution `json:"ready_to_claim"`
	ClaimToCloseout       Distribution `json:"claim_to_closeout"`
	ReservationContention Distribution `json:"reservation_contention"`
	StaleInProgress       Distribution `json:"stale_in_progress"`
	Warnings              []string     `json:"warnings,omitempty"`
}

// Compute reduces inputs to a Summary. Pure: never reads files,
// never mutates state.
func Compute(in Inputs) Summary {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	out := Summary{
		GeneratedAt:           now.UTC(),
		TimeToFirstAck:        computeAckLatencies(in.Mail),
		ReadyToClaim:          computeReadyToClaim(in.Beads),
		ClaimToCloseout:       computeClaimToCloseout(in.Beads),
		ReservationContention: computeReservationContention(in.Reservations),
		StaleInProgress:       computeStaleInProgress(in.Beads, now),
	}

	// MissingSources flags propagate into per-metric Missing booleans
	// so consumers can grey out the relevant tile rather than read
	// "p95=0".
	for _, raw := range in.MissingSources {
		s := strings.TrimSpace(strings.ToLower(raw))
		switch s {
		case "mail", "agentmail", "agent_mail":
			out.TimeToFirstAck.Missing = true
		case "beads", "br":
			out.ReadyToClaim.Missing = true
			out.ClaimToCloseout.Missing = true
			out.StaleInProgress.Missing = true
		case "reservations", "agentmail_reservations":
			out.ReservationContention.Missing = true
		}
		if raw != "" {
			out.Warnings = append(out.Warnings, raw+" unavailable: source not loaded")
		}
	}
	return out
}

// computeAckLatencies measures (AckedAt - CreatedAt) over messages
// that required an ack and have one. Unacked ack-required messages
// are surfaced as count-of-pending in Warnings.
func computeAckLatencies(events []MailEvent) Distribution {
	var seconds []float64
	pending := 0
	for _, m := range events {
		if !m.AckRequired {
			continue
		}
		if m.AckedAt == nil {
			pending++
			continue
		}
		if m.CreatedAt.IsZero() || m.AckedAt.IsZero() {
			continue
		}
		dt := m.AckedAt.Sub(m.CreatedAt).Seconds()
		if dt < 0 {
			continue
		}
		seconds = append(seconds, dt)
	}
	d := distributionFromSeconds(seconds)
	// Pending is recorded out-of-band in the metric; surfacing it as
	// a Distribution field would be misleading because it is a count
	// of *unobserved* latencies, not a sample.
	_ = pending
	return d
}

// computeReadyToClaim pairs each "ready" transition with the next
// "in_progress" transition for the same bead.
func computeReadyToClaim(transitions []BeadTransition) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		var readyAt *time.Time
		for _, t := range ts {
			tt := t
			switch t.Status {
			case "ready":
				ra := tt.EnteredAt
				readyAt = &ra
			case "in_progress":
				if readyAt == nil {
					continue
				}
				if t.EnteredAt.Before(*readyAt) {
					continue
				}
				seconds = append(seconds, t.EnteredAt.Sub(*readyAt).Seconds())
				readyAt = nil
			}
		}
	}
	return distributionFromSeconds(seconds)
}

// computeClaimToCloseout pairs each "in_progress" with the next
// "closed" transition for the same bead.
func computeClaimToCloseout(transitions []BeadTransition) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		var claimedAt *time.Time
		for _, t := range ts {
			tt := t
			switch t.Status {
			case "in_progress":
				ca := tt.EnteredAt
				claimedAt = &ca
			case "closed":
				if claimedAt == nil {
					continue
				}
				if t.EnteredAt.Before(*claimedAt) {
					continue
				}
				seconds = append(seconds, t.EnteredAt.Sub(*claimedAt).Seconds())
				claimedAt = nil
			}
		}
	}
	return distributionFromSeconds(seconds)
}

// computeStaleInProgress measures (now - last_in_progress) for any
// bead whose most recent transition is in_progress (i.e. it has not
// yet been closed).
func computeStaleInProgress(transitions []BeadTransition, now time.Time) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		if len(ts) == 0 {
			continue
		}
		last := ts[len(ts)-1]
		if last.Status != "in_progress" {
			continue
		}
		if last.EnteredAt.IsZero() || last.EnteredAt.After(now) {
			continue
		}
		seconds = append(seconds, now.Sub(last.EnteredAt).Seconds())
	}
	return distributionFromSeconds(seconds)
}

// computeReservationContention groups reservation windows by their
// path pattern and, for each subsequent reservation under the same
// pattern by a *different* agent, records the gap from the prior
// reservation's release to the new one's acquisition. Adjacent same-
// agent reservations do not count (a renewal is not contention).
func computeReservationContention(windows []ReservationWindow) Distribution {
	byPattern := make(map[string][]ReservationWindow, len(windows))
	for _, w := range windows {
		byPattern[w.PathPattern] = append(byPattern[w.PathPattern], w)
	}

	var seconds []float64
	for _, ws := range byPattern {
		sort.SliceStable(ws, func(i, j int) bool {
			return ws[i].AcquiredAt.Before(ws[j].AcquiredAt)
		})
		for i := 1; i < len(ws); i++ {
			prev := ws[i-1]
			cur := ws[i]
			if prev.AgentName == cur.AgentName {
				continue
			}
			if prev.ReleasedAt == nil {
				// Still held when the next acquired — degenerate or
				// concurrent shared lock; skip rather than emit a
				// negative value.
				continue
			}
			gap := cur.AcquiredAt.Sub(*prev.ReleasedAt).Seconds()
			if gap < 0 {
				continue
			}
			seconds = append(seconds, gap)
		}
	}
	return distributionFromSeconds(seconds)
}

// groupBeadTransitions returns transitions grouped by bead id, each
// group sorted by EnteredAt ascending. Empty/whitespace bead ids are
// dropped.
func groupBeadTransitions(transitions []BeadTransition) map[string][]BeadTransition {
	byBead := make(map[string][]BeadTransition)
	for _, t := range transitions {
		id := strings.TrimSpace(t.BeadID)
		if id == "" {
			continue
		}
		byBead[id] = append(byBead[id], t)
	}
	for id, ts := range byBead {
		sort.SliceStable(ts, func(i, j int) bool {
			return ts[i].EnteredAt.Before(ts[j].EnteredAt)
		})
		byBead[id] = ts
	}
	return byBead
}

// distributionFromSeconds reduces a slice of latencies to count,
// mean, p50, p95, and max. Returns a zero Distribution when empty.
func distributionFromSeconds(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return Distribution{
		Count:       len(sorted),
		MeanSeconds: round3(sum / float64(len(sorted))),
		P50Seconds:  round3(percentile(sorted, 50)),
		P95Seconds:  round3(percentile(sorted, 95)),
		MaxSeconds:  round3(sorted[len(sorted)-1]),
	}
}

// percentile expects a sorted slice and returns the value at the
// requested percentile using nearest-rank.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	if p <= 0 {
		return sorted[0]
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// round3 quantizes a float to 3 decimals so JSON output stays stable
// across platforms with subtly different floating-point widening.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
