package messages

import (
	"testing"

	"github.com/plan42-ai/sdk-go/p42"
	p42messages "github.com/plan42-ai/sdk-go/p42/messages"
	"github.com/stretchr/testify/require"
)

func TestShouldFetchPRFeedbackForMainAgentAfterFirstTurn(t *testing.T) {
	connectionID := "conn-1"
	req := &pollerInvokeAgentRequest{
		InvokeAgentRequest: p42messages.InvokeAgentRequest{
			Turn:                      &p42.Turn{TurnIndex: 2},
			PrivateGithubConnectionID: &connectionID,
		},
	}

	require.True(t, req.shouldFetchPRFeedback())
}

func TestShouldFetchPRFeedbackSkipsSubAgent(t *testing.T) {
	connectionID := "conn-1"
	req := &pollerInvokeAgentRequest{
		InvokeAgentRequest: p42messages.InvokeAgentRequest{
			Turn:                      &p42.Turn{TurnIndex: 2},
			PrivateGithubConnectionID: &connectionID,
			SubAgent:                  &p42.SubAgent{AgentID: "agent-1"},
		},
	}

	require.False(t, req.shouldFetchPRFeedback())
}
