package protocol

import "encoding/json"

type ErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Health struct {
	Status    string `json:"status"`
	CheckedAt string `json:"checkedAt"`
	Detail    string `json:"detail,omitempty"`
}

type RuntimeDescriptor struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"displayName"`
	Hostname    string         `json:"hostname"`
	Platform    string         `json:"platform"`
	Version     string         `json:"version"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type RuntimeComponent struct {
	ComponentID string         `json:"componentId"`
	Kind        string         `json:"kind"`
	Subtype     string         `json:"subtype,omitempty"`
	Methods     []string       `json:"methods"`
	Health      Health         `json:"health"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ConnectRequest struct {
	Type   string        `json:"type"`
	ID     string        `json:"id"`
	Method string        `json:"method"`
	Params ConnectParams `json:"params"`
}

type ConnectParams struct {
	MinProtocol int                `json:"minProtocol"`
	MaxProtocol int                `json:"maxProtocol"`
	Client      ClientDescriptor   `json:"client"`
	Role        string             `json:"role"`
	Auth        AuthPayload        `json:"auth"`
	Runtime     RuntimeDescriptor  `json:"runtime"`
	Components  []RuntimeComponent `json:"components"`
}

type ClientDescriptor struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Mode     string `json:"mode"`
}

type AuthPayload struct {
	Token string `json:"token"`
}

type RequestFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Event   string          `json:"event,omitempty"`
	Params  map[string]any  `json:"params,omitempty"`
	Meta    map[string]any  `json:"meta,omitempty"`
	OK      bool            `json:"ok,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *ErrorPayload   `json:"error,omitempty"`
}

type SuccessResponse struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Payload any    `json:"payload"`
}

type ErrorResponse struct {
	Type  string       `json:"type"`
	ID    string       `json:"id"`
	OK    bool         `json:"ok"`
	Error ErrorPayload `json:"error"`
}

type EventFrame struct {
	Type    string `json:"type"`
	Event   string `json:"event"`
	Payload any    `json:"payload"`
}

type HelloPayload struct {
	Type     string           `json:"type"`
	Protocol int              `json:"protocol"`
	Policy   HelloPolicy      `json:"policy"`
	Runtime  HelloRuntimeInfo `json:"runtime"`
}

type HelloPolicy struct {
	TickIntervalMS int `json:"tickIntervalMs"`
	MaxTTLSeconds  int `json:"maxTtlSeconds"`
}

type HelloRuntimeInfo struct {
	RuntimeID          string   `json:"runtimeId"`
	PairingState       string   `json:"pairingState"`
	AcceptedComponents []string `json:"acceptedComponents"`
}

func ParseHelloPayload(frame RequestFrame) (HelloPayload, error) {
	var payload HelloPayload
	err := json.Unmarshal(frame.Payload, &payload)
	return payload, err
}
