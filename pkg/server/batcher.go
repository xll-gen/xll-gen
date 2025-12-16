package server

type AsyncBatcher struct {
	queue chan PendingAsyncResult
}

func NewAsyncBatcher() *AsyncBatcher {
	return &AsyncBatcher{
		queue: make(chan PendingAsyncResult, 1024),
	}
}

func (ab *AsyncBatcher) QueueResult(handle []byte, val interface{}, valType AnyValue, errStr string) {
	ab.queue <- PendingAsyncResult{
		Handle:  handle,
		Val:     val,
		ValType: valType,
		Err:     errStr,
	}
}

// StartWorker starts the background worker that flushes the batch.
// flushFunc is called with a batch of results.
func (ab *AsyncBatcher) StartWorker(flushFunc func([]PendingAsyncResult)) {
	go func() {
		const maxBatchSize = 256
		batch := make([]PendingAsyncResult, 0, maxBatchSize)

		for {
			item, ok := <-ab.queue
			if !ok {
				return
			}
			batch = append(batch, item)

		drain:
			for len(batch) < maxBatchSize {
				select {
				case nextItem, ok := <-ab.queue:
					if !ok {
						flushFunc(batch)
						return
					}
					batch = append(batch, nextItem)
				default:
					break drain
				}
			}

			flushFunc(batch)
			batch = batch[:0]
		}
	}()
}
