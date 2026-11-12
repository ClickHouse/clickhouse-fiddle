package dockerengine

import (
	"sync"
	"time"
)

type containerStatus string

const (
	statusUnknown containerStatus = ""
	statusRunning containerStatus = "RUNNING"
	statusPaused  containerStatus = "PAUSED"
	statusFetched containerStatus = "FETCHED"
)

type containerState struct {
	lock sync.Mutex

	id       string
	imageFQN string

	createdAt time.Time
	status    containerStatus

	// systemLogsPrepared is set once the system.*_log tables have been created
	// in the container. Guarded by lock.
	systemLogsPrepared bool
}

func (c *containerState) acquireLock() (release func()) {
	c.lock.Lock()

	return func() {
		c.lock.Unlock()
	}
}

func (c *containerState) setStatus(s containerStatus) {
	c.status = s
}
