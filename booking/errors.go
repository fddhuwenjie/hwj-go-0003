package booking

import "errors"

// Sentinel errors returned by the service. Test for a category with
// errors.Is; some are wrapped with additional context, and ErrConflict is
// carried by *ConflictError (use errors.As).
var (
	// ErrInvalidInput is returned for malformed or missing request fields.
	ErrInvalidInput = errors.New("invalid input")
	// ErrInvalidTimeRange is returned when a time range is missing or has
	// start >= end.
	ErrInvalidTimeRange = errors.New("invalid time range: start must be before end")
	// ErrInvalidDate is returned when a date string is not YYYY-MM-DD.
	ErrInvalidDate = errors.New("invalid date: expected YYYY-MM-DD")
	// ErrInvalidOperatingWindow is returned when an open/close window is
	// malformed or open >= close.
	ErrInvalidOperatingWindow = errors.New("invalid operating window")
	// ErrRoomNotFound is returned when a referenced room does not exist.
	ErrRoomNotFound = errors.New("room not found")
	// ErrRoomAlreadyExists is returned when registering a duplicate room id.
	ErrRoomAlreadyExists = errors.New("room already exists")
	// ErrReservationNotFound is returned when a referenced reservation does
	// not exist.
	ErrReservationNotFound = errors.New("reservation not found")
	// ErrConflict is the category for a time conflict with an existing
	// reservation. The conflicting id is available via *ConflictError.
	ErrConflict = errors.New("time conflict with an existing reservation")
)

// ConflictError carries the id of the reservation that conflicts with a
// booking request. It wraps ErrConflict.
type ConflictError struct {
	ConflictingReservationID string
}

func (e *ConflictError) Error() string {
	return "time conflict with reservation " + e.ConflictingReservationID
}

func (e *ConflictError) Unwrap() error { return ErrConflict }
