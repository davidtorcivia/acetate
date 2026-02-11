package analytics

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	ChannelBuffer = 1000
	FlushSize     = 50
	FlushInterval = 5 * time.Second
	DrainTimeout  = 10 * time.Second
	MaxBatchSize  = 500
	MaxMetaBytes  = 4096
)

// Event represents an analytics event from the client.
type Event struct {
	SessionID       string  `json:"session_id"`
	EventType       string  `json:"event_type"`
	TrackStem       string  `json:"track_stem,omitempty"`
	PositionSeconds float64 `json:"position_seconds,omitempty"`
	Metadata        string  `json:"metadata,omitempty"`
}

// highValueEvents are worth brief backpressure when the channel is full.
var highValueEvents = map[string]bool{
	"play":          true,
	"complete":      true,
	"session_start": true,
	"session_end":   true,
}

var validEventTypes = map[string]bool{
	"play":          true,
	"pause":         true,
	"seek":          true,
	"complete":      true,
	"dropout":       true,
	"heartbeat":     true,
	"session_start": true,
	"session_end":   true,
}

// Collector manages buffered analytics event ingestion.
type Collector struct {
	db      *sql.DB
	events  chan Event
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
	dropped atomic.Int64
}

// NewCollector creates an analytics collector with a buffered channel and flush goroutine.
func NewCollector(db *sql.DB) *Collector {
	c := &Collector{
		db:     db,
		events: make(chan Event, ChannelBuffer),
		done:   make(chan struct{}),
	}
	c.wg.Add(1)
	go c.flushLoop()
	return c
}

// Record submits an event to the analytics channel.
// High-value events block briefly (100ms); low-value events are dropped immediately if full.
func (c *Collector) Record(e Event) {
	if highValueEvents[e.EventType] {
		select {
		case c.events <- e:
		case <-time.After(100 * time.Millisecond):
			c.dropped.Add(1)
		}
	} else {
		select {
		case c.events <- e:
		default:
			c.dropped.Add(1)
		}
	}
}

// DroppedCount returns the number of events dropped due to backpressure.
func (c *Collector) DroppedCount() int64 {
	return c.dropped.Load()
}

// Close stops the collector and drains remaining events.
func (c *Collector) Close() {
	c.once.Do(func() {
		close(c.done)
		c.wg.Wait()

		// Log dropped events
		if d := c.dropped.Load(); d > 0 {
			log.Printf("analytics: %d events dropped due to backpressure", d)
		}
	})
}

func (c *Collector) flushLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	var batch []Event

	for {
		select {
		case e := <-c.events:
			batch = append(batch, e)
			if len(batch) >= FlushSize {
				c.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				c.flush(batch)
				batch = batch[:0]
			}

		case <-c.done:
			// Drain remaining events with timeout
			drainDone := make(chan struct{})
			go func() {
				defer close(drainDone)
				for {
					select {
					case e := <-c.events:
						batch = append(batch, e)
					default:
						if len(batch) > 0 {
							c.flush(batch)
						}
						return
					}
				}
			}()

			select {
			case <-drainDone:
			case <-time.After(DrainTimeout):
				log.Printf("analytics: drain timeout, %d events may be lost", len(batch))
			}
			return
		}
	}
}

func (c *Collector) flush(batch []Event) {
	tx, err := c.db.Begin()
	if err != nil {
		log.Printf("analytics: begin tx: %v", err)
		return
	}

	stmt, err := tx.Prepare(
		"INSERT INTO events (session_id, event_type, track_stem, position_seconds, metadata) VALUES (?, ?, ?, ?, ?)",
	)
	if err != nil {
		log.Printf("analytics: prepare: %v", err)
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, e := range batch {
		metadata := e.Metadata
		if metadata == "" {
			metadata = "{}"
		}
		_, err := stmt.Exec(e.SessionID, e.EventType, e.TrackStem, e.PositionSeconds, metadata)
		if err != nil {
			log.Printf("analytics: insert event: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("analytics: commit: %v", err)
	}
}

// RecordBatch parses and records a batch of events from JSON.
func (c *Collector) RecordBatch(sessionID string, data []byte) error {
	var events []struct {
		EventType       string          `json:"event_type"`
		TrackStem       string          `json:"track_stem,omitempty"`
		PositionSeconds float64         `json:"position_seconds,omitempty"`
		Metadata        json.RawMessage `json:"metadata,omitempty"`
	}

	if err := json.Unmarshal(data, &events); err != nil {
		return err
	}
	if len(events) > MaxBatchSize {
		return errors.New("too many events")
	}

	for _, e := range events {
		if !validEventTypes[e.EventType] {
			continue
		}
		if e.TrackStem != "" && !validTrackStem(e.TrackStem) {
			continue
		}
		if e.PositionSeconds < 0 || e.PositionSeconds > 24*60*60 {
			continue
		}
		if len(e.Metadata) > MaxMetaBytes {
			continue
		}

		meta := ""
		if len(e.Metadata) > 0 {
			meta = string(e.Metadata)
		}
		c.Record(Event{
			SessionID:       sessionID,
			EventType:       e.EventType,
			TrackStem:       e.TrackStem,
			PositionSeconds: e.PositionSeconds,
			Metadata:        meta,
		})
	}

	return nil
}

func validTrackStem(stem string) bool {
	stem = strings.TrimSpace(stem)
	if stem == "" || len(stem) > 255 {
		return false
	}
	if strings.ContainsAny(stem, `/\.`) {
		return false
	}
	if stem == "." || stem == ".." {
		return false
	}

	for _, r := range stem {
		if unicode.IsControl(r) || r == 0 {
			return false
		}
	}

	return true
}
