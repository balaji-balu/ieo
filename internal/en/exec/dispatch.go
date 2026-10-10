package exec

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

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

	mu sync.Mutex
	// queues has an entry for each deployment with a command in flight. Its goroutine owns the
	// entry and deletes it when it finds nothing waiting.
	queues  map[uuid.UUID]*queue
	running sync.WaitGroup
}

// queue is the one command waiting behind a deployment's command in flight; nil when none is.
type queue struct {
	waiting *contract.Command
}

// NewDispatcher returns a Dispatcher that runs commands on e until ctx ends. log carries the EN's
// site_id and host_id (SPEC §13.1).
func NewDispatcher(ctx context.Context, e *Executor, log *slog.Logger) *Dispatcher {
	return &Dispatcher{ctx: ctx, exec: e, log: log, queues: map[uuid.UUID]*queue{}}
}

// Handle takes one message from the EN's command subject and returns the ack to reply with.
//
// A valid command is queued and acked `accepted: true`; its outcome arrives as status events. An
// Apply whose deployment is not valid is acked `accepted: false` with IEO-INVALID-COMMAND and
// changes nothing (SPEC §8.9 step 2). reply is false when nothing is to be sent: the message is
// not a Command, which is logged and dropped (SPEC §11.2), or the Dispatcher has stopped.
func (d *Dispatcher) Handle(data []byte) (ack contract.CommandAck, reply bool) {
	cmd, err := contract.DecodeCommand(data)
	if errors.Is(err, contract.ErrInvalidDeployment) {
		d.log.Warn("command rejected", "command_id", cmd.CommandID.String(),
			"deployment_id", cmd.DeploymentID.String(), "digest", cmd.Digest.String(), "reason", err.Error())
		return contract.CommandAck{CommandID: cmd.CommandID, Error: &contract.AckError{
			Code: contract.CodeInvalidCommand, Message: truncate(err.Error(), maxMessageBytes),
		}}, true
	}
	if err != nil {
		d.log.Warn("command dropped", "reason", err.Error())
		return contract.CommandAck{}, false
	}
	if !d.enqueue(cmd) {
		return contract.CommandAck{}, false
	}
	return contract.CommandAck{CommandID: cmd.CommandID, Accepted: true}, true
}

// enqueue starts cmd, or leaves it waiting behind its deployment's command in flight, in place of
// any command already waiting there. It reports false, and does neither, once ctx has ended.
func (d *Dispatcher) enqueue(cmd contract.Command) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx.Err() != nil {
		return false
	}
	if q, busy := d.queues[cmd.DeploymentID]; busy {
		q.waiting = &cmd // SPEC §8.9: if several arrive, only the latest runs
		return true
	}
	q := &queue{}
	d.queues[cmd.DeploymentID] = q
	d.running.Add(1)
	go d.run(q, cmd)
	return true
}

// run runs cmd and then whatever is waiting for the same deployment, until nothing is.
func (d *Dispatcher) run(q *queue, cmd contract.Command) {
	defer d.running.Done()
	superseded := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return q.waiting != nil
	}
	for {
		var err error
		if cmd.Action == contract.ActionApply {
			err = d.exec.Apply(d.ctx, cmd, superseded)
		} else {
			err = d.exec.Remove(d.ctx, cmd)
		}
		if err != nil {
			d.log.Error("command ended early", "command_id", cmd.CommandID.String(),
				"deployment_id", cmd.DeploymentID.String(), "digest", cmd.Digest.String(), "reason", err.Error())
		}
		d.mu.Lock()
		if q.waiting == nil || d.ctx.Err() != nil {
			delete(d.queues, cmd.DeploymentID)
			d.mu.Unlock()
			return
		}
		cmd, q.waiting = *q.waiting, nil
		d.mu.Unlock()
	}
}

// Wait returns once no command is running or waiting. After ctx ends, that is once the commands
// in flight have stopped. Call it when no Handle call is in progress or still to come: at
// shutdown, after the command subscription is closed.
func (d *Dispatcher) Wait() {
	d.running.Wait()
}
