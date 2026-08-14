package booking

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Service is a concurrency-safe, in-memory meeting room booking service. All
// state lives in memory and is lost when the process exits. A zero-value
// Service is not usable; construct one with NewService or NewServiceWithClock.
type Service struct {
	mu     sync.RWMutex
	now    func() time.Time
	rooms  map[string]Room
	byRoom map[string][]string // roomID -> reservation IDs in insertion order
	res    map[string]Reservation
	idem   map[string]string // clientRequestID -> reservationID
	seq    int
}

// NewService returns a Service using the system clock for timestamps.
func NewService() *Service {
	return &Service{
		now:    time.Now,
		rooms:  make(map[string]Room),
		byRoom: make(map[string][]string),
		res:    make(map[string]Reservation),
		idem:   make(map[string]string),
	}
}

// NewServiceWithClock returns a Service whose timestamps come from now. Pass a
// fixed clock for deterministic tests, or a mutable clock that advances as the
// business process progresses. The provided function is called on every
// operation that records a timestamp, so a clock that changes over time is
// observed correctly. If now is nil the system clock is used.
func NewServiceWithClock(now func() time.Time) *Service {
	s := NewService()
	if now != nil {
		s.now = now
	}
	return s
}

// RegisterRoom adds a new room. The id must be unique and non-empty; the name
// must be non-empty; capacity must be non-negative.
func (s *Service) RegisterRoom(req RegisterRoomRequest) (Room, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return Room{}, fmt.Errorf("%w: room id is required", ErrInvalidInput)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Room{}, fmt.Errorf("%w: room name is required", ErrInvalidInput)
	}
	if req.Capacity < 0 {
		return Room{}, fmt.Errorf("%w: capacity must be non-negative", ErrInvalidInput)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rooms[id]; exists {
		return Room{}, fmt.Errorf("%w: %s", ErrRoomAlreadyExists, id)
	}
	room := Room{ID: id, Name: name, Capacity: req.Capacity}
	s.rooms[id] = room
	return room, nil
}

// Book creates a reservation for [Start, End) on the given room.
//
// Duplicate and retry handling, in priority order:
//  1. ClientRequestID: if set and a prior active reservation exists for it,
//     that reservation is returned unchanged.
//  2. Content duplicate: if an active reservation already matches the same
//     room, start, end and booker, it is returned unchanged.
//  3. Conflict: if an active reservation overlaps [Start, End) on the room,
//     a *ConflictError (wrapping ErrConflict) is returned.
//
// Otherwise a new reservation is created. Adjacent reservations that share
// only a boundary instant are not considered overlapping.
func (s *Service) Book(req BookRequest) (Reservation, error) {
	if strings.TrimSpace(req.Booker) == "" {
		return Reservation{}, fmt.Errorf("%w: booker is required", ErrInvalidInput)
	}
	if req.Start.IsZero() || req.End.IsZero() {
		return Reservation{}, fmt.Errorf("%w: start and end are required", ErrInvalidInput)
	}
	if !req.Start.Before(req.End) {
		return Reservation{}, ErrInvalidTimeRange
	}
	roomID := strings.TrimSpace(req.RoomID)
	if roomID == "" {
		return Reservation{}, fmt.Errorf("%w: roomId is required", ErrInvalidInput)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.rooms[roomID]; !ok {
		return Reservation{}, fmt.Errorf("%w: %s", ErrRoomNotFound, roomID)
	}

	// 1. Idempotency key.
	if key := strings.TrimSpace(req.ClientRequestID); key != "" {
		if rid, ok := s.idem[key]; ok {
			if r, exists := s.res[rid]; exists && !r.Cancelled {
				return r, nil
			}
			// The prior reservation was cancelled or is missing; fall through
			// and re-evaluate so the key may map to a fresh reservation.
		}
	}

	// 2. Content-based duplicate.
	for _, rid := range s.byRoom[roomID] {
		r := s.res[rid]
		if r.Cancelled {
			continue
		}
		if r.Booker == req.Booker && r.Start.Equal(req.Start) && r.End.Equal(req.End) {
			return r, nil
		}
	}

	// 3. Conflict detection.
	for _, rid := range s.byRoom[roomID] {
		r := s.res[rid]
		if r.Cancelled {
			continue
		}
		if overlaps(r.Start, r.End, req.Start, req.End) {
			return Reservation{}, &ConflictError{ConflictingReservationID: r.ID}
		}
	}

	// Create.
	s.seq++
	r := Reservation{
		ID:        fmt.Sprintf("res-%06d", s.seq),
		RoomID:    roomID,
		Booker:    strings.TrimSpace(req.Booker),
		Start:     req.Start,
		End:       req.End,
		CreatedAt: s.now(),
	}
	s.res[r.ID] = r
	s.byRoom[roomID] = append(s.byRoom[roomID], r.ID)
	if key := strings.TrimSpace(req.ClientRequestID); key != "" {
		s.idem[key] = r.ID
	}
	return r, nil
}

// Cancel marks a reservation as cancelled. Cancelling an already-cancelled
// reservation is idempotent and returns its current state without error,
// which gracefully absorbs duplicate cancel requests.
func (s *Service) Cancel(req CancelRequest) (Reservation, error) {
	id := strings.TrimSpace(req.ReservationID)
	if id == "" {
		return Reservation{}, fmt.Errorf("%w: reservationId is required", ErrInvalidInput)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.res[id]
	if !ok {
		return Reservation{}, fmt.Errorf("%w: %s", ErrReservationNotFound, id)
	}
	if r.Cancelled {
		return r, nil
	}
	now := s.now()
	r.Cancelled = true
	r.CancelledAt = &now
	s.res[id] = r
	return r, nil
}

// Available returns the rooms with no active reservation overlapping
// [Start, End). Results are sorted by room id for deterministic output.
func (s *Service) Available(req AvailabilityRequest) (AvailabilityResponse, error) {
	if !req.Start.Before(req.End) {
		return AvailabilityResponse{}, ErrInvalidTimeRange
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rooms := make([]Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		free := true
		for _, rid := range s.byRoom[room.ID] {
			r := s.res[rid]
			if r.Cancelled {
				continue
			}
			if overlaps(r.Start, r.End, req.Start, req.End) {
				free = false
				break
			}
		}
		if free {
			rooms = append(rooms, room)
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	return AvailabilityResponse{Start: req.Start, End: req.End, Rooms: rooms}, nil
}

// Utilization computes per-room and aggregate booked time for a calendar date.
//
// The date is interpreted in Location (default UTC). Booked time for each
// reservation is clamped to the operating window [Open, Close) on that date
// (defaults 00:00-24:00, i.e. the full day). Utilization is booked divided by
// the window length; reservations spanning midnight or outside the window are
// prorated correctly. Cancelled reservations are excluded.
func (s *Service) Utilization(req UtilizationRequest) (UtilizationResponse, error) {
	loc, err := loadLocation(req.Location)
	if err != nil {
		return UtilizationResponse{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	day, err := time.ParseInLocation("2006-01-02", req.Date, loc)
	if err != nil {
		return UtilizationResponse{}, fmt.Errorf("%w: %q", ErrInvalidDate, req.Date)
	}
	open, closeDur, err := parseWindow(req.Open, req.Close)
	if err != nil {
		return UtilizationResponse{}, err
	}
	windowStart := localClockTime(day, open)
	windowEnd := localClockTime(day, closeDur)
	available := windowEnd.Sub(windowStart)

	s.mu.RLock()
	defer s.mu.RUnlock()

	rooms := make([]Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })

	out := UtilizationResponse{Date: req.Date, Rooms: make([]RoomUtilization, 0, len(rooms))}
	var totalBooked, totalAvail int
	for _, room := range rooms {
		var booked time.Duration
		for _, rid := range s.byRoom[room.ID] {
			r := s.res[rid]
			if r.Cancelled {
				continue
			}
			booked += clampDuration(r.Start, r.End, windowStart, windowEnd)
		}
		ru := RoomUtilization{
			RoomID:           room.ID,
			RoomName:         room.Name,
			BookedSeconds:    int(booked.Seconds()),
			AvailableSeconds: int(available.Seconds()),
		}
		if available > 0 {
			ru.Utilization = float64(booked) / float64(available)
		}
		out.Rooms = append(out.Rooms, ru)
		totalBooked += ru.BookedSeconds
		totalAvail += ru.AvailableSeconds
	}
	out.TotalBookedSeconds = totalBooked
	out.TotalAvailableSeconds = totalAvail
	if totalAvail > 0 {
		out.Utilization = float64(totalBooked) / float64(totalAvail)
	}
	return out, nil
}

// overlaps reports whether [s1,e1) and [s2,e2) share any time. Adjacent
// intervals (e1 == s2 or e2 == s1) do not overlap.
func overlaps(s1, e1, s2, e2 time.Time) bool {
	return s1.Before(e2) && s2.Before(e1)
}

// clampDuration returns the duration that [rStart, rEnd) overlaps
// [wStart, wEnd), zero if they do not intersect.
func clampDuration(rStart, rEnd, wStart, wEnd time.Time) time.Duration {
	start := rStart
	if wStart.After(start) {
		start = wStart
	}
	end := rEnd
	if wEnd.Before(end) {
		end = wEnd
	}
	if !start.Before(end) {
		return 0
	}
	return end.Sub(start)
}

func loadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

// parseWindow resolves the open/close strings to durations since midnight.
// Defaults are "00:00" and "24:00"; open must precede close.
func parseWindow(openStr, closeStr string) (open, closeDur time.Duration, err error) {
	if openStr == "" {
		openStr = "00:00"
	}
	if closeStr == "" {
		closeStr = "24:00"
	}
	open, err = parseClockOfDay(openStr, false)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: open %q: %v", ErrInvalidOperatingWindow, openStr, err)
	}
	closeDur, err = parseClockOfDay(closeStr, true)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: close %q: %v", ErrInvalidOperatingWindow, closeStr, err)
	}
	if open >= closeDur {
		return 0, 0, fmt.Errorf("%w: open must be before close", ErrInvalidOperatingWindow)
	}
	return open, closeDur, nil
}

func localClockTime(day time.Time, offset time.Duration) time.Time {
	year, month, date := day.Date()
	if offset == 24*time.Hour {
		return time.Date(year, month, date+1, 0, 0, 0, 0, day.Location())
	}
	hour := int(offset / time.Hour)
	minute := int((offset % time.Hour) / time.Minute)
	return time.Date(year, month, date, hour, minute, 0, 0, day.Location())
}

// parseClockOfDay parses an "HH:MM" 24-hour clock into a duration since
// midnight. When allowEndOfDay is true, "24:00" is accepted to represent the
// end of the day.
func parseClockOfDay(s string, allowEndOfDay bool) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	if h < 0 || m < 0 || m > 59 {
		return 0, fmt.Errorf("out of range: %q", s)
	}
	if h == 24 {
		if !allowEndOfDay || m != 0 {
			return 0, fmt.Errorf("hour 24 only allowed as 24:00: %q", s)
		}
	} else if h > 23 {
		return 0, fmt.Errorf("hour out of range: %q", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}
