package gohaws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type HaClient struct {
	URI          string
	Token        string
	Conn         *websocket.Conn
	AuthChannel  chan *Message
	OtherChannel chan *Message
	EventChannel chan *Message
	ID           int64
	entities     []string
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
	Result      *HaContext        `json:"result,omitempty"`
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
	DeviceClass       string      `json:"device_class,omitempty"`
	FriendlyName      string      `json:"friendly_name,omitempty"`
	Icon              string      `json:"icon,omitempty"`
	StateClass        string      `json:"state_class,omitempty"`
	UnitOfMeasurement string      `json:"unit_of_measurement,omitempty"`
	Context           *HaContext  `json:"context,omitempty"`
	EntityID          string      `json:"entity_id,omitempty"`
	LastChanged       time.Time   `json:"last_changed,omitempty"`
	LastUpdated       time.Time   `json:"last_updated,omitempty"`
	State             interface{} `json:"state,omitempty"`
}

func New(ctx context.Context, URI, token string) *HaClient {
	client := &HaClient{URI: URI, Token: token}
	client.AuthChannel = make(chan *Message)
	client.OtherChannel = make(chan *Message)
	client.EventChannel = make(chan *Message)
	client.connect(ctx)
	go receiver(ctx, client)
	client.ID = 1
	return client
}

func receiver(ctx context.Context, ha *HaClient) {
	loop := true
	for loop {
		select {
		case <-ctx.Done():
			loop = false
		default:
			buf := &Message{}
			err := wsjson.Read(ctx, ha.Conn, buf)
			if err != nil {
				log.Printf("HA: could not read from HA WS 1: %v\n", err)
				loop = false
			} else {
				switch buf.Type {
				case "auth":
					ha.AuthChannel <- buf
				case "event":
					//log.Printf("HA: receiveed event: %v", buf.Event.Data.EntityID)
					if ha.filterMessage(buf) {
						ha.EventChannel <- buf
					}
				default:
					ha.OtherChannel <- buf
				}
			}
		}
	}
	log.Printf("HA: closing channels")
	close(ha.AuthChannel)
	close(ha.EventChannel)
	close(ha.OtherChannel)
	log.Printf("HA: done closing channels")
}

func (ha *HaClient) connect(ctx context.Context) {
	fullHAWSURI := fmt.Sprintf("%s/api/websocket", ha.URI)

	var err error
	ha.Conn, _, err = websocket.Dial(ctx, fullHAWSURI, nil)
	if err != nil {
		log.Fatalf("HA: could not connect: %v", err)
	}

	log.Printf("HA: connect ok")
	//defer ha.Conn.Close(websocket.StatusInternalError, "the sky is falling")

	buf := &Message{}
	err = wsjson.Read(ctx, ha.Conn, buf)
	if err != nil {
		log.Fatalf("HA: could not read from websocket: %v", err)
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
		log.Fatalf("HA: could not write to websocket: %v", err)
	}

	log.Printf("HA: wrote auth message")

	buf = &Message{}
	err = wsjson.Read(ctx, ha.Conn, buf)
	if err != nil {
		log.Printf("HA: could not read from HA WS")
	}
}

func (ha *HaClient) SubscribeToUpdates(ctx context.Context) error {
	se := &Message{
		ID:        ha.ID,
		Type:      "subscribe_events",
		EventType: "state_changed",
	}
	err := wsjson.Write(ctx, ha.Conn, se)
	ha.ID = ha.ID + 1

	if err != nil {
		return err
	}

	buf := <-ha.OtherChannel

	if !buf.Success {
		return errors.New("HA: could not subscribe to updates from HA")
	}

	return nil
}

func (ha *HaClient) CallService(ctx context.Context, domain string, service string, serviceData interface{}) error {
	se := &Message{
		ID:          ha.ID,
		Type:        "call_service",
		Domain:      domain,
		Service:     service,
		ServiceData: serviceData,
	}
	err := wsjson.Write(ctx, ha.Conn, se)
	ha.ID = ha.ID + 1

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
	ha.entities = append(ha.entities, entityName)
}
