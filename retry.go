package main

import "time"

func retryWithBackoff(attempts int, baseDelay time.Duration, fn func() error) error {
	var lastErr error
	delay := baseDelay
	for i := 0; i < attempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(delay)
		delay *= 2
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
	}
	return lastErr
}
