package source

import "context"

// BackgroundLimit is shared by library scans, follow refreshes and speed tests.
// A channel queues blocked senders rather than allowing a new scan its own pool.
const BackgroundLimit = 4

var backgroundSlots = make(chan struct{}, BackgroundLimit)

// AcquireBackground must only be called from work goroutines. The returned
// release belongs to one finite site operation, never to a cached UI session.
func AcquireBackground(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case backgroundSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-backgroundSlots
			return nil, err
		}
		return func() { <-backgroundSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
