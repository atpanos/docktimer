package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
	composeOneOffLabel  = "com.docker.compose.oneoff"
)

// engine implements API on top of the official Docker client.
type engine struct {
	cli *client.Client
}

// NewEngine creates a Docker Engine client. host is used unless DOCKER_HOST is
// set in the environment, which the official client handles via FromEnv.
func NewEngine(host string) (API, error) {
	opts := []client.Opt{client.FromEnv}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &engine{cli: cli}, nil
}

func (e *engine) List(ctx context.Context) ([]Container, error) {
	res, err := e.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, fromSummary(c))
	}
	return out, nil
}

func (e *engine) Start(ctx context.Context, id string) error {
	_, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return mapError(err)
}

func (e *engine) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	_, err := e.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return mapError(err)
}

func (e *engine) Restart(ctx context.Context, id string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	_, err := e.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
	return mapError(err)
}

func (e *engine) Ping(ctx context.Context) error {
	_, err := e.cli.Ping(ctx, client.PingOptions{})
	return mapError(err)
}

func fromSummary(c container.Summary) Container {
	name := c.ID
	if len(c.Names) > 0 && c.Names[0] != "" {
		name = c.Names[0]
	}
	return Container{
		ID:      c.ID,
		Name:    name,
		Image:   c.Image,
		State:   string(c.State),
		Status:  c.Status,
		Created: timeFromUnix(c.Created),
		Project: c.Labels[composeProjectLabel],
		Service: c.Labels[composeServiceLabel],
		OneOff:  strings.EqualFold(c.Labels[composeOneOffLabel], "true"),
	}
}

func isConnectionFailed(err error) bool {
	return client.IsErrConnectionFailed(err)
}

func isNotFound(err error) bool { return errdefs.IsNotFound(err) }

func timeFromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

func isConflict(err error) bool {
	return errdefs.IsConflict(err) || errors.Is(err, errdefs.ErrConflict)
}
