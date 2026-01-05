package server

import (
	"testing"

	"shelley.exe.dev/db"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestAgentWorking(t *testing.T) {
	tests := []struct {
		name     string
		messages []APIMessage
		want     bool
	}{
		{
			name:     "empty messages",
			messages: []APIMessage{},
			want:     false,
		},
		{
			name: "agent with end_of_turn true",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(true)},
			},
			want: false,
		},
		{
			name: "agent with end_of_turn false",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(false)},
			},
			want: true,
		},
		{
			name: "agent with end_of_turn nil",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: nil},
			},
			want: true,
		},
		{
			name: "error message",
			messages: []APIMessage{
				{Type: string(db.MessageTypeError)},
			},
			want: false,
		},
		{
			name: "agent end_of_turn then tool message means working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(true)},
				{Type: string(db.MessageTypeTool)},
			},
			want: true,
		},
		{
			name: "gitinfo after agent end_of_turn should NOT indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(true)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
		{
			name: "multiple gitinfo after agent end_of_turn should NOT indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(true)},
				{Type: string(db.MessageTypeGitInfo)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
		{
			name: "gitinfo after agent not end_of_turn should indicate working",
			messages: []APIMessage{
				{Type: string(db.MessageTypeAgent), EndOfTurn: boolPtr(false)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: true,
		},
		{
			name: "only gitinfo messages",
			messages: []APIMessage{
				{Type: string(db.MessageTypeGitInfo)},
				{Type: string(db.MessageTypeGitInfo)},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentWorking(tt.messages)
			if got != tt.want {
				t.Errorf("agentWorking() = %v, want %v", got, tt.want)
			}
		})
	}
}
