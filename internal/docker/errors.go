// Package docker wraps the Docker Engine API. It only exposes listing and the
// three container lifecycle operations; there is deliberately no way to run
// commands, pull images or touch volumes/networks.
package docker

import "errors"

var (
	// ErrUnavailable means the Docker daemon could not be reached.
	ErrUnavailable = errors.New("docker daemon unavailable")
	// ErrNotFound means the container does not exist.
	ErrNotFound = errors.New("container not found")
	// ErrConflict means the container is in a state that conflicts with the action.
	ErrConflict = errors.New("container state conflict")
	// ErrNotAllowed means the container is outside Docktimer's scope.
	ErrNotAllowed = errors.New("container is not managed by docktimer")
	// ErrTimeout means the Docker call timed out.
	ErrTimeout = errors.New("docker operation timed out")
)
