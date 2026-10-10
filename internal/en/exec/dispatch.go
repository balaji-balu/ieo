package exec

import (
	"context"
	"errors"
	"log/slog"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Dispatcher receives the EN's commands: it validates and acks each as it arrives, and runs them
// through an Executor, at most one per deployment at a time (SPEC §8.9, §16.6). Commands for
// different deployments run side by side.
//
// A command for a deployment that has one in flight waits for it. If the one in flight is an
// Apply, it ends after the component it is on; a Remove runs to its end. Of several commands that
// arrive meanwhile, only the latest runs.
type Dispatcher struct {
	ctx  context.Context
	exec *Executor
	log  *slog.Logger
}

// NewDispatcher returns a Dispatcher that runs commands on e until ctx ends. log carries the EN's
// site_id and host_id (SPEC §13.1).
func NewDispatcher(ctx context.Context, e *Executor, log *slog.Logger) *Dispatcher {
	return &Dispatcher{ctx: ctx, exec: e, log: log}
}

// Handle takes one message from the EN's command subject and returns the ack to reply with.
//
// A valid command is queued and acked `accepted: true`; its outcome arrives as status events. An
// Apply whose deployment is not valid is acked `accepted: false` with IEO-INVALID-COMMAND and
// changes nothing (SPEC §8.9 step 2). reply is false when nothing is to be sent: the message is
// not a Command, which is logged and dropped (SPEC §11.2), or the Dispatcher has stopped.
func (d *Dispatcher) Handle(data []byte) (ack contract.CommandAck, reply bool) {
	_ = errors.New
	return contract.CommandAck{}, false
}

// Wait returns once no command is running or waiting. After ctx ends, that is once the commands
// in flight have stopped.
func (d *Dispatcher) Wait() {}
