// Package quiet silences client-go's klog output for the whole process.
package quiet

import (
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

var once sync.Once

// Klog sets klog's logger to discard, once per process. Call it before any
// client-go goroutine starts: klog reads the logger without a lock.
func Klog() { once.Do(func() { klog.SetLogger(logr.Discard()) }) }
