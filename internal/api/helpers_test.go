package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/marz32one/kube-state-graph/internal/auth"
	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/clock"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	promqlmocks "github.com/marz32one/kube-state-graph/internal/promql/mocks"
)

// fixtureSet maps a query-substring needle to the model.Vector returned when
// the upstream Querier is asked a query containing that substring. Mirrors
// the substring-match semantics of the previous httptest-based promMock but
// without spinning a TCP listener.
type fixtureSet map[string]model.Vector

// newMockQuerier returns a Querier mock that responds to Instant by matching
// the query string against fixtures. No match → empty Vector. Any number of
// calls is allowed (Maybe()) so tests focusing on routing/auth do not fail
// when a handler happens not to query upstream.
func newMockQuerier(t *testing.T, fixtures fixtureSet) *promqlmocks.MockQuerier {
	t.Helper()
	q := promqlmocks.NewMockQuerier(t)
	q.EXPECT().Instant(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, query string, _ time.Time) (model.Vector, error) {
			for needle, vec := range fixtures {
				if strings.Contains(query, needle) {
					return vec, nil
				}
			}
			return model.Vector{}, nil
		}).
		Maybe()
	return q
}

// newErrQuerier returns a Querier mock whose every Instant call fails. Used
// for the upstream-failure / 502 path.
func newErrQuerier(t *testing.T, err error) *promqlmocks.MockQuerier {
	t.Helper()
	q := promqlmocks.NewMockQuerier(t)
	q.EXPECT().Instant(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, err).
		Maybe()
	return q
}

// newServerWithMocks constructs a Server backed by the supplied querier mock
// and an empty KeySet (auth disabled). override may tweak the Config before
// the server is built.
func newServerWithMocks(t *testing.T, q *promqlmocks.MockQuerier, override func(*config.Config)) *Server {
	t.Helper()
	return newServerWithMocksAndKeys(t, q, auth.NewKeySet(), override)
}

// newServerWithMocksAndKeys is like newServerWithMocks but lets the caller
// supply a populated *auth.KeySet so the API-key middleware can be exercised
// end-to-end. *auth.KeySet is a pure in-memory construct — using the real
// type here exercises the production validator instead of its mock.
func newServerWithMocksAndKeys(t *testing.T, q *promqlmocks.MockQuerier, ks *auth.KeySet, override func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.PromURL = "http://unused" // satisfies cfg.Validate; the mock bypasses HTTP
	if override != nil {
		override(&cfg)
	}
	require.NoError(t, cfg.Validate())

	logger := observability.NewLogger("error")
	metrics := observability.NewMetrics()
	builder := build.New(q, cfg, metrics, clock.System{})
	return New(cfg, builder, q, metrics, logger, ks, clock.System{})
}

// vec is a small helper that builds a model.Vector from per-series label
// maps. Callers express expected upstream rows declaratively without writing
// JSON or worrying about timestamps (which the handlers never read).
func vec(samples ...map[string]string) model.Vector {
	out := make(model.Vector, 0, len(samples))
	for _, s := range samples {
		metric := make(model.Metric, len(s))
		for k, v := range s {
			metric[model.LabelName(k)] = model.LabelValue(v)
		}
		out = append(out, &model.Sample{Metric: metric, Value: 1, Timestamp: 0})
	}
	return out
}
