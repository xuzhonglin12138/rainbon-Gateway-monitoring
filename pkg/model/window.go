package model

import (
	"fmt"
	"time"
)

const BucketSize = 5 * time.Second

type Window string

const (
	Window5m  Window = "5m"
	Window10m Window = "10m"
	Window30m Window = "30m"
)

func ParseWindow(input string) (Window, error) {
	switch input {
	case "", string(Window5m):
		return Window5m, nil
	case string(Window10m):
		return Window10m, nil
	case string(Window30m):
		return Window30m, nil
	default:
		return "", fmt.Errorf("unsupported window %q: allowed values are 5m, 10m, 30m", input)
	}
}

func HotWindows() []Window {
	return []Window{Window5m, Window10m, Window30m}
}

func (w Window) Duration() time.Duration {
	switch w {
	case Window10m:
		return 10 * time.Minute
	case Window30m:
		return 30 * time.Minute
	default:
		return 5 * time.Minute
	}
}

func (w Window) BucketCount() int {
	return int(w.Duration() / BucketSize)
}

func AlignBucket(t time.Time) int64 {
	sec := t.Unix()
	size := int64(BucketSize / time.Second)
	return sec - sec%size
}
