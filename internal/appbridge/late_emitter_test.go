package appbridge

import (
	"sync"
	"sync/atomic"
	"testing"
)

type countEmitter struct{ n atomic.Int64 }

func (c *countEmitter) Emit(string, any) { c.n.Add(1) }

// Run with -race: publishers emit while main installs the app's emitter.
func TestLateEmitterInstallRacesEmit(t *testing.T) {
	var l LateEmitter
	dst := &countEmitter{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 1000; j++ {
				l.Emit("health:summary", j)
			}
		}()
	}
	close(start)
	l.Install(dst)
	wg.Wait()
	before := dst.n.Load()
	l.Emit("after", nil)
	if dst.n.Load() != before+1 {
		t.Fatal("an emit after Install must reach the destination")
	}
}

func TestLateEmitterDropsBeforeInstall(t *testing.T) {
	var l LateEmitter
	l.Emit("early", nil) // no destination: dropped, no panic
	dst := &countEmitter{}
	l.Install(dst)
	if dst.n.Load() != 0 {
		t.Fatal("events before Install are not replayed")
	}
}
