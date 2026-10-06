// Package clock is a monotonic clock fine enough to time a page that
// takes microseconds. Go's own clock on Windows advances in steps of about
// half a millisecond, so there it reads QueryPerformanceCounter, as
// Python, Node and Rust do.
package clock

import "time"

var start = time.Now()

// Now is nanoseconds since an arbitrary start.
func Now() int64 { return now() }

// Since is nanoseconds since t, a value of Now.
func Since(t int64) int64 { return Now() - t }

func portable() int64 { return int64(time.Since(start)) }
