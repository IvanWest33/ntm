package swarmslo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func at(offset time.Duration) time.Time { return clock().Add(offset) }
func ptrAt(offset time.Duration) *time.Time {
	t := at(offset)
	return &t
}

func TestCompute_AllZerosWhenNoInputs(t *testing.T) {
	t.Parallel()
	got := Compute(Inputs{Now: clock()})
	if got.TimeToFirstAck.Count != 0 {
		t.Errorf("TimeToFirstAck.Count = %d, want 0", got.TimeToFirstAck.Count)
	}
	if got.ReadyToClaim.Count != 0 {
		t.Errorf("ReadyToClaim.Count = %d, want 0", got.ReadyToClaim.Count)
	}
	if got.ReservationContention.Count != 0 {
		t.Errorf("ReservationContention.Count = %d, want 0", got.ReservationContention.Count)
	}
	if !got.GeneratedAt.Equal(clock()) {
		t.Errorf("GeneratedAt = %s, want %s", got.GeneratedAt, clock())
	}
}

func TestCompute_TimeToFirstAck_OnlyAckRequiredWithAck(t *testing.T) {
	t.Parallel()
	created := at(0)
	in := Inputs{
		Now: at(2 * time.Hour),
		Mail: []MailEvent{
			// Acked after 30s.
			{ID: 1, CreatedAt: created, AckedAt: ptrAt(30 * time.Second), AckRequired: true},
			// Acked after 90s.
			{ID: 2, CreatedAt: created, AckedAt: ptrAt(90 * time.Second), AckRequired: true},
			// Ack required but never acked — must NOT count.
			{ID: 3, CreatedAt: created, AckedAt: nil, AckRequired: true},
			// Not ack-required — must NOT count.
			{ID: 4, CreatedAt: created, AckedAt: ptrAt(5 * time.Second), AckRequired: false},
			// Pre-acked (acked before create) — must be filtered.
			{ID: 5, CreatedAt: at(60 * time.Second), AckedAt: ptrAt(10 * time.Second), AckRequired: true},
		},
	}
	d := Compute(in).TimeToFirstAck
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 90 {
		t.Errorf("MaxSeconds = %v, want 90", d.MaxSeconds)
	}
	if d.MeanSeconds != 60 {
		t.Errorf("MeanSeconds = %v, want 60", d.MeanSeconds)
	}
}

func TestCompute_ReadyToClaim_PairsTransitions(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Beads: []BeadTransition{
			// bd-1: ready at +0, claimed at +60s -> 60s.
			{BeadID: "bd-1", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-1", Status: "in_progress", EnteredAt: at(60 * time.Second)},
			// bd-2: ready at +0, claimed at +5min -> 300s.
			{BeadID: "bd-2", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-2", Status: "in_progress", EnteredAt: at(5 * time.Minute)},
			// bd-3: claimed before any ready transition -> ignored.
			{BeadID: "bd-3", Status: "in_progress", EnteredAt: at(10 * time.Second)},
		},
	}
	d := Compute(in).ReadyToClaim
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 300 {
		t.Errorf("MaxSeconds = %v, want 300", d.MaxSeconds)
	}
	if d.MeanSeconds != 180 {
		t.Errorf("MeanSeconds = %v, want 180", d.MeanSeconds)
	}
}

func TestCompute_ClaimToCloseout(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Beads: []BeadTransition{
			{BeadID: "bd-a", Status: "in_progress", EnteredAt: at(0)},
			{BeadID: "bd-a", Status: "closed", EnteredAt: at(2 * time.Hour)},
			{BeadID: "bd-b", Status: "in_progress", EnteredAt: at(30 * time.Minute)},
			{BeadID: "bd-b", Status: "closed", EnteredAt: at(40 * time.Minute)}, // 600s
		},
	}
	d := Compute(in).ClaimToCloseout
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 7200 {
		t.Errorf("MaxSeconds = %v, want 7200", d.MaxSeconds)
	}
}

func TestCompute_StaleInProgress_ExcludesClosed(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: at(2 * time.Hour),
		Beads: []BeadTransition{
			// bd-a: still in_progress, claimed at t=0 -> 7200s stale.
			{BeadID: "bd-a", Status: "ready", EnteredAt: at(-1 * time.Hour)},
			{BeadID: "bd-a", Status: "in_progress", EnteredAt: at(0)},
			// bd-b: closed -> excluded.
			{BeadID: "bd-b", Status: "in_progress", EnteredAt: at(0)},
			{BeadID: "bd-b", Status: "closed", EnteredAt: at(30 * time.Minute)},
		},
	}
	d := Compute(in).StaleInProgress
	if d.Count != 1 {
		t.Fatalf("Count = %d, want 1", d.Count)
	}
	if d.MaxSeconds != 7200 {
		t.Errorf("MaxSeconds = %v, want 7200", d.MaxSeconds)
	}
}

func TestCompute_ReservationContention_DifferentAgentsOnly(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Reservations: []ReservationWindow{
			// Alice acquires at +0, releases at +60s.
			// Bob acquires at +90s -> 30s contention.
			{PathPattern: "internal/auth/**", AgentName: "Alice", AcquiredAt: at(0), ReleasedAt: ptrAt(60 * time.Second)},
			{PathPattern: "internal/auth/**", AgentName: "Bob", AcquiredAt: at(90 * time.Second), ReleasedAt: ptrAt(150 * time.Second)},
			// Carol acquires at +200s -> Bob released at +150s -> 50s contention.
			{PathPattern: "internal/auth/**", AgentName: "Carol", AcquiredAt: at(200 * time.Second), ReleasedAt: nil},
			// Same agent renewal -> NOT counted.
			{PathPattern: "internal/foo/**", AgentName: "Alice", AcquiredAt: at(0), ReleasedAt: ptrAt(10 * time.Second)},
			{PathPattern: "internal/foo/**", AgentName: "Alice", AcquiredAt: at(20 * time.Second), ReleasedAt: nil},
		},
	}
	d := Compute(in).ReservationContention
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2 (renewal must not count)", d.Count)
	}
	if d.MaxSeconds != 50 {
		t.Errorf("MaxSeconds = %v, want 50", d.MaxSeconds)
	}
	if d.MeanSeconds != 40 {
		t.Errorf("MeanSeconds = %v, want 40", d.MeanSeconds)
	}
}

func TestCompute_MissingSourcesPropagateAndWarnAndZero(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now:            clock(),
		MissingSources: []string{"agentmail", "beads"},
	}
	got := Compute(in)
	if !got.TimeToFirstAck.Missing {
		t.Errorf("TimeToFirstAck.Missing = false, want true (agentmail unavailable)")
	}
	if !got.ReadyToClaim.Missing || !got.ClaimToCloseout.Missing || !got.StaleInProgress.Missing {
		t.Errorf("beads-derived metrics did not all set Missing: ready=%v claim=%v stale=%v",
			got.ReadyToClaim.Missing, got.ClaimToCloseout.Missing, got.StaleInProgress.Missing)
	}
	if got.ReservationContention.Missing {
		t.Errorf("ReservationContention.Missing = true, want false (reservations not in MissingSources)")
	}
	if len(got.Warnings) != 2 {
		t.Errorf("Warnings = %v, want 2 entries", got.Warnings)
	}
}

func TestCompute_PercentileShape(t *testing.T) {
	t.Parallel()
	// 20 evenly-spaced ack latencies: 1s..20s.
	mail := make([]MailEvent, 20)
	for i := 0; i < 20; i++ {
		mail[i] = MailEvent{
			ID:          i + 1,
			CreatedAt:   at(0),
			AckedAt:     ptrAt(time.Duration(i+1) * time.Second),
			AckRequired: true,
		}
	}
	d := Compute(Inputs{Now: clock(), Mail: mail}).TimeToFirstAck
	if d.Count != 20 {
		t.Fatalf("Count = %d", d.Count)
	}
	if d.MaxSeconds != 20 {
		t.Errorf("MaxSeconds = %v, want 20", d.MaxSeconds)
	}
	// p50 nearest-rank: ceil(50/100*20)=10 -> sorted[9]=10
	if d.P50Seconds != 10 {
		t.Errorf("P50Seconds = %v, want 10", d.P50Seconds)
	}
	// p95: ceil(95/100*20)=19 -> sorted[18]=19
	if d.P95Seconds != 19 {
		t.Errorf("P95Seconds = %v, want 19", d.P95Seconds)
	}
}

func TestCompute_JSONShapeIsStableAndDeterministic(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Mail: []MailEvent{
			{ID: 1, CreatedAt: at(0), AckedAt: ptrAt(30 * time.Second), AckRequired: true},
		},
		Beads: []BeadTransition{
			{BeadID: "bd-1", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-1", Status: "in_progress", EnteredAt: at(60 * time.Second)},
		},
	}
	a, _ := json.Marshal(Compute(in))
	b, _ := json.Marshal(Compute(in))
	if string(a) != string(b) {
		t.Errorf("JSON drifted between two Compute calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"time_to_first_ack"`, `"ready_to_claim"`, `"claim_to_closeout"`,
		`"reservation_contention"`, `"stale_in_progress"`, `"generated_at"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing field %s: %s", want, a)
		}
	}
}

func TestCompute_NegativeAckLatencyIsDropped(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Mail: []MailEvent{
			// Acked 30s BEFORE created — must be silently dropped.
			{ID: 1, CreatedAt: at(60 * time.Second), AckedAt: ptrAt(30 * time.Second), AckRequired: true},
			// Valid sample.
			{ID: 2, CreatedAt: at(0), AckedAt: ptrAt(10 * time.Second), AckRequired: true},
		},
	}
	d := Compute(in).TimeToFirstAck
	if d.Count != 1 {
		t.Fatalf("Count = %d, want 1 (negative latency dropped)", d.Count)
	}
}
