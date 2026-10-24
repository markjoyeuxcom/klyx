package appbridge

import (
	"context"
	"fmt"

	"github.com/moomora/klyx/internal/fleet"
)

// The A3 matrix verbs below their shells (A3 §4.4). A shell is the manual
// bridge method: checkProtected, the lookup by cluster name, a Background
// context with actionTimeout, then the inner function here, which holds
// everything else - the arming, the write, the admission attribution and
// the receipt. An investigation's RunStep calls the same checkProtected and
// the same inner function, with the conversation's pinned connection and
// its run fence. Only these verbs classify their outcome: their refusals
// before any write are marked fleet.ErrNotSent, so an unmarked failure is
// a write whose answer was lost.

// ReceiptDTO is a write's own witness (A3 §5.1): the written object's
// identity and generation from the server's response, and the token this
// request wrote. A zero field is one the response did not carry.
type ReceiptDTO struct {
	UID             string `json:"uid"`
	Generation      int64  `json:"generation,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Token           string `json:"token,omitempty"`
	PriorGeneration int64  `json:"priorGeneration,omitempty"`
}

// AttemptDTO is the token a request carried when it was sent (a restart's
// restartedAt, a reconcile's requestedAt): kept apart from the receipt, so
// a request whose answer was lost still has its own token (AI A3 stage 1
// review).
type AttemptDTO struct {
	UID   string `json:"uid"`
	Token string `json:"token"`
}

// refusedNotSent is a matrix verb refused before anything was sent.
func refusedNotSent(text string) ActionResultDTO {
	return ActionResultDTO{Error: text, Outcome: fleet.OutcomeRefused, NotSent: true}
}

// verbFailed is a matrix verb's failure, classified; an unverified one
// keeps the token its request was sent with.
func verbFailed(ctx context.Context, conn any, err error, scrubbed bool, rec *fleet.Receipts, uid string) ActionResultDTO {
	var r ActionResultDTO
	if scrubbed {
		r = failedScrubbed(ctx, conn, err)
	} else {
		r = failed(ctx, conn, err)
	}
	r.Outcome, r.NotSent = fleet.WriteOutcome(err)
	if r.Outcome == fleet.OutcomeUnverified {
		r.Attempt = attemptOf(rec, uid)
	}
	return r
}

// wrote is a matrix verb's success, with the receipt of its write to uid.
func wrote(rec *fleet.Receipts, uid string) ActionResultDTO {
	r := ActionResultDTO{OK: true, Outcome: fleet.OutcomeWritten, Attempt: attemptOf(rec, uid)}
	if rc, ok := rec.Of(uid); ok {
		r.Receipt = &ReceiptDTO{UID: rc.UID, Generation: rc.Generation, ResourceVersion: rc.ResourceVersion, Token: rc.Token, PriorGeneration: rc.PriorGeneration}
	}
	return r
}

func attemptOf(rec *fleet.Receipts, uid string) *AttemptDTO {
	if a, ok := rec.AttemptOf(uid); ok {
		return &AttemptDTO{UID: a.UID, Token: a.Token}
	}
	return nil
}

func notConnected(cluster string) ActionResultDTO {
	return refusedNotSent("cluster not connected: " + cluster)
}

// Each inner function needs only its own write: the manual shells pass
// their full connections, an investigation's RunStep its pinned one.
type (
	restarter interface {
		RolloutRestart(ctx context.Context, kind, namespace, name, uid string) error
	}
	scaler interface {
		ScaleWorkload(ctx context.Context, kind, namespace, name, uid string, replicas int32) error
	}
	undoer interface {
		RolloutUndo(ctx context.Context, kind, namespace, name, uid string, revision int64, expect *fleet.RevisionExpect) error
	}
	pauser interface {
		RolloutPause(ctx context.Context, namespace, name, uid string, pause bool) error
	}
	podDeleter interface {
		DeletePod(ctx context.Context, namespace, name, uid string) error
	}
	cordoner interface {
		SetCordon(ctx context.Context, node, uid string, cordon bool) error
	}
	reconciler interface {
		Reconcile(ctx context.Context, kind, namespace, name, uid string) error
	}
	suspender interface {
		SetSuspend(ctx context.Context, kind, namespace, name, uid string, suspend bool) error
	}
	kedaPauser interface {
		KEDASetPaused(ctx context.Context, resource, namespace, name, uid string, pause bool) error
	}
)

func doRolloutRestart(ctx context.Context, conn restarter, kind, namespace, name, uid string) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.RolloutRestart(ctx, kind, namespace, name, uid); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

// doScaleWorkload checks the count after the shell's checkProtected, so an
// out-of-range count consumes the grant like any other armed call (A3 §0).
func doScaleWorkload(ctx context.Context, conn scaler, kind, namespace, name, uid string, replicas int) ActionResultDTO {
	if replicas < 0 || replicas > maxScaleReplicas {
		return refusedNotSent(fmt.Sprintf("replicas %d out of range [0, %d]", replicas, maxScaleReplicas))
	}
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.ScaleWorkload(ctx, kind, namespace, name, uid, int32(replicas)); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doRolloutUndo(ctx context.Context, conn undoer, kind, namespace, name, uid string, revision int, expect *fleet.RevisionExpect) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.RolloutUndo(ctx, kind, namespace, name, uid, int64(revision), expect); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doRolloutPause(ctx context.Context, conn pauser, namespace, name, uid string, pause bool) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.RolloutPause(ctx, namespace, name, uid, pause); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doDeletePod(ctx context.Context, conn podDeleter, namespace, name, uid string) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.DeletePod(ctx, namespace, name, uid); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doCordon(ctx context.Context, conn cordoner, node, uid string, cordon bool) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.SetCordon(ctx, node, uid, cordon); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doReconcile(ctx context.Context, conn reconciler, kind, namespace, name, uid string) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.Reconcile(ctx, kind, namespace, name, uid); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doSetSuspend(ctx context.Context, conn suspender, kind, namespace, name, uid string, suspend bool) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.SetSuspend(ctx, kind, namespace, name, uid, suspend); err != nil {
		return verbFailed(ctx, conn, err, false, rec, uid)
	}
	return wrote(rec, uid)
}

func doKEDASetPaused(ctx context.Context, conn kedaPauser, resource, namespace, name, uid string, pause bool) ActionResultDTO {
	ctx, rec := fleet.WithReceipts(ctx)
	if err := conn.KEDASetPaused(ctx, resource, namespace, name, uid, pause); err != nil {
		return verbFailed(ctx, conn, err, true, rec, uid)
	}
	return wrote(rec, uid)
}
