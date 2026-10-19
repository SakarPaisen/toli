package taskclient

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"golang.org/x/sync/errgroup"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// Poll tasks for updates/cancel expired tasks
func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.opts.PollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			tasks, err := c.store.List(ctx, taskstore.Filter{States: activeStates})
			if err != nil {
				slog.Error("poll: list active tasks", "err", err)
				ticker.Reset(c.opts.PollEvery)
				continue
			}

			var g errgroup.Group
			g.SetLimit(8)
			for _, task := range tasks {
				g.Go(func() error {
					if err := c.pollTask(ctx, task); err != nil {
						slog.Warn("poll: task", "task", task.A2A.ID, "err", err)
					}
					return nil
				})
			}
			g.Wait()

			ticker.Reset(c.opts.PollEvery)
		}
	}
}

func (c *Client) pollTask(ctx context.Context, task *taskstore.Task) error {
	if time.Now().After(task.Deadline) {
		canceled, err := c.Cancel(ctx, task.A2A.ID)
		if err == nil && canceled.Version != task.Version && c.opts.OnChange != nil {
			c.opts.OnChange(task, canceled)
		}
		if errors.Is(err, a2a.ErrTaskNotCancelable) {
			// It finished on the peer before our cancel arrived. Past its deadline
			// it won't be polled again, so fetch how it ended now.
			_, err = c.checkAndUpdateStatus(ctx, task)
		}
		return err
	}
	_, err := c.checkAndUpdateStatus(ctx, task)
	return err
}
