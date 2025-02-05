package eventloop

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var once sync.Once
var GlobalEventLoop *EventLoop

type EventLoop struct {
	promiseQueue []*Promise
	size         uint64
}

func Init() {
	once.Do(func() {
		GlobalEventLoop = &EventLoop{promiseQueue: []*Promise{}}
	})
}

func GetGlobalEventLoop() *EventLoop {
	return GlobalEventLoop
}

func (e *EventLoop) Await(currentP *Promise) (interface{}, error) {
	defer currentP.Done()
	currentP.RegisterHandler()
	select {
	case err := <-currentP.errChan:
		return nil, err
	case rev := <-currentP.rev:
		return rev, nil
	}
}

func (e *EventLoop) Async(fn func() (interface{}, error)) *Promise {
	resultChan := make(chan interface{})
	errChan := make(chan error)
	p := e.newPromise(resultChan, errChan)
	go func() {
		recoveryHandler := promiseRecovery(resultChan, errChan)
		defer func() {
			if r := recover(); r != nil {
				switch x := r.(type) {
				case error:
					recoveryHandler(nil, x)
				default:
					recoveryHandler(nil, fmt.Errorf("%v", x))
				}
			}
		}()
		result, err := fn()
		recoveryHandler(result, err)
	}()
	return p
}

func promiseRecovery(resultChan chan interface{}, errChan chan error) func(result interface{}, err error) {
	return func(result interface{}, err error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
		defer cancel()
		if err != nil {
			select {
			case errChan <- err:
			case <-ctx.Done():
			}
			return
		}

		select {
		case resultChan <- result:
		case <-ctx.Done():
		}
	}
}

func (e *EventLoop) Main(fn func()) {
	fn()
	//await all promises
	e.awaitAll()
}

func (e *EventLoop) awaitAll() {
	n := len(e.promiseQueue)
	for i := n - 1; i >= 0; i-- {
		p := e.promiseQueue[i]
		if p.handler {
			<-p.done
		}
		if currentN := int(atomic.LoadUint64(&e.size)); i == 0 && currentN > n {
			// process fresh promise
			e.awaitAll()
		}
	}
}

//Promise

type Promise struct {
	id      uint64
	handler bool
	rev     <-chan interface{}
	errChan chan error
	err     chan struct{}
	done    chan struct{}
}

func (e *EventLoop) newPromise(rev <-chan interface{}, errChan chan error) *Promise {
	currentP := &Promise{id: atomic.AddUint64(&e.size, 1), rev: rev, errChan: errChan, done: make(chan struct{}), err: make(chan struct{})}
	e.promiseQueue = append(e.promiseQueue, currentP)
	return currentP
}

func (p *Promise) Done() {
	close(p.done)
}

func (p *Promise) RegisterHandler() {
	p.handler = true
}

func (p *Promise) Then(fn func(interface{})) *Promise {
	p.RegisterHandler()
	go func() {
		select {
		case <-p.err:
		case val := <-p.rev:
			defer func() {
				if r := recover(); r != nil {
					switch x := r.(type) {
					case error:
						p.errChan <- x
					default:
						p.errChan <- fmt.Errorf("%v", x)
					}
				} else {
					close(p.err)
					p.Done()
				}
			}()
			fn(val)
		}
	}()
	return p
}

func (p *Promise) Catch(fn func(err error)) {
	p.RegisterHandler()
	go func() {
		select {
		case <-p.err:
		case err := <-p.errChan:
			close(p.err)
			fn(err)
			p.Done()
		}
	}()
}

type Future struct {
	completeChan  chan interface{}
	onComFunc     interface{}
	completeEvent []interface{}
	signalCount   int // could be useful
}

func (e *EventLoop) NewFuture() *Future {
	return &Future{completeChan: make(chan interface{})}
}

func (f *Future) GetCompleteEventFromFuture(signalId int) interface{} {
	if signalId < f.signalCount {
		return f.completeEvent[signalId]
	}
	return nil
}

func (f *Future) GetCompleteEventsFromFuture() []interface{} {
	return f.completeEvent
}

func (f *Future) set(value interface{}, future string) {
	switch future {
	case "complete":
		f.completeChan <- value
	default:
	}
}

func (f *Future) RegisterComplete(futureFunc interface{}) {
	f.onComFunc = futureFunc
}

func (f *Future) signal() {
	// maybe this should be a blocking call?
	go func() {
	Loop:
		for {
			select {
			case e := <-f.completeChan:
				f.completeEvent = append(f.completeEvent, e)
				f.signalCount++
				break Loop
			default:
				break Loop
			}
		}
	}()
}

func (f *Future) SignalComplete(value interface{}) {
	if f.onComFunc != nil {
		go func() {
			f.onComFunc.(func(interface{}))(value)
			// should handle error here -- only if user registered a function for a future error event
			f.set(value, "complete")
		}()
		f.signal()
	} else {
		panic("no function registered for future event [SignalComplete]")
	}
}

func (f *Future) SigalCount() int {
	return f.signalCount
}
