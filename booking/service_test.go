package booking

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedClock returns a clock pinned to a constant instant and that instant.
func fixedClock() (func() time.Time, time.Time) {
	t := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }, t
}

func mustRoom(t *testing.T, s *Service, req RegisterRoomRequest) Room {
	t.Helper()
	r, err := s.RegisterRoom(req)
	if err != nil {
		t.Fatalf("RegisterRoom(%+v): %v", req, err)
	}
	return r
}

func day(hour, min int) time.Time {
	return time.Date(2026, 8, 15, hour, min, 0, 0, time.UTC)
}

func mustBook(t *testing.T, s *Service, req BookRequest) Reservation {
	t.Helper()
	r, err := s.Book(req)
	if err != nil {
		t.Fatalf("Book(%+v): %v", req, err)
	}
	return r
}

func TestRegisterRoom(t *testing.T) {
	clock, now := fixedClock()
	_ = now
	s := NewServiceWithClock(clock)

	r := mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	if r.ID != "A" || r.Name != "Atlas" || r.Capacity != 10 {
		t.Fatalf("unexpected room %+v", r)
	}
	trimmed := mustRoom(t, s, RegisterRoomRequest{ID: " B ", Name: " Borealis ", Capacity: 4})
	if trimmed.ID != "B" || trimmed.Name != "Borealis" {
		t.Fatalf("room identifiers and names should be normalized: %+v", trimmed)
	}
	if _, err := s.Book(BookRequest{RoomID: "B", Start: day(7, 0), End: day(8, 0), Booker: "bob"}); err != nil {
		t.Fatalf("normalized room id should be usable: %v", err)
	}

	// Capacity zero is a legitimate (if tiny) room.
	if _, err := s.RegisterRoom(RegisterRoomRequest{ID: "Z", Name: "Zero", Capacity: 0}); err != nil {
		t.Fatalf("capacity 0 should be allowed: %v", err)
	}

	// Duplicate id.
	_, err := s.RegisterRoom(RegisterRoomRequest{ID: "A", Name: "Dup", Capacity: 1})
	if !errors.Is(err, ErrRoomAlreadyExists) {
		t.Fatalf("duplicate id: want ErrRoomAlreadyExists, got %v", err)
	}

	// Invalid inputs.
	cases := []struct {
		name string
		req  RegisterRoomRequest
		want error
	}{
		{"empty id", RegisterRoomRequest{ID: "  ", Name: "X"}, ErrInvalidInput},
		{"empty name", RegisterRoomRequest{ID: "B", Name: "  "}, ErrInvalidInput},
		{"negative capacity", RegisterRoomRequest{ID: "C", Name: "X", Capacity: -1}, ErrInvalidInput},
	}
	for _, c := range cases {
		_, err := s.RegisterRoom(c.req)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
}

func TestBook_BasicAndBoundaries(t *testing.T) {
	clock, now := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	r := mustBook(t, s, BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})
	if r.ID != "res-000001" {
		t.Fatalf("id = %q, want res-000001", r.ID)
	}
	if !r.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", r.CreatedAt, now)
	}
	if !r.Start.Equal(day(9, 0)) || !r.End.Equal(day(10, 0)) {
		t.Fatalf("reservation times wrong: %+v", r)
	}
	if r.Cancelled {
		t.Fatalf("new reservation should not be cancelled")
	}

	// Adjacent booking (10:00 start == previous end) is allowed: boundary.
	mustBook(t, s, BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "carol"})

	// Invalid time ranges.
	if _, err := s.Book(BookRequest{RoomID: "A", Start: day(10, 0), End: day(10, 0), Booker: "x"}); !errors.Is(err, ErrInvalidTimeRange) {
		t.Errorf("start==end: want ErrInvalidTimeRange, got %v", err)
	}
	if _, err := s.Book(BookRequest{RoomID: "A", Start: day(11, 0), End: day(10, 0), Booker: "x"}); !errors.Is(err, ErrInvalidTimeRange) {
		t.Errorf("start>end: want ErrInvalidTimeRange, got %v", err)
	}

	// Missing room.
	if _, err := s.Book(BookRequest{RoomID: "ghost", Start: day(9, 0), End: day(10, 0), Booker: "x"}); !errors.Is(err, ErrRoomNotFound) {
		t.Errorf("unknown room: want ErrRoomNotFound, got %v", err)
	}

	// Empty booker.
	if _, err := s.Book(BookRequest{RoomID: "A", Start: day(12, 0), End: day(13, 0), Booker: "  "}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty booker: want ErrInvalidInput, got %v", err)
	}

	// Overlapping booking returns a ConflictError carrying the conflicting id.
	_, err := s.Book(BookRequest{RoomID: "A", Start: day(9, 30), End: day(10, 30), Booker: "bob"})
	if err == nil {
		t.Fatalf("expected conflict, got nil")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConflictError, got %T: %v", err, err)
	}
	if ce.ConflictingReservationID != "res-000001" {
		t.Fatalf("conflicting id = %q, want res-000001", ce.ConflictingReservationID)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict should wrap ErrConflict, got %v", err)
	}
}

func TestBook_DuplicateAndIdempotency(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	r1 := mustBook(t, s, BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})

	// Same content without a key -> returns the existing reservation.
	r2, err := s.Book(BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})
	if err != nil {
		t.Fatalf("duplicate content: %v", err)
	}
	if r2.ID != r1.ID {
		t.Fatalf("duplicate content should return same id: %q vs %q", r2.ID, r1.ID)
	}

	// Same content with an idempotency key -> returns the existing reservation.
	r3, err := s.Book(BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice", ClientRequestID: "k1"})
	if err != nil || r3.ID != r1.ID {
		t.Fatalf("idempotency key on existing content: id=%q err=%v", r3.ID, err)
	}

	// Different booker, same slot -> conflict (not a duplicate).
	if _, err := s.Book(BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "bob"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different booker same slot: want ErrConflict, got %v", err)
	}

	// Idempotency key on a fresh booking.
	r4, err := s.Book(BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "dave", ClientRequestID: "k2"})
	if err != nil {
		t.Fatalf("fresh book: %v", err)
	}
	if r4.ID != "res-000002" {
		t.Fatalf("fresh book id = %q, want res-000002", r4.ID)
	}
	r5, err := s.Book(BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "dave", ClientRequestID: "k2"})
	if err != nil || r5.ID != r4.ID {
		t.Fatalf("idempotent retry should return same id: %q err=%v", r5.ID, err)
	}
}

func TestCancel_AndRebook(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	r1 := mustBook(t, s, BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice", ClientRequestID: "k1"})

	// Cancel.
	c, err := s.Cancel(CancelRequest{ReservationID: r1.ID})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !c.Cancelled || c.ID != r1.ID {
		t.Fatalf("cancel result wrong: %+v", c)
	}

	// Duplicate cancel is idempotent: returns the cancelled state, no error.
	c2, err := s.Cancel(CancelRequest{ReservationID: r1.ID})
	if err != nil {
		t.Fatalf("double cancel should be idempotent: %v", err)
	}
	if !c2.Cancelled || c2.ID != r1.ID {
		t.Fatalf("double cancel result wrong: %+v", c2)
	}

	// Cancel unknown id.
	if _, err := s.Cancel(CancelRequest{ReservationID: "nope"}); !errors.Is(err, ErrReservationNotFound) {
		t.Errorf("cancel unknown: want ErrReservationNotFound, got %v", err)
	}
	// Cancel empty id.
	if _, err := s.Cancel(CancelRequest{ReservationID: "  "}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("cancel empty: want ErrInvalidInput, got %v", err)
	}

	// After cancel, the slot is free for a fresh booking.
	r3, err := s.Book(BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})
	if err != nil {
		t.Fatalf("rebook after cancel: %v", err)
	}
	if r3.ID == r1.ID {
		t.Fatalf("rebook should create a new reservation id")
	}

	// Reusing the idempotency key of a cancelled reservation creates a new one
	// and rebinds the key to it.
	r4, err := s.Book(BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "eve", ClientRequestID: "k2"})
	if err != nil {
		t.Fatalf("first use of k2: %v", err)
	}
	if _, err := s.Cancel(CancelRequest{ReservationID: r4.ID}); err != nil {
		t.Fatalf("cancel r4: %v", err)
	}
	r5, err := s.Book(BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "eve", ClientRequestID: "k2"})
	if err != nil {
		t.Fatalf("reuse k2 after cancel: %v", err)
	}
	if r5.ID == r4.ID {
		t.Fatalf("reusing key of cancelled reservation should create a new id")
	}
}

func TestAvailable(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	mustRoom(t, s, RegisterRoomRequest{ID: "B", Name: "Borealis", Capacity: 8})

	mustBook(t, s, BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})

	ids := func(resp AvailabilityResponse) []string {
		out := make([]string, len(resp.Rooms))
		for i, r := range resp.Rooms {
			out[i] = r.ID
		}
		return out
	}

	// 09:30-10:00 overlaps A's booking -> only B free.
	resp, err := s.Available(AvailabilityRequest{Start: day(9, 30), End: day(10, 0)})
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if got := ids(resp); !equal(got, []string{"B"}) {
		t.Fatalf("09:30-10:00 free = %v, want [B]", got)
	}

	// 10:00-11:00 is adjacent to A's booking (ends 10:00) -> both free.
	resp, err = s.Available(AvailabilityRequest{Start: day(10, 0), End: day(11, 0)})
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if got := ids(resp); !equal(got, []string{"A", "B"}) {
		t.Fatalf("10:00-11:00 free = %v, want [A B]", got)
	}

	// Before any booking -> both free.
	resp, err = s.Available(AvailabilityRequest{Start: day(8, 0), End: day(9, 0)})
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if got := ids(resp); !equal(got, []string{"A", "B"}) {
		t.Fatalf("08:00-09:00 free = %v, want [A B]", got)
	}

	// Invalid range.
	if _, err := s.Available(AvailabilityRequest{Start: day(10, 0), End: day(10, 0)}); !errors.Is(err, ErrInvalidTimeRange) {
		t.Errorf("zero-length available: want ErrInvalidTimeRange, got %v", err)
	}

	// A cancelled reservation frees the room.
	if _, err := s.Cancel(CancelRequest{ReservationID: "res-000001"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	resp, err = s.Available(AvailabilityRequest{Start: day(9, 0), End: day(10, 0)})
	if err != nil {
		t.Fatalf("available after cancel: %v", err)
	}
	if got := ids(resp); !equal(got, []string{"A", "B"}) {
		t.Fatalf("after cancel 09:00-10:00 free = %v, want [A B]", got)
	}
}

func TestUtilization(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	mustRoom(t, s, RegisterRoomRequest{ID: "B", Name: "Borealis", Capacity: 8})

	// Window 09:00-18:00 = 9h = 32400s.
	mustBook(t, s, BookRequest{RoomID: "A", Start: day(10, 0), End: day(11, 0), Booker: "carol"}) // 1h in window
	mustBook(t, s, BookRequest{RoomID: "B", Start: day(9, 0), End: day(17, 0), Booker: "dave"})   // 8h in window
	mustBook(t, s, BookRequest{RoomID: "A", Start: day(18, 0), End: day(19, 0), Booker: "after"}) // outside window -> 0

	resp, err := s.Utilization(UtilizationRequest{Date: "2026-08-15", Location: "UTC", Open: "09:00", Close: "18:00"})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}

	find := func(id string) RoomUtilization {
		for _, r := range resp.Rooms {
			if r.RoomID == id {
				return r
			}
		}
		t.Fatalf("room %s not in response", id)
		return RoomUtilization{}
	}

	a := find("A")
	if a.BookedSeconds != 3600 {
		t.Errorf("A booked = %d, want 3600", a.BookedSeconds)
	}
	if a.AvailableSeconds != 32400 {
		t.Errorf("A available = %d, want 32400", a.AvailableSeconds)
	}
	if a.Utilization != 1.0/9.0 {
		t.Errorf("A utilization = %v, want %v", a.Utilization, 1.0/9.0)
	}

	b := find("B")
	if b.BookedSeconds != 28800 {
		t.Errorf("B booked = %d, want 28800", b.BookedSeconds)
	}
	if b.Utilization != 8.0/9.0 {
		t.Errorf("B utilization = %v, want %v", b.Utilization, 8.0/9.0)
	}

	// Aggregates: total booked = 3600 + 28800 = 32400; total available = 64800.
	if resp.TotalBookedSeconds != 32400 {
		t.Errorf("total booked = %d, want 32400", resp.TotalBookedSeconds)
	}
	if resp.TotalAvailableSeconds != 64800 {
		t.Errorf("total available = %d, want 64800", resp.TotalAvailableSeconds)
	}
	if resp.Utilization != 0.5 {
		t.Errorf("aggregate utilization = %v, want 0.5", resp.Utilization)
	}
}

func TestUtilization_PartialAndMidnight(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	// Partially in a 09:00-18:00 window: 08:00-10:00 contributes 1h.
	mustBook(t, s, BookRequest{RoomID: "A", Start: day(8, 0), End: day(10, 0), Booker: "early"})
	resp, err := s.Utilization(UtilizationRequest{Date: "2026-08-15", Open: "09:00", Close: "18:00"})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if resp.Rooms[0].BookedSeconds != 3600 {
		t.Errorf("partial booking booked = %d, want 3600", resp.Rooms[0].BookedSeconds)
	}

	// Full-day window (00:00-24:00); a reservation crossing midnight contributes
	// only the part inside the queried day (23:00 -> 24:00 = 1h).
	s2 := NewServiceWithClock(clock)
	mustRoom(t, s2, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	mustBook(t, s2, BookRequest{RoomID: "A",
		Start:  time.Date(2026, 8, 15, 23, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC),
		Booker: "night"})
	resp, err = s2.Utilization(UtilizationRequest{Date: "2026-08-15"})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if resp.Rooms[0].BookedSeconds != 3600 {
		t.Errorf("midnight-spanning booked = %d, want 3600", resp.Rooms[0].BookedSeconds)
	}
	if resp.Rooms[0].AvailableSeconds != 86400 {
		t.Errorf("full-day available = %d, want 86400", resp.Rooms[0].AvailableSeconds)
	}
}

func TestUtilization_TimeZone(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	// 09:00-10:00 New York local time on 2026-08-15 (EDT, UTC-4).
	loc, err := loadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	mustBook(t, s, BookRequest{RoomID: "A",
		Start:  time.Date(2026, 8, 15, 9, 0, 0, 0, loc),
		End:    time.Date(2026, 8, 15, 10, 0, 0, 0, loc),
		Booker: "ny"})

	resp, err := s.Utilization(UtilizationRequest{Date: "2026-08-15", Location: "America/New_York", Open: "09:00", Close: "10:00"})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if resp.Rooms[0].BookedSeconds != 3600 || resp.Rooms[0].AvailableSeconds != 3600 {
		t.Errorf("tz utilization booked=%d avail=%d, want 3600/3600", resp.Rooms[0].BookedSeconds, resp.Rooms[0].AvailableSeconds)
	}
	if resp.Rooms[0].Utilization != 1.0 {
		t.Errorf("tz utilization = %v, want 1.0", resp.Rooms[0].Utilization)
	}
}

func TestUtilization_DaylightSavingCalendarWindow(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	loc, err := loadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	start := time.Date(2026, 3, 8, 9, 0, 0, 0, loc)
	mustBook(t, s, BookRequest{RoomID: "A", Start: start, End: start.Add(time.Hour), Booker: "ny"})

	resp, err := s.Utilization(UtilizationRequest{
		Date: "2026-03-08", Location: "America/New_York", Open: "09:00", Close: "10:00",
	})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if got := resp.Rooms[0]; got.BookedSeconds != 3600 || got.AvailableSeconds != 3600 {
		t.Fatalf("DST calendar window booked=%d available=%d, want 3600/3600", got.BookedSeconds, got.AvailableSeconds)
	}

	fullDay, err := s.Utilization(UtilizationRequest{Date: "2026-03-08", Location: "America/New_York"})
	if err != nil {
		t.Fatalf("full-day utilization: %v", err)
	}
	if fullDay.Rooms[0].AvailableSeconds != 23*60*60 {
		t.Fatalf("spring-forward day has %d available seconds, want %d", fullDay.Rooms[0].AvailableSeconds, 23*60*60)
	}
}

func TestUtilization_CancelledExcluded(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
	r := mustBook(t, s, BookRequest{RoomID: "A", Start: day(9, 0), End: day(10, 0), Booker: "alice"})
	if _, err := s.Cancel(CancelRequest{ReservationID: r.ID}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	resp, err := s.Utilization(UtilizationRequest{Date: "2026-08-15", Open: "09:00", Close: "18:00"})
	if err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if resp.Rooms[0].BookedSeconds != 0 {
		t.Errorf("cancelled should not count: booked=%d", resp.Rooms[0].BookedSeconds)
	}
}

func TestUtilization_InvalidInput(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	cases := []struct {
		name string
		req  UtilizationRequest
		want error
	}{
		{"bad date", UtilizationRequest{Date: "2026/08/15"}, ErrInvalidDate},
		{"empty date", UtilizationRequest{Date: ""}, ErrInvalidDate},
		{"bad open hour", UtilizationRequest{Date: "2026-08-15", Open: "25:00", Close: "26:00"}, ErrInvalidOperatingWindow},
		{"bad minute", UtilizationRequest{Date: "2026-08-15", Open: "09:99"}, ErrInvalidOperatingWindow},
		{"open >= close", UtilizationRequest{Date: "2026-08-15", Open: "12:00", Close: "12:00"}, ErrInvalidOperatingWindow},
		{"open after close", UtilizationRequest{Date: "2026-08-15", Open: "13:00", Close: "12:00"}, ErrInvalidOperatingWindow},
		{"bad location", UtilizationRequest{Date: "2026-08-15", Location: "Mars/Olympus"}, ErrInvalidInput},
	}
	for _, c := range cases {
		_, err := s.Utilization(c.req)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
}

// TestBook_ConcurrentDistinct verifies the service is race-free under
// concurrent bookings on distinct (non-overlapping) slots.
func TestBook_ConcurrentDistinct(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "R", Name: "R", Capacity: 10})

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			st := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
			if _, err := s.Book(BookRequest{RoomID: "R", Start: st, End: st.Add(time.Hour), Booker: fmt.Sprintf("u%d", i)}); err != nil {
				t.Errorf("book %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	// Invariant: no two active reservations on the same room overlap.
	assertNoOverlaps(t, s, "R")
}

// TestBook_ConcurrentSameSlot verifies that many goroutines racing to book the
// identical slot produce exactly one reservation and conflicts for the rest.
func TestBook_ConcurrentSameSlot(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "R", Name: "R", Capacity: 10})

	const N = 200
	var ok, fail int64
	var wg sync.WaitGroup
	wg.Add(N)
	st := day(9, 0)
	for i := 0; i < N; i++ {
		i := i
		// Distinct bookers make each request a genuine (non-duplicate) claim
		// for the same slot, so only the first can succeed.
		go func() {
			defer wg.Done()
			_, err := s.Book(BookRequest{RoomID: "R", Start: st, End: st.Add(time.Hour), Booker: fmt.Sprintf("u%d", i)})
			if err == nil {
				atomic.AddInt64(&ok, 1)
			} else if errors.Is(err, ErrConflict) {
				atomic.AddInt64(&fail, 1)
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if ok != 1 {
		t.Errorf("expected exactly 1 successful booking, got %d", ok)
	}
	if fail != N-1 {
		t.Errorf("expected %d conflicts, got %d", N-1, fail)
	}
	assertNoOverlaps(t, s, "R")
}

// assertNoOverlaps loads all active reservations for roomID via the Available
// path indirectly is not enough; here we reconstruct from a probe: we book a
// sentinel and rely on the service's own overlap invariant by scanning. Since
// there is no public list API, we verify the invariant through cancellation
// counting via the id sequence: every active reservation must be disjoint.
func assertNoOverlaps(t *testing.T, s *Service, roomID string) {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var active []Reservation
	for _, rid := range s.byRoom[roomID] {
		r := s.res[rid]
		if !r.Cancelled {
			active = append(active, r)
		}
	}
	for i := 0; i < len(active); i++ {
		for j := i + 1; j < len(active); j++ {
			if overlaps(active[i].Start, active[i].End, active[j].Start, active[j].End) {
				t.Errorf("overlap between %s and %s", active[i].ID, active[j].ID)
			}
		}
	}
}

// mustParseRFC3339 parses an RFC3339 timestamp, failing the test on error. It
// preserves the input's time-zone offset (including negative offsets), which is
// how JSON-decoded BookRequest fields arrive at the service.
func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return ts
}

// TestBook_CrossTimeZoneConflict reproduces the cross-timezone double-booking
// defect: a room reserved with a UTC interval must still reject an overlapping
// reservation whose RFC3339 instants use a negative offset. Boundary-adjacent
// reservations across time zones remain valid, and Available agrees with Book.
func TestBook_CrossTimeZoneConflict(t *testing.T) {
	clock, _ := fixedClock()
	s := NewServiceWithClock(clock)
	mustRoom(t, s, RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})

	// Reserve 10:00-11:00 UTC.
	utcStart := mustParseRFC3339(t, "2026-08-15T10:00:00Z")
	utcEnd := mustParseRFC3339(t, "2026-08-15T11:00:00Z")
	r1 := mustBook(t, s, BookRequest{RoomID: "A", Start: utcStart, End: utcEnd, Booker: "alice"})

	// 05:30-06:30 at -05:00 is 10:30-11:30 UTC: it overlaps the first booking
	// and must be rejected even though its wall-clock representation differs.
	negStart := mustParseRFC3339(t, "2026-08-15T05:30:00-05:00")
	negEnd := mustParseRFC3339(t, "2026-08-15T06:30:00-05:00")
	if !negStart.Equal(utcStart.Add(30 * time.Minute)) {
		t.Fatalf("setup invariant: %v != %v+30m", negStart, utcStart)
	}
	_, err := s.Book(BookRequest{RoomID: "A", Start: negStart, End: negEnd, Booker: "bob"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping negative-offset book: want ErrConflict, got %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.ConflictingReservationID != r1.ID {
		t.Fatalf("conflict should point at %s, got %+v", r1.ID, err)
	}

	// Available must agree: the overlapping window is not free for room A.
	resp, err := s.Available(AvailabilityRequest{Start: negStart, End: negEnd})
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if ids := roomIDs(resp); len(ids) != 0 {
		t.Fatalf("available for overlapping negative-offset window = %v, want []", ids)
	}

	// Boundary case: a reservation starting exactly at the first reservation's
	// ending instant, expressed with a negative offset, is adjacent and valid.
	// 06:00 at -05:00 is 11:00 UTC == r1.End.
	adjStart := mustParseRFC3339(t, "2026-08-15T06:00:00-05:00")
	adjEnd := mustParseRFC3339(t, "2026-08-15T07:00:00-05:00")
	if !adjStart.Equal(utcEnd) {
		t.Fatalf("setup invariant: %v != %v", adjStart, utcEnd)
	}

	// Available agrees the adjacent slot is free for room A before booking.
	resp, err = s.Available(AvailabilityRequest{Start: adjStart, End: adjEnd})
	if err != nil {
		t.Fatalf("available adjacent: %v", err)
	}
	if ids := roomIDs(resp); !equal(ids, []string{"A"}) {
		t.Fatalf("available for adjacent negative-offset window = %v, want [A]", ids)
	}

	// Book the adjacent slot: succeeds because it only shares a boundary.
	r2 := mustBook(t, s, BookRequest{RoomID: "A", Start: adjStart, End: adjEnd, Booker: "carol"})
	if r2.ID == r1.ID {
		t.Fatalf("adjacent negative-offset book should create a new reservation")
	}

	// After booking, Available agrees the slot is now occupied.
	resp, err = s.Available(AvailabilityRequest{Start: adjStart, End: adjEnd})
	if err != nil {
		t.Fatalf("available after adjacent book: %v", err)
	}
	if ids := roomIDs(resp); len(ids) != 0 {
		t.Fatalf("available for booked adjacent window = %v, want []", ids)
	}
}

func roomIDs(resp AvailabilityResponse) []string {
	out := make([]string, len(resp.Rooms))
	for i, r := range resp.Rooms {
		out[i] = r.ID
	}
	return out
}

func equal(a, b []string) bool {
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
