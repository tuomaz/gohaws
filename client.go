package gohaws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type HaClient struct {
	URI          string
	Token        string
	Conn         *websocket.Conn
	connMu       sync.RWMutex
	AuthChannel  chan *Message
	OtherChannel chan *Message
	EventChannel chan *Message
	id           atomic.Int64
	entitiesMu   sync.RWMutex
	entities     []string
	statesMu     sync.RWMutex
	states       map[string]*State
}

type Message struct {
	ID          int64             `json:"id,omitempty"`
	AccessToken string            `json:"access_token,omitempty"`
	Type        string            `json:"type,omitempty"`
	Event       *Event            `json:"event,omitempty"`
	EventType   string            `json:"event_type,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	Service     string            `json:"service,omitempty"`
	ServiceData interface{}       `json:"service_data,omitempty"`
	Target      map[string]string `json:"target,omitempty"`
	Success     bool              `json:"success,omitempty"`
	Result      json.RawMessage   `json:"result,omitempty"`
}

type Event struct {
	Data *Data `json:"data,omitempty"`
}

type Data struct {
	EntityID  string                 `json:"entity_id,omitempty"`
	EventType string                 `json:"data,omitempty"`
	TimeFired time.Time              `json:"time_fired,omitempty"`
	Origin    string                 `json:"origin,omitempty"`
	Context   *HaContext             `json:"context,omitempty"`
	NewState  *State                 `json:"new_state,omitempty"`
	OldState  map[string]interface{} `json:"old_state,omitempty"`
}

type HaContext struct {
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
}

type State struct {
	DeviceClass       string                 `json:"device_class,omitempty"`
	FriendlyName      string                 `json:"friendly_name,omitempty"`
	Icon              string                 `json:"icon,omitempty"`
	StateClass        string                 `json:"state_class,omitempty"`
	UnitOfMeasurement string                 `json:"unit_of_measurement,omitempty"`
	Context           *HaContext              `json:"context,omitempty"`
	EntityID          string                 `json:"entity_id,omitempty"`
	LastChanged       time.Time              `json:"last_changed,omitempty"`
	LastUpdated       time.Time              `json:"last_updated,omitempty"`
	State             interface{}            `json:"state,omitempty"`
	Attributes        map[string]interface{} `json:"attributes,omitempty"`
}

func New(ctx context.Context, URI, token string) (*HaClient, error) {
	client := &HaClient{
		URI:    URI,
		Token:  token,
		states: make(map[string]*State),
	}
	client.AuthChannel = make(chan *Message, 10)
	client.OtherChannel = make(chan *Message, 10)
	client.EventChannel = make(chan *Message, 10)
	err := client.connect(ctx)
	if err != nil {
		return nil, err
	}
	go receiver(ctx, client)
	client.id.Store(1)
	return client, nil
}

func receiver(ctx context.Context, ha *HaClient) {
	for {
		select {
		case <-ctx.Done():
			log.Printf("HA: receiver stopping due to context done")
			goto done
		default:
			ha.connMu.RLock()
			conn := ha.Conn
			ha.connMu.RUnlock()

			if conn == nil {
				time.Sleep(time.Second)
				continue
			}

			buf := &Message{}
			err := wsjson.Read(ctx, conn, buf)
			if err != nil {
				log.Printf("HA: could not read from HA WS: %v\n", err)
				// Connection might be closed, try to reconnect
				ha.connMu.Lock()
				ha.Conn = nil
				ha.connMu.Unlock()

				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						default:
							log.Printf("HA: attempting to reconnect...")
							if err := ha.connect(ctx); err == nil {
								log.Printf("HA: reconnected")
								// Re-subscribe and re-fetch states after reconnect
								if err := ha.SubscribeToUpdates(ctx); err != nil {
									log.Printf("HA: failed to re-subscribe: %v", err)
								}
								if err := ha.FetchStates(ctx); err != nil {
									log.Printf("HA: failed to re-fetch states: %v", err)
								}
								return
							}
							time.Sleep(5 * time.Second)
						}
					}
				}()
				time.Sleep(time.Second) // Give it a moment before trying to read again
				continue
			}

			switch buf.Type {
			case "auth":
				ha.AuthChannel <- buf
			case "event":
				if buf.Event != nil && buf.Event.Data != nil && buf.Event.Data.NewState != nil {
					ha.statesMu.Lock()
					ha.states[buf.Event.Data.EntityID] = buf.Event.Data.NewState
					ha.statesMu.Unlock()
				}
				if ha.filterMessage(buf) {
					ha.EventChannel <- buf
				}
			default:
				select {
				case ha.OtherChannel <- buf:
				default:
					log.Printf("HA: OtherChannel full, dropping message")
				}
			}
		}
	}

done:
	log.Printf("HA: closing channels")
	close(ha.AuthChannel)
	close(ha.EventChannel)
	close(ha.OtherChannel)
	log.Printf("HA: done closing channels")
}

func (ha *HaClient) connect(ctx context.Context) error {
	fullHAWSURI := fmt.Sprintf("%s/api/websocket", ha.URI)

	ha.connMu.Lock()
	defer ha.connMu.Unlock()

	conn, _, err := websocket.Dial(ctx, fullHAWSURI, nil)
	if err != nil {
		return fmt.Errorf("HA: could not connect: %w", err)
	}

	// Increase read limit to 1MB as Home Assistant state responses can be large
	conn.SetReadLimit(1024 * 1024)

	ha.Conn = conn
	log.Printf("HA: connect ok")

	buf := &Message{}
	err = wsjson.Read(ctx, ha.Conn, buf)
	if err != nil {
		ha.Conn.Close(websocket.StatusAbnormalClosure, "read error during handshake")
		return fmt.Errorf("HA: could not read from websocket: %w", err)
	}

	if buf.Type == "auth_required" {
		log.Printf("HA: auth required")
	}

	am := Message{
		Type:        "auth",
		AccessToken: ha.Token,
	}
	err = wsjson.Write(ctx, ha.Conn, am)
	if err != nil {
		ha.Conn.Close(websocket.StatusAbnormalClosure, "write error during handshake")
		return fmt.Errorf("HA: could not write to websocket: %w", err)
	}

	log.Printf("HA: wrote auth message")

	buf = &Message{}
	err = wsjson.Read(ctx, ha.Conn, buf)
	if err != nil {
		ha.Conn.Close(websocket.StatusAbnormalClosure, "read error after auth")
		return fmt.Errorf("HA: could not read from HA WS: %w", err)
	}

	if buf.Type != "auth_ok" {
		ha.Conn.Close(websocket.StatusAbnormalClosure, "auth failed")
		return fmt.Errorf("HA: auth failed: %v", buf.Type)
	}

	return nil
}

func (ha *HaClient) SubscribeToUpdates(ctx context.Context) error {
	id := ha.id.Add(1)
	se := &Message{
		ID:        id,
		Type:      "subscribe_events",
		EventType: "state_changed",
	}

	ha.connMu.RLock()
	conn := ha.Conn
	ha.connMu.RUnlock()

	if conn == nil {
		return errors.New("HA: not connected")
	}

	err := wsjson.Write(ctx, conn, se)
	if err != nil {
		return err
	}

	buf := <-ha.OtherChannel

	if !buf.Success {
		return errors.New("HA: could not subscribe to updates from HA")
	}

	return nil
}

func (ha *HaClient) CallService(ctx context.Context, domain string, service string, serviceData interface{}, target string) error {
	id := ha.id.Add(1)
	targetMap := make(map[string]string)

	if target != "" {
		targetMap["entity_id"] = target
	}

	se := &Message{
		ID:          id,
		Type:        "call_service",
		Domain:      domain,
		Service:     service,
		ServiceData: serviceData,
		Target:      targetMap,
	}

	ha.connMu.RLock()
	conn := ha.Conn
	ha.connMu.RUnlock()

	if conn == nil {
		return errors.New("HA: not connected")
	}

	err := wsjson.Write(ctx, conn, se)
	if err != nil {
		return err
	}

	buf := <-ha.OtherChannel

	if !buf.Success {
		return errors.New("HA: could not call service from HA")
	}

	return nil
}

func (ha *HaClient) filterMessage(message *Message) bool {
	ha.entitiesMu.RLock()
	defer ha.entitiesMu.RUnlock()
	if message != nil && message.Event != nil && message.Event.Data != nil {
		for _, k := range ha.entities {
			if k == message.Event.Data.EntityID {
				return true
			}
		}
	}
	return false
}

func (ha *HaClient) Add(entityName string) {
	ha.entitiesMu.Lock()
	defer ha.entitiesMu.Unlock()
	ha.entities = append(ha.entities, entityName)
}

func (ha *HaClient) GetState(entityID string) (*State, bool) {
	ha.statesMu.RLock()
	defer ha.statesMu.RUnlock()
	state, ok := ha.states[entityID]
	return state, ok
}

func (ha *HaClient) GetAllStates() map[string]*State {
	ha.statesMu.RLock()
	defer ha.statesMu.RUnlock()
	// Return a copy to avoid external modification of the internal map
	copy := make(map[string]*State)
	for k, v := range ha.states {
		copy[k] = v
	}
	return copy
}

func (ha *HaClient) FetchStates(ctx context.Context) error {
	id := ha.id.Add(1)
	se := &Message{
		ID:   id,
		Type: "get_states",
	}

	ha.connMu.RLock()
	conn := ha.Conn
	ha.connMu.RUnlock()

	if conn == nil {
		return errors.New("HA: not connected")
	}

	err := wsjson.Write(ctx, conn, se)
	if err != nil {
		return err
	}

	buf := <-ha.OtherChannel

	if !buf.Success {
		return errors.New("HA: could not fetch states from HA")
	}

	var statesList []*State
	if err := json.Unmarshal(buf.Result, &statesList); err != nil {
		return fmt.Errorf("HA: could not unmarshal states: %w", err)
	}

	ha.statesMu.Lock()
	defer ha.statesMu.Unlock()
	for _, st := range statesList {
		if st.EntityID != "" {
			ha.states[st.EntityID] = st
		}
	}

	return nil
}
