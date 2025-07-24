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
	"log"
	"net/http"
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
	StartCPUProfiler()
	defer StopCPUProfiler()

	// --- HTTP Server for Profiling ---
	// Create a new ServeMux to handle HTTP requests.
	serverMux := http.NewServeMux()
	// Register the CPU profile handler at the specified endpoint.
	// This endpoint will provide a pprof-formatted CPU profile.
	serverMux.HandleFunc("/debug/pprof/profile", ProfileHTTPHandler)

	// Start the HTTP server in a new goroutine so it doesn't block the main thread.
	go func() {
		// Listen on port 6060, a common port for pprof.
		fmt.Println("✅ Starting pprof server on http://localhost:6060/debug/pprof/profile")
		if err := http.ListenAndServe(":6060", serverMux); err != nil {
			log.Fatalf("pprof server failed to start: %v", err)
		}
	}()
	// --- End of HTTP Server Setup ---

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
