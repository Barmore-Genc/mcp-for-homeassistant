package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	unsubscribeTimeout      = 10 * time.Second
	defaultSubscriptionBuf  = 256
	defaultCollectMaxEvents = 100
	maxCollectEvents        = 10000
	maxCollectDuration      = 15 * time.Minute
)

// Subscription is a live WebSocket subscription. Events are delivered on
// Events(); when the reader falls behind, events are dropped and counted
// instead of blocking the shared connection.
type Subscription struct {
	w   *wsConn
	id  int64
	typ string

	mu      sync.Mutex
	ch      chan json.RawMessage
	ended   bool
	err     error
	dropped int64

	stop      func() bool
	closeOnce sync.Once
	closeErr  error
}

// Events returns the channel of raw event payloads. It is closed when the
// subscription ends (Close, context cancellation or a dropped connection).
func (s *Subscription) Events() <-chan json.RawMessage { return s.ch }

// Dropped returns the number of events discarded because the buffer was full.
func (s *Subscription) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Err returns why the subscription ended early, or nil.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Subscription) deliver(ev json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	select {
	case s.ch <- ev:
	default:
		s.dropped++
	}
}

func (s *Subscription) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.err = err
	close(s.ch)
}

// Close ends the subscription and unsubscribes on the server. It is safe to
// call more than once.
func (s *Subscription) Close() error {
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		s.w.removeSub(s.id)
		s.finish(nil)
		if s.w.isClosed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), unsubscribeTimeout)
		defer cancel()
		_, err := s.w.call(ctx, "unsubscribe_events", map[string]any{"subscription": s.id})
		if err != nil && !errors.Is(err, errNotSent) && !errors.Is(err, ErrConnectionClosed) {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// subscribe starts a subscription command. The subscription lives until ctx
// is cancelled or Close is called.
func (c *Client) subscribe(ctx context.Context, typ string, payload any, buf int) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	var (
		s   *Subscription
		err error
	)
	for attempt := 0; attempt < 2; attempt++ {
		var ws *wsConn
		ws, err = c.getWS(callCtx)
		if err != nil {
			return nil, err
		}
		s = &Subscription{w: ws, typ: typ, ch: make(chan json.RawMessage, buf)}
		_, err = ws.callWithSub(callCtx, typ, payload, s)
		if !errors.Is(err, errNotSent) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, errNotSent) {
			return nil, fmt.Errorf("homeassistant: %s: %w", typ, ErrConnectionClosed)
		}
		s.w.removeSub(s.id)
		s.finish(err)
		var haErr *Error
		if s.id != 0 && !errors.As(err, &haErr) && !s.w.isClosed() {
			// The command may have reached HA even though we stopped waiting.
			go func(w *wsConn, id int64) {
				ctx, cancel := context.WithTimeout(context.Background(), unsubscribeTimeout)
				defer cancel()
				_, _ = w.call(ctx, "unsubscribe_events", map[string]any{"subscription": id})
			}(s.w, s.id)
		}
		return nil, err
	}
	s.stop = context.AfterFunc(ctx, func() { _ = s.Close() })
	return s, nil
}

// Context is the HA context attached to states and events.
type Context struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	UserID   *string `json:"user_id"`
}

// Event is a Home Assistant bus event.
type Event struct {
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
	Origin    string          `json:"origin"`
	TimeFired time.Time       `json:"time_fired"`
	Context   Context         `json:"context"`
}

// EventSubscription wraps a Subscription and decodes bus events.
type EventSubscription struct {
	*Subscription
}

// Next waits for the next event. ok is false once the subscription has ended.
func (s *EventSubscription) Next(ctx context.Context) (ev Event, ok bool, err error) {
	select {
	case raw, open := <-s.ch:
		if !open {
			return Event{}, false, s.Err()
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Event{}, true, fmt.Errorf("homeassistant: decode event: %w", err)
		}
		return ev, true, nil
	case <-ctx.Done():
		return Event{}, false, ctx.Err()
	}
}

// SubscribeEvents subscribes to bus events of eventType ("" for all events;
// non-admin users may only subscribe to an allowlist). The subscription ends
// when ctx is cancelled or Close is called.
func (c *Client) SubscribeEvents(ctx context.Context, eventType string) (*EventSubscription, error) {
	payload := map[string]any{}
	if eventType != "" {
		if err := validateEventType(eventType); err != nil {
			return nil, err
		}
		payload["event_type"] = eventType
	}
	s, err := c.subscribe(ctx, "subscribe_events", payload, defaultSubscriptionBuf)
	if err != nil {
		return nil, err
	}
	return &EventSubscription{s}, nil
}

// CollectEventsOptions bounds CollectEvents.
type CollectEventsOptions struct {
	// EventType filters events; empty means all events.
	EventType string
	// Duration is how long to listen. Required, at most 15 minutes.
	Duration time.Duration
	// MaxEvents stops collection early. Default 100, at most 10000.
	MaxEvents int
}

// CollectedEvents is the result of CollectEvents.
type CollectedEvents struct {
	Events []Event `json:"events"`
	// LimitReached is true when collection stopped because MaxEvents was hit.
	LimitReached bool `json:"limit_reached"`
	// Dropped counts events lost because they arrived faster than they were read.
	Dropped int64 `json:"dropped"`
}

// CollectEvents listens for events for a bounded time and returns them. The
// subscription is always removed before returning. If ctx is cancelled the
// events collected so far are returned together with the context error.
func (c *Client) CollectEvents(ctx context.Context, opts CollectEventsOptions) (*CollectedEvents, error) {
	if opts.Duration <= 0 || opts.Duration > maxCollectDuration {
		return nil, invalidArg("duration must be between 0 and %s", maxCollectDuration)
	}
	max := opts.MaxEvents
	if max <= 0 {
		max = defaultCollectMaxEvents
	}
	if max > maxCollectEvents {
		return nil, invalidArg("max events must be at most %d", maxCollectEvents)
	}

	sub, err := c.SubscribeEvents(ctx, opts.EventType)
	if err != nil {
		return nil, err
	}
	defer sub.Close()

	timer := time.NewTimer(opts.Duration)
	defer timer.Stop()

	res := &CollectedEvents{Events: []Event{}}
	for {
		select {
		case raw, open := <-sub.ch:
			if !open {
				res.Dropped = sub.Dropped()
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				return res, sub.Err()
			}
			var ev Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			res.Events = append(res.Events, ev)
			if len(res.Events) >= max {
				res.LimitReached = true
				res.Dropped = sub.Dropped()
				return res, nil
			}
		case <-timer.C:
			res.Dropped = sub.Dropped()
			return res, nil
		case <-ctx.Done():
			res.Dropped = sub.Dropped()
			return res, ctx.Err()
		}
	}
}
