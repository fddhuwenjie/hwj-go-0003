package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fddhuwenjie/hwj-go-0003/booking"
)

func TestParseCommands_Array(t *testing.T) {
	in := []byte(`[
		{"op":"register-room","id":"A","name":"Atlas","capacity":10},
		{"op":"book","roomId":"A","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","booker":"alice"}
	]`)
	cmds, err := parseCommands(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("len = %d, want 2", len(cmds))
	}
	if cmds[0].Op != "register-room" || cmds[0].ID != "A" {
		t.Errorf("cmd0 = %+v", cmds[0])
	}
	if cmds[1].Op != "book" || cmds[1].Booker != "alice" {
		t.Errorf("cmd1 = %+v", cmds[1])
	}
}

func TestParseCommands_SingleObject(t *testing.T) {
	in := []byte(`{"op":"register-room","id":"A","name":"Atlas","capacity":10}`)
	cmds, err := parseCommands(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].ID != "A" {
		t.Fatalf("cmds = %+v", cmds)
	}
}

func TestParseCommands_Invalid(t *testing.T) {
	if _, err := parseCommands([]byte(``)); err == nil {
		t.Fatal("empty input should error")
	}
	if _, err := parseCommands([]byte(`{"op":"register-room"`)); err == nil {
		t.Fatal("malformed JSON should error")
	}
}

// TestProcess_FullWorkflow drives the CLI dispatch end to end and checks the
// key public behaviors: registration, duplicate room, booking, idempotent
// retry, conflict, boundary adjacency, cancel idempotency, availability and
// utilization.
func TestProcess_FullWorkflow(t *testing.T) {
	in := []byte(`[
		{"op":"register-room","id":"atlas","name":"Atlas","capacity":12},
		{"op":"register-room","id":"borealis","name":"Borealis","capacity":8},
		{"op":"register-room","id":"atlas","name":"Dup","capacity":1},
		{"op":"book","roomId":"atlas","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","booker":"alice","clientRequestId":"k1"},
		{"op":"book","roomId":"atlas","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","booker":"alice","clientRequestId":"k1"},
		{"op":"book","roomId":"atlas","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:30:00Z","booker":"bob"},
		{"op":"book","roomId":"atlas","start":"2026-08-15T10:00:00Z","end":"2026-08-15T11:00:00Z","booker":"carol"},
		{"op":"book","roomId":"borealis","start":"2026-08-15T09:00:00Z","end":"2026-08-15T17:00:00Z","booker":"dave"},
		{"op":"available","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:00:00Z"},
		{"op":"cancel","reservationId":"res-000001"},
		{"op":"cancel","reservationId":"res-000001"},
		{"op":"available","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:00:00Z"},
		{"op":"utilization","date":"2026-08-15","location":"UTC","open":"09:00","close":"18:00"},
		{"op":"bogus"}
	]`)
	results, err := process(in)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(results) != 14 {
		t.Fatalf("len(results) = %d, want 14", len(results))
	}

	// 0,1: register two rooms OK.
	mustOK(t, results[0])
	mustOK(t, results[1])
	// 2: duplicate room id -> error.
	if results[2].OK {
		t.Fatal("duplicate room should fail")
	}
	// 3: book alice OK, id res-000001.
	mustOK(t, results[3])
	if results[3].Reservation.ID != "res-000001" {
		t.Errorf("res3 id = %q", results[3].Reservation.ID)
	}
	// 4: idempotent retry -> same id, OK.
	mustOK(t, results[4])
	if results[4].Reservation.ID != "res-000001" {
		t.Errorf("res4 id = %q, want res-000001 (idempotent)", results[4].Reservation.ID)
	}
	// 5: conflict -> error.
	if results[5].OK {
		t.Fatal("overlapping book should fail with conflict")
	}
	if !strings.Contains(results[5].Error, "conflict") {
		t.Errorf("res5 error = %q, want conflict", results[5].Error)
	}
	// 6: adjacent booking OK.
	mustOK(t, results[6])
	if results[6].Reservation.ID != "res-000002" {
		t.Errorf("res6 id = %q, want res-000002", results[6].Reservation.ID)
	}
	// 7: book borealis OK -> res-000003.
	mustOK(t, results[7])
	if results[7].Reservation.ID != "res-000003" {
		t.Errorf("res7 id = %q, want res-000003", results[7].Reservation.ID)
	}
	// 8: available 09:30-10:00. atlas has 9-10 (overlap) and 10-11 (no
	// overlap); borealis has 9-17 (overlap). So free = none.
	mustOK(t, results[8])
	if ids := roomIDs(results[8].Available); len(ids) != 0 {
		t.Errorf("res8 free rooms = %v, want []", ids)
	}
	// 9: cancel res-000001 OK.
	mustOK(t, results[9])
	if !results[9].Reservation.Cancelled {
		t.Error("res9 should be cancelled")
	}
	// 10: double cancel -> OK (idempotent), still cancelled.
	mustOK(t, results[10])
	if !results[10].Reservation.Cancelled {
		t.Error("res10 should be cancelled")
	}
	// 11: available 09:30-10:00 after cancel -> atlas free (carol 10-11 no
	// overlap), borealis still booked -> [atlas].
	mustOK(t, results[11])
	if ids := roomIDs(results[11].Available); !equalStr(ids, []string{"atlas"}) {
		t.Errorf("res11 free rooms = %v, want [atlas]", ids)
	}
	// 12: utilization -> aggregate 0.5.
	mustOK(t, results[12])
	u := results[12].Utilization
	if u.Utilization != 0.5 {
		t.Errorf("utilization = %v, want 0.5", u.Utilization)
	}
	if u.TotalBookedSeconds != 32400 {
		t.Errorf("total booked = %d, want 32400", u.TotalBookedSeconds)
	}
	// 13: unknown op -> error.
	if results[13].OK {
		t.Error("unknown op should fail")
	}
}

func TestProcess_UnknownOp(t *testing.T) {
	results, err := process([]byte(`{"op":"bogus"}`))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if results[0].OK {
		t.Fatal("unknown op should not be OK")
	}
}

func TestProcess_MalformedTime(t *testing.T) {
	// An invalid time range surfaces as an error result, not a process error.
	results, err := process([]byte(`{"op":"book","roomId":"A","start":"2026-08-15T10:00:00Z","end":"2026-08-15T10:00:00Z","booker":"x"}`))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if results[0].OK {
		t.Fatal("zero-length book should fail")
	}
}

func TestProcess_OutputJSON(t *testing.T) {
	results, err := process([]byte(`{"op":"register-room","id":"A","name":"Atlas","capacity":10}`))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	b, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(b) {
		t.Fatal("output not valid JSON")
	}
}

func mustOK(t *testing.T, r result) {
	t.Helper()
	if !r.OK {
		t.Fatalf("result %d (%s) expected OK, got error: %s", r.Index, r.Op, r.Error)
	}
}

func roomIDs(a *booking.AvailabilityResponse) []string {
	if a == nil {
		return nil
	}
	out := make([]string, len(a.Rooms))
	for i, r := range a.Rooms {
		out[i] = r.ID
	}
	return out
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
