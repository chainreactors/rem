package utils

import "time"

// Sleep is a replaceable sleep function. In WASM builds, this is overridden
// with a host-function-based implementation since TinyGo's time.Sleep panics.
var Sleep = time.Sleep

// After is a replaceable timer function. In WASM builds, this is overridden
// with a goroutine+Sleep-based implementation since TinyGo's time.After panics.
var After = time.After
