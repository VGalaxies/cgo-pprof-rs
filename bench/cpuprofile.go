package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"runtime/pprof"
	"sync"
	"time"
)

// DefProfileDuration exports for testing.
var DefProfileDuration = time.Second

// globalCPUProfiler is the global CPU profiler.
var globalCPUProfiler = newParallelCPUProfiler()

// ProfileConsumer is a channel that will receive profiling data from the CPU profiler periodically.
// If the channel is full, then the channel won't receive the latest profile data until it is not blocked.
type ProfileConsumer = chan *ProfileData

// ProfileData contains the cpu profile data between the start and end time, usually the interval between start and end is about 1 second.
type ProfileData struct {
	Data  *bytes.Buffer
	Error error
}

// StartCPUProfiler uses to start to run the global parallelCPUProfiler.
func StartCPUProfiler() error {
	return globalCPUProfiler.start()
}

// StopCPUProfiler uses to stop the global parallelCPUProfiler.
func StopCPUProfiler() {
	globalCPUProfiler.stop()
}

// Register register a ProfileConsumer into the global CPU profiler.
// Normally, the registered ProfileConsumer will receive the cpu profile data per second.
// If the ProfileConsumer (channel) is full, the latest cpu profile data will not be sent to it.
// This function is thread-safe.
// WARN: ProfileConsumer should not be closed before unregister.
func Register(ch ProfileConsumer) {
	globalCPUProfiler.register(ch)
}

// Unregister unregister a ProfileConsumer from the global CPU profiler.
// The unregistered ProfileConsumer won't receive the cpu profile data any more.
// This function is thread-safe.
func Unregister(ch ProfileConsumer) {
	globalCPUProfiler.unregister(ch)
}

// parallelCPUProfiler is a cpu profiler.
// With parallelCPUProfiler, it is possible to have multiple profile consumer at the same time.
// WARN: Only one running parallelCPUProfiler is allowed in the process, otherwise some profiler may profiling fail.
type parallelCPUProfiler struct {
	ctx            context.Context
	cs             map[ProfileConsumer]struct{}
	notifyRegister chan struct{}
	profileData    *ProfileData
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	lastDataSize   int
	sync.Mutex
	started bool
}

// newParallelCPUProfiler crate a new parallelCPUProfiler.
func newParallelCPUProfiler() *parallelCPUProfiler {
	return &parallelCPUProfiler{
		cs:             make(map[ProfileConsumer]struct{}),
		notifyRegister: make(chan struct{}),
	}
}

var (
	errProfilerAlreadyStarted = errors.New("parallelCPUProfiler is already started")
)

func (p *parallelCPUProfiler) start() error {
	p.Lock()
	if p.started {
		p.Unlock()
		return errProfilerAlreadyStarted
	}

	p.started = true
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.Unlock()
	p.wg.Add(1)
	go WithRecovery(p.profilingLoop, nil)

	log.Println("parallel cpu profiler started")
	return nil
}

func (p *parallelCPUProfiler) stop() {
	p.Lock()
	if !p.started {
		p.Unlock()
		return
	}
	p.started = false
	if p.cancel != nil {
		p.cancel()
	}
	p.Unlock()

	p.wg.Wait()
	log.Println("parallel cpu profiler stopped")
}

func (p *parallelCPUProfiler) register(ch ProfileConsumer) {
	if ch == nil {
		return
	}
	p.Lock()
	p.cs[ch] = struct{}{}
	p.Unlock()

	select {
	case p.notifyRegister <- struct{}{}:
	default:
	}
}

func (p *parallelCPUProfiler) unregister(ch ProfileConsumer) {
	if ch == nil {
		return
	}
	p.Lock()
	delete(p.cs, ch)
	p.Unlock()
}

func (p *parallelCPUProfiler) profilingLoop() {
	checkTicker := time.NewTicker(DefProfileDuration)
	defer func() {
		checkTicker.Stop()
		pprof.StopCPUProfile()
		p.wg.Done()
	}()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.notifyRegister:
			// If already in profiling, don't do anything.
			if p.profileData != nil {
				continue
			}
		case <-checkTicker.C:
		}
		p.doProfiling()
	}
}

func (p *parallelCPUProfiler) doProfiling() {
	if p.profileData != nil {
		pprof.StopCPUProfile()
		p.lastDataSize = p.profileData.Data.Len()
		p.sendToConsumers()
	}

	if p.consumersCount() == 0 {
		return
	}

	capacity := (p.lastDataSize/4096 + 1) * 4096
	p.profileData = &ProfileData{Data: bytes.NewBuffer(make([]byte, 0, capacity))}
	err := pprof.StartCPUProfile(p.profileData.Data)
	if err != nil {
		p.profileData.Error = err
		// notify error as soon as possible
		p.sendToConsumers()
		return
	}
}

func (p *parallelCPUProfiler) consumersCount() int {
	p.Lock()
	n := len(p.cs)
	p.Unlock()
	return n
}

func (p *parallelCPUProfiler) sendToConsumers() {
	p.Lock()
	defer func() {
		p.Unlock()
		if r := recover(); r != nil {
			log.Println("parallel cpu profiler panic", r)
		}
	}()

	for c := range p.cs {
		select {
		case c <- p.profileData:
		default:
			// ignore
		}
	}
	p.profileData = nil
}
