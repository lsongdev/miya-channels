package app

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type policyWriter struct {
	inner     channels.Writer
	streaming bool
	finalOnly bool
	interval  time.Duration

	mu         sync.Mutex
	pending    strings.Builder
	last       time.Time
	timer      *time.Timer
	generation uint64
}

func newPolicyWriter(inner channels.Writer, cfg config.DeliveryConfig) channels.Writer {
	streaming := true
	if cfg.Streaming != nil {
		streaming = *cfg.Streaming
	}
	interval := time.Duration(cfg.EditIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 800 * time.Millisecond
	}
	return &policyWriter{inner: inner, streaming: streaming, finalOnly: cfg.FinalOnly, interval: interval}
}

func (w *policyWriter) Write(text string, done bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending.WriteString(text)
	if done {
		w.cancelTimerLocked()
		pending := w.takePending()
		return w.inner.Write(pending, true)
	}
	if w.finalOnly || !w.streaming || w.pending.Len() == 0 {
		return nil
	}
	if w.last.IsZero() || time.Since(w.last) >= w.interval {
		w.cancelTimerLocked()
		return w.writePendingLocked()
	}
	w.schedulePendingLocked()
	return nil
}

func (w *policyWriter) schedulePendingLocked() {
	if w.timer != nil {
		return
	}
	delay := time.Until(w.last.Add(w.interval))
	generation := w.generation
	w.timer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if generation != w.generation {
			return
		}
		w.timer = nil
		if w.pending.Len() == 0 {
			return
		}
		if err := w.writePendingLocked(); err != nil {
			log.Printf("[channels] deliver buffered stream update: %v", err)
		}
	})
}

func (w *policyWriter) writePendingLocked() error {
	pending := w.takePending()
	w.last = time.Now()
	return w.inner.Write(pending, false)
}

func (w *policyWriter) cancelTimerLocked() {
	if w.timer == nil {
		return
	}
	w.timer.Stop()
	w.timer = nil
	w.generation++
}

func (w *policyWriter) takePending() string {
	text := w.pending.String()
	w.pending.Reset()
	return text
}
