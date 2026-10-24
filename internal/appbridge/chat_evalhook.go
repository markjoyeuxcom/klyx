//go:build !eval

package appbridge

import (
	"encoding/json"

	"github.com/moomora/klyx/internal/ai"
)

// The AI eval's delivered-evidence record (eval design §5) observes a turn
// at these points. Only the eval build gives them a body
// (chat_evalhook_eval.go); here evalHook holds nothing and every hook is
// empty.
type evalHook struct{} //nolint:unused // the eval build gives it state

func (r *turnRunner) evalMode(string)                                  {}
func (r *turnRunner) evalRead(string, json.RawMessage, ChatToolResult) {}
func (r *turnRunner) evalSent([]ai.ConvMessage, bool)                  {}
func (r *turnRunner) evalSnapshotDoc([]string, []string)               {}
func (r *turnRunner) evalSnapshotSent()                                {}
func (r *turnRunner) evalToolsReport(bool)                             {}
func (r *turnRunner) evalSnapshotReport(bool, bool)                    {}
