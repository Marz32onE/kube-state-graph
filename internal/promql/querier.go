package promql

import (
	"context"
	"time"

	"github.com/prometheus/common/model"
)

// Querier is the minimal contract Server and Builder depend on. Production
// wiring uses *Client; tests inject a mockery-generated mock so unit tests do
// not need an httptest.NewServer fronting hand-rolled JSON fixtures.
//
// Defined on the consumer side per Go convention ("accept interfaces, return
// structs") would mean api + build each redeclare a near-identical interface;
// keeping it here avoids that duplication while *Client trivially satisfies it.
type Querier interface {
	Instant(ctx context.Context, name, query string, ts time.Time) (model.Vector, error)
}

// Compile-time assertion that *Client satisfies Querier.
var _ Querier = (*Client)(nil)
