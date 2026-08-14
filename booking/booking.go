// Package booking implements an in-memory meeting room booking and
// utilization service using only the Go standard library.
//
// A reservation occupies a room for a half-open time interval [Start, End):
// the End instant is exclusive, so two reservations that share only a boundary
// instant (one ends at 10:00, the next starts at 10:00) do not conflict. This
// convention is applied uniformly across booking, availability and
// utilization.
package booking

import (
	"time"

	// Embed the timezone database so time.LoadLocation works in fully
	// offline builds even when the host has no system tz database.
	_ "time/tzdata"
)

// Room is a registered meeting room.
type Room struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

// Reservation is a booking for a room over a half-open interval [Start, End).
// A cancelled reservation is retained for history but no longer blocks the
// room or contributes to utilization.
type Reservation struct {
	ID          string     `json:"id"`
	RoomID      string     `json:"roomId"`
	Booker      string     `json:"booker"`
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	CreatedAt   time.Time  `json:"createdAt"`
	Cancelled   bool       `json:"cancelled"`
	CancelledAt *time.Time `json:"cancelledAt,omitempty"`
}
