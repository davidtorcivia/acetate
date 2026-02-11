package analytics

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	MaxMetaKeys   = 32
	MaxMetaDepth  = 4
	MaxStringSize = 512
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
	db       *sql.DB
	events   chan Event
	flushReq chan chan struct{}
	done     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
	dropped  atomic.Int64
	rejected atomic.Int64
}

// NewCollector creates an analytics collector with a buffered channel and flush goroutine.
func NewCollector(db *sql.DB) *Collector {
	c := &Collector{
		db:       db,
		events:   make(chan Event, ChannelBuffer),
		flushReq: make(chan chan struct{}),
		done:     make(chan struct{}),
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

// RejectedCount returns the number of events rejected by ingestion validation.
func (c *Collector) RejectedCount() int64 {
	return c.rejected.Load()
}

// FlushNow forces a synchronous flush of currently buffered events.
func (c *Collector) FlushNow(ctx context.Context) error {
	ack := make(chan struct{})
	select {
	case c.flushReq <- ack:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("collector closed")
	}

	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("collector closed")
	}
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
		if r := c.rejected.Load(); r > 0 {
			log.Printf("analytics: %d events rejected by validation", r)
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

		case ack := <-c.flushReq:
			if len(batch) > 0 {
				c.flush(batch)
				batch = batch[:0]
			}
			close(ack)

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
	if !validSessionID(sessionID) {
		return errors.New("invalid session")
	}

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

	var rejected int64
	for _, e := range events {
		normalized, ok := normalizeBatchEvent(e)
		if !ok {
			rejected++
			continue
		}
		normalized.SessionID = sessionID
		c.Record(normalized)
	}

	if rejected > 0 {
		c.rejected.Add(rejected)
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

func normalizeBatchEvent(raw struct {
	EventType       string          `json:"event_type"`
	TrackStem       string          `json:"track_stem,omitempty"`
	PositionSeconds float64         `json:"position_seconds,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}) (Event, bool) {
	eventType := strings.TrimSpace(raw.EventType)
	if !validEventTypes[eventType] {
		return Event{}, false
	}

	trackStem := strings.TrimSpace(raw.TrackStem)
	if trackStem != "" && !validTrackStem(trackStem) {
		return Event{}, false
	}

	if raw.PositionSeconds < 0 || raw.PositionSeconds > 24*60*60 {
		return Event{}, false
	}

	meta, metaObj, ok := sanitizeMetadata(raw.Metadata)
	if !ok {
		return Event{}, false
	}

	if requiresTrackStem(eventType) && trackStem == "" {
		return Event{}, false
	}

	if !validEventByType(eventType, trackStem, raw.PositionSeconds, metaObj) {
		return Event{}, false
	}

	if eventType == "session_start" || eventType == "session_end" {
		trackStem = ""
	}

	return Event{
		EventType:       eventType,
		TrackStem:       trackStem,
		PositionSeconds: raw.PositionSeconds,
		Metadata:        meta,
	}, true
}

func requiresTrackStem(eventType string) bool {
	switch eventType {
	case "play", "pause", "seek", "complete", "dropout":
		return true
	default:
		return false
	}
}

func validEventByType(eventType, trackStem string, position float64, metadata map[string]interface{}) bool {
	switch eventType {
	case "seek":
		if !hasNumericField(metadata, "from_position") || !hasNumericField(metadata, "to_position") {
			return false
		}
	case "pause", "dropout":
		if position <= 0 {
			return false
		}
	case "session_start", "session_end":
		if trackStem != "" || position != 0 {
			return false
		}
	}
	return true
}

func sanitizeMetadata(raw json.RawMessage) (string, map[string]interface{}, bool) {
	if len(raw) == 0 {
		return "{}", map[string]interface{}{}, true
	}
	if len(raw) > MaxMetaBytes {
		return "", nil, false
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var parsed interface{}
	if err := dec.Decode(&parsed); err != nil {
		return "", nil, false
	}
	var extra interface{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", nil, false
	}

	obj, ok := parsed.(map[string]interface{})
	if !ok {
		return "", nil, false
	}
	if len(obj) > MaxMetaKeys {
		return "", nil, false
	}
	if !validMetadataValue(obj, 0) {
		return "", nil, false
	}

	normalized, err := json.Marshal(obj)
	if err != nil {
		return "", nil, false
	}
	if len(normalized) > MaxMetaBytes {
		return "", nil, false
	}

	return string(normalized), obj, true
}

func validMetadataValue(v interface{}, depth int) bool {
	if depth > MaxMetaDepth {
		return false
	}

	switch val := v.(type) {
	case map[string]interface{}:
		if len(val) > MaxMetaKeys {
			return false
		}
		for k, child := range val {
			if strings.TrimSpace(k) == "" || len(k) > 64 {
				return false
			}
			if !validMetadataValue(child, depth+1) {
				return false
			}
		}
		return true
	case []interface{}:
		if len(val) > MaxMetaKeys {
			return false
		}
		for _, child := range val {
			if !validMetadataValue(child, depth+1) {
				return false
			}
		}
		return true
	case string:
		return len(val) <= MaxStringSize
	case json.Number, bool, nil, float64:
		return true
	default:
		return false
	}
}

func hasNumericField(obj map[string]interface{}, key string) bool {
	v, ok := obj[key]
	if !ok {
		return false
	}
	switch v.(type) {
	case json.Number, float64:
		return true
	default:
		return false
	}
}

func validSessionID(sessionID string) bool {
	if len(sessionID) != 64 {
		return false
	}
	for _, r := range sessionID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
