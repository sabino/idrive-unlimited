package main

import (
	"crypto/sha256"
	"encoding/json"
	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/session"
	"log"
	"sync"
	"time"
)

// Persist refreshed cookies without overwriting a login from another process.
func startSessionPersistence(store *session.Store, client *evs.Client) func() error {
	var mu sync.Mutex
	fingerprint := func(data *session.Data) [32]byte {
		copy := *data
		copy.SavedAt = time.Time{}
		raw, _ := json.Marshal(copy)
		return sha256.Sum256(raw)
	}
	initial, initialErr := client.SessionData()
	var last [32]byte
	if initialErr == nil {
		last = fingerprint(initial)
	}
	saved, _ := store.Load()
	var revision time.Time
	if saved != nil {
		revision = saved.SavedAt
	}
	save := func() error {
		mu.Lock()
		defer mu.Unlock()
		data, err := client.SessionData()
		if err != nil {
			return err
		}
		sum := fingerprint(data)
		if sum == last {
			return nil
		}
		data.SavedAt = revision
		if err := store.SaveCurrent(data); err != nil {
			return err
		}
		last = sum
		revision = data.SavedAt
		return nil
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := save(); err != nil {
					log.Printf("persist refreshed session: %v", err)
				}
			}
		}
	}()
	return func() error { close(stop); <-done; return save() }
}
