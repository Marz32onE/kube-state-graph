package clock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSystem_NowReturnsRecentTime(t *testing.T) {
	before := time.Now()
	got := System{}.Now()
	after := time.Now()
	assert.False(t, got.Before(before), "System.Now must not return a time before the call")
	assert.False(t, got.After(after), "System.Now must not return a time after the call returned")
}

func TestFake_NowReturnsConfiguredTime(t *testing.T) {
	want := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	c := Fake{T: want}
	assert.Equal(t, want, c.Now())
	assert.Equal(t, want, c.Now(), "Fake.Now must be stable across calls")
}
