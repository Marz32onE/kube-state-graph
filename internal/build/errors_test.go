package build

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewError_FieldsRoundTrip(t *testing.T) {
	t.Parallel()
	cause := errors.New("dns")
	e := NewError(ReasonUpstream, "msg", cause)
	var be *Error
	if !errors.As(e, &be) {
		t.Fatalf("errors.As did not match *Error")
	}
	if be.Reason != ReasonUpstream {
		t.Errorf("reason=%q", be.Reason)
	}
	if be.Message != "msg" {
		t.Errorf("message=%q", be.Message)
	}
	if !errors.Is(e, cause) {
		t.Errorf("errors.Is did not unwrap to cause")
	}
}

func TestError_StringFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{"with cause", &Error{Reason: ReasonTimeout, Message: "ignored", Err: errors.New("ctx")}, "timeout: ctx"},
		{"without cause", &Error{Reason: ReasonOutsideRetention, Message: "no rows"}, "outside_retention: no rows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestAsReason_ExtractsThroughWrap(t *testing.T) {
	t.Parallel()
	inner := NewError(ReasonTimeout, "deadline", nil)
	wrapped := fmt.Errorf("handler: %w", inner)
	if got := AsReason(wrapped); got != ReasonTimeout {
		t.Errorf("AsReason wrapped=%q want timeout", got)
	}
}

func TestAsReason_NonBuildError(t *testing.T) {
	t.Parallel()
	if got := AsReason(errors.New("plain")); got != "" {
		t.Errorf("AsReason plain=%q want empty", got)
	}
	if got := AsReason(nil); got != "" {
		t.Errorf("AsReason nil=%q want empty", got)
	}
}

func TestError_Unwrap_NilCause(t *testing.T) {
	t.Parallel()
	e := &Error{Reason: ReasonOutsideRetention, Message: "x"}
	if e.Unwrap() != nil {
		t.Errorf("Unwrap nil cause should return nil")
	}
}
