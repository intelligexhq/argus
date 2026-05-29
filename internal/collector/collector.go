// Package collector defines the Source interface and the built-in collectors.
//
// Collection is autonomous: the engine drives collectors on a timer (or via
// streamed OS events for privileged sources). Collectors never serve data
// themselves — they only produce observations that the engine correlates and
// persists. The API package is the only thing that exposes data outward.
package collector

import (
	"context"

	"github.com/intelligexhq/argus/internal/model"
)

// Result is the set of observations a single Collect call produced. A collector
// fills only the slices it is responsible for.
type Result struct {
	Processes   []model.Process
	Connections []model.Connection
}

// Collector is one discovery signal source (process table, sockets, artifacts…).
type Collector interface {
	Name() string
	Collect(ctx context.Context) (Result, error)
}
