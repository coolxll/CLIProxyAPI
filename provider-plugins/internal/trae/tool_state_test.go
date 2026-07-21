package trae

import (
	"testing"
)

func TestEncodeDecodeTraeToolID(t *testing.T) {
	tests := []struct {
		name    string
		state   traeToolState
		wantErr bool
	}{
		{
			name: "basic state",
			state: traeToolState{
				SessionID:      "session123",
				ConversationID: "conv456",
				TaskID:         "task789",
				AgentRunID:     "agent000",
				NativeID:       "native111",
				Name:           "Bash",
			},
			wantErr: false,
		},
		{
			name: "empty fields",
			state: traeToolState{
				SessionID:      "",
				ConversationID: "",
				TaskID:         "",
				AgentRunID:     "",
				NativeID:       "",
				Name:           "",
			},
			wantErr: true, // decodeTraeToolID validates all fields are non-empty
		},
		{
			name: "special characters",
			state: traeToolState{
				SessionID:      "session-with-dashes",
				ConversationID: "conv_with_underscores",
				TaskID:         "task.with.dots",
				AgentRunID:     "agent:with:colons",
				NativeID:       "native/with/slashes",
				Name:           "Read",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := encodeTraeToolID(tt.state)
			if err != nil {
				t.Errorf("encodeTraeToolID() error = %v", err)
				return
			}

			decoded, err := decodeTraeToolID(encoded)
			if (err != nil) != tt.wantErr {
				t.Errorf("decodeTraeToolID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if decoded.SessionID != tt.state.SessionID {
				t.Errorf("SessionID mismatch: got %v, want %v", decoded.SessionID, tt.state.SessionID)
			}
			if decoded.ConversationID != tt.state.ConversationID {
				t.Errorf("ConversationID mismatch: got %v, want %v", decoded.ConversationID, tt.state.ConversationID)
			}
			if decoded.TaskID != tt.state.TaskID {
				t.Errorf("TaskID mismatch: got %v, want %v", decoded.TaskID, tt.state.TaskID)
			}
			if decoded.AgentRunID != tt.state.AgentRunID {
				t.Errorf("AgentRunID mismatch: got %v, want %v", decoded.AgentRunID, tt.state.AgentRunID)
			}
			if decoded.NativeID != tt.state.NativeID {
				t.Errorf("NativeID mismatch: got %v, want %v", decoded.NativeID, tt.state.NativeID)
			}
			if decoded.Name != tt.state.Name {
				t.Errorf("Name mismatch: got %v, want %v", decoded.Name, tt.state.Name)
			}
		})
	}
}

func TestDecodeTraeToolID_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "empty string",
			id:      "",
			wantErr: true,
		},
		{
			name:    "no prefix",
			id:      "invalid-id",
			wantErr: true,
		},
		{
			name:    "invalid base64",
			id:      "trae_!!!invalid!!!",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			id:      "trae_aW52YWxpZCBqc29u", // "invalid json" in base64
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeTraeToolID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("decodeTraeToolID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
