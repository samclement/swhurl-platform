package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// livePoll is how often a live test's wait reads again.
const livePoll = 2 * time.Second

// liveCluster is how a live test reads the cluster: one object at a time, a failed read never taken for absence, and
// each wait polled on the clock the caller supplies.
type liveCluster struct {
	ctx    context.Context
	reader kube.Reader
	now    func() time.Time
	sleep  func(time.Duration)
}

// read is one object's document, nil when the server says it is absent, or an error naming why it could not be read.
func (c liveCluster) read(ref observe.Ref) ([]byte, error) {
	return readObject(c.ctx, c.reader, ref, ref.Kind+" "+ref.Name)
}

// present is whether the server says the object exists.
func (c liveCluster) present(ref observe.Ref) (bool, error) {
	document, err := c.read(ref)
	return document != nil, err
}

// wait polls met until it holds or limit passes. A read that fails is retried, and named when the wait times out;
// what is the failure's words.
func (c liveCluster) wait(limit time.Duration, what string, met func() (bool, error)) error {
	started := c.now()
	last := ""
	for {
		done, err := met()
		if err == nil && done {
			return nil
		}
		last = ""
		if err != nil {
			last = err.Error()
		}
		if c.ctx.Err() != nil {
			return fmt.Errorf("%s: wait stopped: %w", what, c.ctx.Err())
		}
		if c.now().Sub(started) >= limit {
			if last != "" {
				return fmt.Errorf("%s after %s (last read: %s)", what, limit, last)
			}
			return fmt.Errorf("%s after %s", what, limit)
		}
		c.sleep(livePoll)
	}
}

// gone is whether the server says the object is absent.
func (c liveCluster) gone(ref observe.Ref) func() (bool, error) {
	return func() (bool, error) {
		present, err := c.present(ref)
		return !present, err
	}
}
