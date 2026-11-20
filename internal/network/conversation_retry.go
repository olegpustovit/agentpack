package network

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// Agents retry a dropped connection themselves for a while, then end the turn with an error line
// such as Claude's "API Error: Connection error." or Codex's "stream disconnected before
// completion: error sending request". Only such a last line counts, never a reply that merely
// talks about networking.
var (
	networkFailureLine = regexp.MustCompile(`(?i)^\W*(api error|error|stream disconnected|request failed|connection error)\b.*\b(connection|network|econnreset|econnrefused|enotfound|eai_again|etimedout|enetunreach|fetch failed|error sending request|timed out|disconnected|socket hang up|unreachable)`)
	networkFailure     = regexp.MustCompile(`(?i)\b(connection (error|refused|reset)|econnreset|econnrefused|enotfound|eai_again|etimedout|enetunreach|fetch failed|error sending request|stream disconnected|network is unreachable|socket hang up)\b`)
)

// networkRetryDelays give a machine that went offline time to come back; one retry per delay.
var networkRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 8 * time.Minute}

const networkRetryPrompt = "The connection to your model service dropped and your last turn failed. Continue where you left off."

// retryAfterNetworkDrop schedules another turn when this one failed because the agent could not
// reach its service. A turn the person sends meanwhile, a cancel or a closed chat drops the retry.
func (session *ConversationSession) retryAfterNetworkDrop(err error) {
	session.mu.Lock()
	session.turnsEnded++
	reply := strings.TrimSpace(string(session.turnText))
	last := reply[strings.LastIndexByte(reply, '\n')+1:]
	dropped := len(last) < 400 && networkFailureLine.MatchString(last) || err != nil && networkFailure.MatchString(err.Error())
	if !dropped || session.userCancelled || session.closedErr() != nil {
		session.networkRetries = 0
		session.mu.Unlock()
		return
	}
	attempt, turns := session.networkRetries, session.turnsEnded
	if attempt >= len(networkRetryDelays) {
		session.mu.Unlock()
		session.logNotice("The agent's service is still unreachable", "Send a message to try again.")
		return
	}
	session.networkRetries++
	after := session.after
	session.mu.Unlock()
	if after == nil {
		after = func(delay time.Duration, run func()) { time.AfterFunc(delay, run) }
	}
	delay := networkRetryDelays[attempt]
	session.logNotice("Lost the connection to the agent's service", fmt.Sprintf("Retrying in %s (%d of %d).", delay, attempt+1, len(networkRetryDelays)))
	after(delay, func() {
		session.mu.Lock()
		stale := session.turnsEnded != turns || session.running || session.closedErr() != nil
		session.mu.Unlock()
		if !stale {
			_, _, _ = session.Submit(context.Background(), acp.PromptInput{Text: networkRetryPrompt}, false)
		}
	})
}

func (session *ConversationSession) logNotice(title, description string) {
	session.log.Append("notice", session.clock().UnixMilli(), mustRawJSON(map[string]string{"sessionUpdate": "notice", "severity": "warning", "title": title, "description": description}))
}
