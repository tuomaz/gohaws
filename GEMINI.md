# gohaws - Golang Home Assistant Web Socket client

`gohaws` is a lightweight Go library for interacting with the Home Assistant WebSocket API. It simplifies the process of connecting, authenticating, and communicating with a Home Assistant instance.

## Key Features

- **Authentication:** Handles the `auth_required` flow and uses Long-Lived Access Tokens.
- **Resilience:** Implements automatic reconnection logic when the connection is lost.
- **Event Subscription:** Easily subscribe to `state_changed` events.
- **State Tracking (New):** Maintains a local cache of entity states.
    - `FetchStates(ctx)`: Populates the initial cache.
    - `GetState(entityID)`: Thread-safe retrieval of a single entity's state.
    - `GetAllStates()`: Returns a copy of all tracked entity states.
    - Automatic updates: The cache is updated in real-time as `state_changed` events arrive.
- **Service Calls:** Supports calling any Home Assistant service with data and target entity IDs.
- **Message Filtering:** Built-in mechanism to filter incoming events based on a whitelist of entity IDs.
- **Channels-based Communication:** Uses Go channels (`AuthChannel`, `EventChannel`, `OtherChannel`) to expose incoming messages.

## Core Components

- `HaClient`: The primary struct for managing the connection and state.
- `Message`: Unified struct for JSON serialization/deserialization of WebSocket messages.
- `State`: Represents the state of a Home Assistant entity.

## Usage Overview

1.  **Initialize:** Create a new client using `gohaws.New(ctx, uri, token)`.
2.  **Filter Entities:** Use `client.Add("sensor.my_sensor")` to specify which entity updates should be sent to the `EventChannel`.
3.  **Subscribe:** Call `client.SubscribeToUpdates(ctx)` to start receiving event data.
4.  **Listen:** Read from `client.EventChannel` to process incoming state changes.
5.  **Act:** Use `client.CallService(...)` to control entities or trigger automations.

## Dependencies

- `nhooyr.io/websocket`: A high-performance WebSocket library for Go.
