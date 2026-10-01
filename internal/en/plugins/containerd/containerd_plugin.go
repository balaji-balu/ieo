package containerd

import (
	"context"
	"fmt"
	"os"
	"path"
	"syscall"
	"time"

    "go.uber.org/zap"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"

	"github.com/balaji-balu/ieo/pkg/en/edgeruntime"
	"github.com/balaji-balu/ieo/internal/en/plugins"
    "github.com/balaji-balu/ieo/pkg/logx"
)

// 
func init() {
	plugins.Register(&ContainerdPlugin{})
}

type ContainerdPlugin struct {
	client     *containerd.Client
	socketPath string
	containers map[string]containerd.Container
    log        *zap.SugaredLogger
}

func (c *ContainerdPlugin) Name() string {
	return "containerd"
}

func (c *ContainerdPlugin) Capabilities() []string {
	return []string{"oci", "containerd"}
}

// detectContainerdSocket tries common socket locations (standalone containerd, k3s, etc.)
func detectContainerdSocket() string {
	candidates := []string{
		"/run/containerd/containerd.sock",        // normal
		"/var/run/containerd/containerd.sock",    // alternate
		"/run/k3s/containerd/containerd.sock",    // k3s
		"/var/run/k3s/containerd/containerd.sock",
	}

	for _, s := range candidates {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return ""
}

// ensureClient initializes the containerd client once and stores detected socket
func (c *ContainerdPlugin) ensureClient() error {
	if c.client != nil {
		return nil
	}

	socket := detectContainerdSocket()
	if socket == "" {
		return fmt.Errorf("containerd socket not found (checked common locations)")
	}

	cli, err := containerd.New(socket)
	if err != nil {
		return fmt.Errorf("cannot connect to containerd at %s: %w", socket, err)
	}

	c.client = cli
	c.socketPath = socket
	c.containers = map[string]containerd.Container{}
	return nil
}

/* ====================
        INSTALL
   Pull and unpack OCI image
==================== */
func (c *ContainerdPlugin) Install(spec edgeruntime.ComponentSpec) error {
    c.log = logx.New("en.containerd")
	c.log.Infow("Install: enter")
    
    // spec.Artifact is expected to be an OCI image reference: docker.io/library/nginx:latest etc.
	if err := c.ensureClient(); err != nil {
		return err
	}

	ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

	if spec.Artifact == "" {
		return fmt.Errorf("artifact (image) is empty")
	}

	// Pull and unpack the image into containerd content store
	_, err := c.client.Pull(ctx, spec.Artifact, containerd.WithPullUnpack)
	if err != nil {
		return fmt.Errorf("containerd pull failed for %s: %w", spec.Artifact, err)
	}

	return nil
}

/* ====================
        START
   Create container + task and start it
==================== */
func (c *ContainerdPlugin) Start(spec edgeruntime.ComponentSpec) error {
	c.log.Infow("Start: enter")
	if err := c.ensureClient(); err != nil {
		return err
	}

	ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

	if spec.Artifact == "" {
		return fmt.Errorf("artifact (image) is empty")
	}

	image, err := c.client.GetImage(ctx, spec.Artifact)
	if err != nil {
		return fmt.Errorf("image not found %s: %w", spec.Artifact, err)
	}

	// Snapshot key (unique per container name)
	snapKey := fmt.Sprintf("%s-snap", spec.Name)

	// Create container with new snapshot and spec configured from image
	container, err := c.client.NewContainer(
		ctx,
		spec.Name,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapKey, image),
		containerd.WithNewSpec(
			// populate spec using image config (entrypoint, env, etc.)
			oci.WithImageConfig(image),
			// you may append more config options here (mounts, env, user) using oci.With... funcs
		),
	)
	if err != nil {
		return fmt.Errorf("container create failed: %w", err)
	}

	// Create task (process) for the container using Null IO (no TTY). You can change to cio.NewCreator() to wire logs.
	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		// try to cleanup container on failure
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return fmt.Errorf("task create failed: %w", err)
	}

	// Start the task
	if err := task.Start(ctx); err != nil {
		// cleanup on failure
		_, _ = task.Delete(ctx)
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return fmt.Errorf("task start failed: %w", err)
	}

	// Save container reference for future ops
	c.containers[spec.Name] = container

	c.log.Infow("Start: exit")
	return nil
}

/* ====================
        STOP
   Kill task then delete task (keeps snapshot/container for possible restart)
==================== */
func (c *ContainerdPlugin) Stop(name string) error {
	c.log.Infow("Stop: enter")
	if err := c.ensureClient(); err != nil {
		return err
	}

	ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

	container, ok := c.containers[name]
	if !ok {
		// attempt to load container if not present in map (best-effort)
		var err error
		container, err = c.client.LoadContainer(ctx, name)
		if err != nil {
			// nothing to do
			return nil
		}
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// task not present/running
		delete(c.containers, name)
		return nil
	}

	// Graceful shutdown
	_ = task.Kill(ctx, syscall.SIGTERM)

	// give a short grace period for process to exit, but do not block too long
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Wait for task to be in stopped state or timeout
	_, waitErr := task.Wait(waitCtx)
	if waitErr != nil {
		// timed out or other error; try SIGKILL
		_ = task.Kill(ctx, syscall.SIGKILL)
	}

	// Delete the task (best-effort)
	s, _ := task.Delete(ctx)
	c.log.Debugw("s", "", s)

	// Note: we keep the container and snapshot; Delete(name) will perform full cleanup if desired
	c.log.Infow("Stop: exit")
	delete(c.containers, name)
	return nil
}

/* ====================
        DELETE
   Full cleanup including container and snapshot
==================== */
/*func (c *ContainerdPlugin) Delete(name string) error {
	c.log.Infow("Delete: enter")
	if err := c.ensureClient(); err != nil {
		return err
	}

	ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

	// Attempt to stop first (best-effort)
	_ = c.Stop(name)

	// Load container and delete (including snapshot cleanup)
	container, err := c.client.LoadContainer(ctx, name)
	if err != nil {
		// Already gone
		return nil
	}

	if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("failed to delete container %s: %w", name, err)
	}

	delete(c.containers, name)
	c.log.Infow("Delete: exit")

	return nil
}
*/
func (c *ContainerdPlugin) Delete(name string) error {
    c.log.Infow("Delete: enter", "name", name)

    if err := c.ensureClient(); err != nil {
        return err
    }

    ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

    // 1. Load container (if missing, nothing to do)
    container, err := c.client.LoadContainer(ctx, name)
    if err != nil {
        c.log.Infow("Delete: container not found; treating as deleted", "name", name)
        return nil
    }

    // 2. Load task (if exists)
    task, err := container.Task(ctx, nil)
    if err == nil {
        // 3. Kill task hard
        if killErr := task.Kill(ctx, syscall.SIGKILL); killErr != nil {
            c.log.Warnw("Delete: kill failed", "error", killErr)
        }

        // 4. Wait for exit
        statusC, waitErr := task.Wait(ctx)
        if waitErr == nil {
            <-statusC
        }

        // 5. Delete task
        if _, delErr := task.Delete(ctx); delErr != nil {
            c.log.Warnw("Delete: task delete failed", "error", delErr)
        }
    }

    // 6. Now delete container + snapshot cleanup
    if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
        return fmt.Errorf("failed to delete container %s: %w", name, err)
    }

    delete(c.containers, name)

    c.log.Infow("Delete: exit", "name", name)
    return nil
}


/* ====================
        STATUS
==================== */
func (c *ContainerdPlugin) Status(name string) (edgeruntime.ComponentStatus, error) {
	c.log.Infow("Status: Enter")
	if err := c.ensureClient(); err != nil {
		return edgeruntime.ComponentStatus{}, err
	}

	ctx := namespaces.WithNamespace(context.Background(), "ieo-en")

	// Try to load container
	container, err := c.client.LoadContainer(ctx, name)
	if err != nil {
		return edgeruntime.ComponentStatus{
			Name:      name,
			State:     "NotFound",
			Message:   fmt.Sprintf("container %s not found", name),
			Timestamp: time.Now().Unix(),
		}, nil
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// no task → stopped
		return edgeruntime.ComponentStatus{
			Name:      name,
			State:     "Stopped",
			Message:   "task not running",
			Timestamp: time.Now().Unix(),
		}, nil
	}

	ti, err := task.Status(ctx)
	if err != nil {
		return edgeruntime.ComponentStatus{
			Name:      name,
			State:     "Unknown",
			Message:   err.Error(),
			Timestamp: time.Now().Unix(),
		}, nil
	}

	// Map ProcessStatus to string
	var state string
	switch ti.Status {
	case containerd.Created:
		state = "Created"
	case containerd.Running:
		state = "Running"
	case containerd.Stopped:
		state = "Stopped"
	case containerd.Paused:
		state = "Paused"
	case containerd.Unknown:
		state = "Unknown"
	default:
		state = "Unknown"
	}

	c.log.Infow("Status: exit")
	return edgeruntime.ComponentStatus{
		Name:      name,
		State:     state,
		Message:   fmt.Sprintf("containerd (%s) at %s", path.Base(c.socketPath), c.socketPath),
		Timestamp: time.Now().Unix(),
	}, nil
}
