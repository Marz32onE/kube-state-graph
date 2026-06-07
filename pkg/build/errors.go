package build

import "errors"

// Reason classifies a build failure for HTTP status mapping.
type Reason string

const (
	ReasonTimeout          Reason = "timeout"
	ReasonOutsideRetention Reason = "outside_retention"
	ReasonUpstream         Reason = "upstream"
	// ReasonCanceled marks a build aborted by client disconnect
	// (context.Canceled). It is a client action, not a server/upstream fault,
	// so the API layer maps it to a 4xx and avoids 5xx metric / span-error
	// pollution.
	ReasonCanceled Reason = "canceled"
)

// Error wraps an underlying cause with a typed Reason for HTTP mapping.
type Error struct {
	Reason  Reason
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return string(e.Reason) + ": " + e.Err.Error()
	}
	return string(e.Reason) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// AsReason returns the typed Reason of err, or "" if it is not a build.Error.
func AsReason(err error) Reason {
	var be *Error
	if errors.As(err, &be) {
		return be.Reason
	}
	return ""
}

// NewError constructs a build.Error.
func NewError(reason Reason, message string, cause error) error {
	return &Error{Reason: reason, Message: message, Err: cause}
}
