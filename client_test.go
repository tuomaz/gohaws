package gohaws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestNew(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "the sky is falling")

		// 1. Send auth_required
		wsjson.Write(r.Context(), c, Message{Type: "auth_required"})

		// 2. Receive auth
		var msg Message
		wsjson.Read(r.Context(), c, &msg)
		if msg.Type != "auth" || msg.AccessToken != "test-token" {
			return
		}

		// 3. Send auth_ok
		wsjson.Write(r.Context(), c, Message{Type: "auth_ok"})
	}))
	defer s.Close()

	uri := strings.Replace(s.URL, "http", "ws", 1)
	client, err := New(ctx, uri, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}

	// Wait a bit for receiver to start and potentially process messages
	time.Sleep(100 * time.Millisecond)
}

func TestCallService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "the sky is falling")

		wsjson.Write(r.Context(), c, Message{Type: "auth_required"})
		var msg Message
		wsjson.Read(r.Context(), c, &msg)
		wsjson.Write(r.Context(), c, Message{Type: "auth_ok"})

		// Wait for call_service
		wsjson.Read(r.Context(), c, &msg)
		if msg.Type == "call_service" {
			wsjson.Write(r.Context(), c, Message{
				ID:      msg.ID,
				Type:    "result",
				Success: true,
			})
		}
	}))
	defer s.Close()

	uri := strings.Replace(s.URL, "http", "ws", 1)
	client, err := New(ctx, uri, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	err = client.CallService(ctx, "light", "turn_on", nil, "light.test")
	if err != nil {
		t.Fatalf("CallService failed: %v", err)
	}
}

func TestFiltering(t *testing.T) {
	ha := &HaClient{}
	ha.Add("sensor.temp")

	msg := &Message{
		Event: &Event{
			Data: &Data{
				EntityID: "sensor.temp",
			},
		},
	}

	if !ha.filterMessage(msg) {
		t.Error("expected sensor.temp to be filtered in")
	}

	msg.Event.Data.EntityID = "sensor.humidity"
	if ha.filterMessage(msg) {
		t.Error("expected sensor.humidity to be filtered out")
	}
}

func TestFetchStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "the sky is falling")

		wsjson.Write(r.Context(), c, Message{Type: "auth_required"})
		var msg Message
		wsjson.Read(r.Context(), c, &msg)
		wsjson.Write(r.Context(), c, Message{Type: "auth_ok"})

		// Wait for get_states
		wsjson.Read(r.Context(), c, &msg)
		if msg.Type == "get_states" {
			wsjson.Write(r.Context(), c, Message{
				ID:      msg.ID,
				Type:    "result",
				Success: true,
				Result:  []byte(`[{"entity_id": "sensor.test", "state": "on", "attributes": {"friendly_name": "Test Sensor"}}]`),
			})
		}
	}))
	defer s.Close()

	uri := strings.Replace(s.URL, "http", "ws", 1)
	client, err := New(ctx, uri, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	err = client.FetchStates(ctx)
	if err != nil {
		t.Fatalf("FetchStates failed: %v", err)
	}

	state, ok := client.GetState("sensor.test")
	if !ok {
		t.Fatal("expected to find sensor.test in cache")
	}
	if state.State != "on" {
		t.Errorf("expected state to be 'on', got %v", state.State)
	}
	if state.Attributes["friendly_name"] != "Test Sensor" {
		t.Errorf("expected friendly_name to be 'Test Sensor', got %v", state.Attributes["friendly_name"])
	}
}
