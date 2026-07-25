package reportstudio

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrAdmissionFull = errors.New("structured inference admission full")

type Admission struct {
	active   chan struct{}
	maxQueue int64
	waiting  atomic.Int64
	observer func(active, waiting int)
}

func NewAdmission(maxActive, maxQueue int) *Admission {
	if maxActive < 1 {
		maxActive = 1
	}
	if maxQueue < 0 {
		maxQueue = 0
	}
	return &Admission{active: make(chan struct{}, maxActive), maxQueue: int64(maxQueue)}
}

func (a *Admission) WithObserver(observer func(active, waiting int)) {
	a.observer = observer
	a.notify()
}

func (a *Admission) Acquire(ctx context.Context) (func(), error) {
	select {
	case a.active <- struct{}{}:
		a.notify()
		return a.release, nil
	default:
	}
	waiting := a.waiting.Add(1)
	a.notify()
	if waiting > a.maxQueue {
		a.waiting.Add(-1)
		a.notify()
		return nil, ErrAdmissionFull
	}
	select {
	case a.active <- struct{}{}:
		a.waiting.Add(-1)
		a.notify()
		return a.release, nil
	case <-ctx.Done():
		a.waiting.Add(-1)
		a.notify()
		return nil, ctx.Err()
	}
}

func (a *Admission) release() {
	<-a.active
	a.notify()
}

func (a *Admission) Active() int {
	return len(a.active)
}

func (a *Admission) Waiting() int {
	return int(a.waiting.Load())
}

func (a *Admission) notify() {
	if a != nil && a.observer != nil {
		a.observer(a.Active(), a.Waiting())
	}
}
