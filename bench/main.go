package main

//#cgo LDFLAGS: -L${SRCDIR}/../../target/release -lgoodboy
/*
#include <stdint.h>
extern void init_tokio_runtime();
extern int get_sigprof_handler(uint64_t* ptr);
*/
import "C"
import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

// stressGC continuously allocates memory in a loop to pressure the GC.
func stressGC() {
	for i := 0; ; i++ {
		// Allocate 10MB slice each iteration
		b := make([]byte, 10<<20)
		// Prevent compiler optimization
		if len(b) == 0 {
			fmt.Println("never")
		}
		// Small pause so GC has a chance to run between allocations
		time.Sleep(50 * time.Millisecond)
	}
}

func main() {
	// Lower the GC trigger threshold (percentage of live heap growth)
	// Default is 100; 10 means GC will run when heap grows 10% beyond last GC
	debug.SetGCPercent(10)

	// Start the allocator goroutine
	go stressGC()

	// goroutine stack signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	go func() {
		for {
			<-sigs
			buf := make([]byte, 1<<20)
			stacklen := runtime.Stack(buf, true)
			os.Stdout.Write(buf[:stacklen])
			os.Exit(-1)
		}
	}()

	// trace.Start(os.Stderr)
	// defer trace.Stop()

	C.init_tokio_runtime()
	for {
		time.Sleep(1 * time.Second)
		printSigprofHandler("pprof-rs")
	}
}

func printSigprofHandler(prefix string) {
	var ptr C.uint64_t
	if C.get_sigprof_handler(&ptr) != 0 {
		fmt.Printf("%s: get_sigprof_handler failed\n", prefix)
	} else {
		fmt.Printf("%s SIGPROF handler = 0x%x\n", prefix, ptr)
	}
}
