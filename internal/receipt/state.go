package receipt

import (
	"fmt"
	"sync"

	"boltgo/internal/proto"
)

// State represents a receipt's position in the 7-state lifecycle.
type State int

const (
	StateInitiated      State = iota // freshly created
	StateStreamOpened                // bidi stream opened
	StateDataTransferred             // all bytes flushed
	StateStreamClosed                // remote closed
	StateProcessing                  // checksum verified, yielded to app
	StateCompleted                   // terminal: acked
	StateFailed                      // terminal: error / nack
)

func (s State) String() string {
	switch s {
	case StateInitiated:
		return "INITIATED"
	case StateStreamOpened:
		return "STREAM_OPENED"
	case StateDataTransferred:
		return "DATA_TRANSFERRED"
	case StateStreamClosed:
		return "STREAM_CLOSED"
	case StateProcessing:
		return "PROCESSING"
	case StateCompleted:
		return "COMPLETED"
	case StateFailed:
		return "FAILED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", s)
	}
}

// IsTerminal returns true for COMPLETED and FAILED.
func (s State) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed
}

// Event represents a state transition trigger.
type Event struct {
	Type   EventType
	Detail string // for failures
}

type EventType int

const (
	EventOpen       EventType = iota // stream opened
	EventClose                       // stream closed
	EventProcess                     // checksum verified
	EventAck                         // app called ack
	EventNack                        // app called nack with reason
	EventError                       // error occurred
)

// Transition returns the new state given the current state and event.
// Returns an error if the transition is invalid.
func Transition(current State, evt Event) (State, error) {
	switch current {
	case StateInitiated:
		switch evt.Type {
		case EventOpen:
			return StateStreamOpened, nil
		case EventClose, EventError:
			return StateFailed, nil
		}
	case StateStreamOpened:
		switch evt.Type {
		case EventClose:
			return StateDataTransferred, nil
		case EventProcess:
			return StateProcessing, nil
		case EventError:
			return StateFailed, nil
		}
	case StateDataTransferred:
		switch evt.Type {
		case EventClose:
			return StateStreamClosed, nil
		case EventProcess:
			return StateProcessing, nil
		case EventError:
			return StateFailed, nil
		}
	case StateStreamClosed:
		switch evt.Type {
		case EventProcess:
			return StateProcessing, nil
		case EventError:
			return StateFailed, nil
		}
	case StateProcessing:
		switch evt.Type {
		case EventAck:
			return StateCompleted, nil
		case EventNack, EventError:
			return StateFailed, nil
		}
	}
	return current, fmt.Errorf("invalid transition: %s + event %d", current, evt.Type)
}

// Receipt is a thread-safe receipt object tracking transfer state.
type Receipt struct {
	mu     sync.Mutex
	id     string
	state  State
	reason string // failure reason
}

// NewReceipt creates a receipt in the INITIATED state.
func NewReceipt(id string) *Receipt {
	return &Receipt{id: id, state: StateInitiated}
}

func (r *Receipt) ID() string     { return r.id }
func (r *Receipt) State() State   { r.mu.Lock(); defer r.mu.Unlock(); return r.state }
func (r *Receipt) Reason() string { r.mu.Lock(); defer r.mu.Unlock(); return r.reason }

// ApplyEvent attempts a state transition. Returns the new state.
func (r *Receipt) ApplyEvent(evt Event) (State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.IsTerminal() {
		return r.state, nil // no-op on terminal
	}
	next, err := Transition(r.state, evt)
	if err != nil {
		return r.state, err
	}
	r.state = next
	if evt.Type == EventError || evt.Type == EventNack {
		r.reason = evt.Detail
	}
	return next, nil
}

// FrameToEvent translates a ReceiptFrame into an Event for the local state machine.
func FrameToEvent(frame *proto.ReceiptFrame) *Event {
	switch {
	case frame.BytesReceived != nil:
		return nil // progress hint, no state change
	case frame.Received != nil:
		return &Event{Type: EventProcess}
	case frame.Acked != nil:
		return &Event{Type: EventAck}
	case frame.Nacked != nil:
		return &Event{Type: EventNack, Detail: frame.Nacked.Reason}
	case frame.Failed != nil:
		return &Event{Type: EventError, Detail: fmt.Sprintf("code=%d detail=%s", frame.Failed.Code, frame.Failed.Detail)}
	default:
		return nil
	}
}
