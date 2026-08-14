// Command meetingroom is a JSON command-line interface to the in-memory
// booking service. It reads a batch of commands (a JSON array, or a single
// JSON object) from an input file or stdin, replays them against a fresh
// Service, and writes a JSON array of results.
//
// This batch design keeps the in-memory service meaningful: one process, one
// service instance, a full workflow. Each result mirrors its command and is
// marked ok=true on success or carries an error string on failure.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fddhuwenjie/hwj-go-0003/booking"
)

// command is the flat envelope for every operation. Only the fields relevant
// to op are populated.
type command struct {
	Op              string    `json:"op"`
	ID              string    `json:"id,omitempty"`
	Name            string    `json:"name,omitempty"`
	Capacity        int       `json:"capacity,omitempty"`
	RoomID          string    `json:"roomId,omitempty"`
	Start           time.Time `json:"start,omitempty"`
	End             time.Time `json:"end,omitempty"`
	Booker          string    `json:"booker,omitempty"`
	ClientRequestID string    `json:"clientRequestId,omitempty"`
	ReservationID   string    `json:"reservationId,omitempty"`
	Date            string    `json:"date,omitempty"`
	Location        string    `json:"location,omitempty"`
	Open            string    `json:"open,omitempty"`
	Close           string    `json:"close,omitempty"`
}

// result is the outcome of one command, aligned by index to the input.
type result struct {
	Index       int                           `json:"index"`
	Op          string                        `json:"op"`
	OK          bool                          `json:"ok"`
	Error       string                        `json:"error,omitempty"`
	Room        *booking.Room                 `json:"room,omitempty"`
	Reservation *booking.Reservation          `json:"reservation,omitempty"`
	Available   *booking.AvailabilityResponse `json:"available,omitempty"`
	Utilization *booking.UtilizationResponse  `json:"utilization,omitempty"`
}

func main() {
	var inPath, outPath string
	flag.StringVar(&inPath, "in", "-", `input JSON file ("-" for stdin)`)
	flag.StringVar(&outPath, "out", "-", `output JSON file ("-" for stdout)`)
	flag.Parse()

	if err := run(inPath, outPath); err != nil {
		fmt.Fprintln(os.Stderr, "meetingroom:", err)
		os.Exit(1)
	}
}

func run(inPath, outPath string) error {
	data, err := readInput(inPath)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	results, err := process(data)
	if err != nil {
		return fmt.Errorf("process: %w", err)
	}
	return writeOutput(outPath, results)
}

// process parses a batch and executes it against a fresh service.
func process(data []byte) ([]result, error) {
	cmds, err := parseCommands(data)
	if err != nil {
		return nil, err
	}
	svc := booking.NewService()
	return execute(cmds, svc), nil
}

// parseCommands accepts a JSON array of commands or a single command object.
func parseCommands(data []byte) ([]command, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("empty input")
	}
	var cmds []command
	if err := json.Unmarshal(trimmed, &cmds); err == nil {
		return cmds, nil
	}
	var single command
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return []command{single}, nil
	}
	return nil, errors.New("input must be a JSON array or object of commands")
}

// execute runs each command in order, collecting results.
func execute(cmds []command, svc *booking.Service) []result {
	results := make([]result, len(cmds))
	for i, c := range cmds {
		results[i] = dispatch(c, svc)
		results[i].Index = i
	}
	return results
}

func dispatch(c command, svc *booking.Service) result {
	res := result{Op: c.Op}
	switch c.Op {
	case "register-room":
		room, err := svc.RegisterRoom(booking.RegisterRoomRequest{
			ID: c.ID, Name: c.Name, Capacity: c.Capacity,
		})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Room = &room
	case "book":
		r, err := svc.Book(booking.BookRequest{
			RoomID: c.RoomID, Start: c.Start, End: c.End,
			Booker: c.Booker, ClientRequestID: c.ClientRequestID,
		})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Reservation = &r
	case "cancel":
		r, err := svc.Cancel(booking.CancelRequest{ReservationID: c.ReservationID})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Reservation = &r
	case "available":
		a, err := svc.Available(booking.AvailabilityRequest{Start: c.Start, End: c.End})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Available = &a
	case "utilization":
		u, err := svc.Utilization(booking.UtilizationRequest{
			Date: c.Date, Location: c.Location, Open: c.Open, Close: c.Close,
		})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Utilization = &u
	default:
		res.Error = fmt.Sprintf("unknown op: %q", c.Op)
	}
	return res
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func writeOutput(path string, results []result) error {
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if path == "-" {
		_, err := os.Stdout.Write(out)
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(out)
	return err
}
