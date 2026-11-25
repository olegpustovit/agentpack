package network

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

func (cs *ConversationSession) configureACPGoal(meta map[string]any, pause func(context.Context) error) {
	reports, canPause := acp.GoalControlSupport(meta)
	if !reports {
		return
	}
	cs.Log.KeepAIRGoalDuringReplay()
	cs.mu.Lock()
	cs.nativeGoalReports = true
	if canPause {
		cs.nativeGoalPauseRPC = pause
	}
	cs.mu.Unlock()
}

func (p *acpPrompter) pauseGoal(ctx context.Context) error {
	p.mu.Lock()
	conn, sid := p.conn, p.sessID
	p.mu.Unlock()
	if conn == nil || sid == "" {
		return errors.New("ACP session is not ready for goal control")
	}
	_, err := conn.CallExtension(ctx, acp.GoalControlMethod, struct {
		SessionID sdkacp.SessionId `json:"sessionId"`
		Action    string           `json:"action"`
	}{SessionID: sid, Action: "pause"})
	var rpc *sdkacp.RequestError
	if errors.As(err, &rpc) {
		return fmt.Errorf("agent rejected goal pause (ACP error %d)", rpc.Code)
	}
	return err
}

func (cs *ConversationSession) requestNativeGoalPause(ctx context.Context, pause func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx, event := obs.Start(ctx, "acp.goal", "pause")
	defer event.End()
	event.Set("session_id", cs.sessionID)
	cs.writes.RLock()
	defer cs.writes.RUnlock()
	cs.mu.Lock()
	err := cs.closedErr()
	cs.mu.Unlock()
	if err == nil {
		err = pause(ctx)
	}
	event.Fail(err)
	return err
}
