package booking

import "time"

// RegisterRoomRequest creates a new room.
type RegisterRoomRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

// BookRequest creates a reservation. ClientRequestID enables idempotent
// retries: a repeated request with the same id returns the prior reservation
// instead of creating a new one.
type BookRequest struct {
	RoomID          string    `json:"roomId"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Booker          string    `json:"booker"`
	ClientRequestID string    `json:"clientRequestId,omitempty"`
}

// CancelRequest cancels an existing reservation.
type CancelRequest struct {
	ReservationID string `json:"reservationId"`
}

// AvailabilityRequest finds rooms free during [Start, End).
type AvailabilityRequest struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// UtilizationRequest computes utilization for a calendar date in the given
// location (default UTC). Open and Close define an optional operating window
// as "HH:MM" (defaults "00:00" and "24:00"); booked time is clamped to it.
type UtilizationRequest struct {
	Date     string `json:"date"`
	Location string `json:"location,omitempty"`
	Open     string `json:"open,omitempty"`
	Close    string `json:"close,omitempty"`
}

// AvailabilityResponse lists rooms free for the requested interval.
type AvailabilityResponse struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Rooms []Room    `json:"rooms"`
}

// RoomUtilization is the utilization of one room for the queried date.
type RoomUtilization struct {
	RoomID           string  `json:"roomId"`
	RoomName         string  `json:"roomName"`
	BookedSeconds    int     `json:"bookedSeconds"`
	AvailableSeconds int     `json:"availableSeconds"`
	Utilization      float64 `json:"utilization"`
}

// UtilizationResponse is the per-room and aggregate utilization for a date.
type UtilizationResponse struct {
	Date                  string            `json:"date"`
	Rooms                 []RoomUtilization `json:"rooms"`
	TotalBookedSeconds    int               `json:"totalBookedSeconds"`
	TotalAvailableSeconds int               `json:"totalAvailableSeconds"`
	Utilization           float64           `json:"utilization"`
}
