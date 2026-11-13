// Package notice is the notice domain of the herdr-core backend (spec section
// 10): an instruction for one execution generation that the daemon records and
// the execution pulls. A notice is issued, claimed, handled or returned; every
// gate is checked when it is issued and again in the transaction that claims
// it, and a notice that cannot be claimed wakes no one. Nothing here types into
// a terminal.
//
// The claim discipline is the inbox's (internal/message/read.go): reading is
// transport evidence and claims, acknowledgement with a token and a record is
// what says the work was accounted for, and a claim never expires by time.
package notice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the version of the notice document this package writes.
const SchemaVersion = 1

// The kinds of notice. Only the daemon's operations issue them; NoticeService
// has no call that does.
const (
	KindBrief       = "brief"        // up's first brief to a coordinator
	KindContinue    = "continue"     // resume's continue prompt
	KindDispatch    = "dispatch"     // a route's dispatch to an execution
	KindCheckpoint  = "checkpoint"   // the checkpoint prompt (phase 5)
	KindTask        = "task"         // the task launch prompt (phase 5)
	KindFinalReport = "final_report" // stop's request for a report
)

// What follows an outcome other than completed (spec section 10).
const (
	FollowReport  = "report"  // an event to the issuer, which the coordinator reads
	FollowReissue = "reissue" // a new notice to the same execution with the old record
	FollowNone    = "none"    // nothing: a later notice replaces it
)

// Rule is what a kind declares.
type Rule struct {
	// SafeToRepeat: doing the work twice does no harm, so an agent that lost
	// its record may do it again; otherwise it acknowledges interrupted.
	SafeToRepeat bool
	FollowUp     string
}

var rules = map[string]Rule{
	KindBrief:       {SafeToRepeat: false, FollowUp: FollowReport},
	KindContinue:    {SafeToRepeat: true, FollowUp: FollowNone},
	KindDispatch:    {SafeToRepeat: false, FollowUp: FollowReport},
	KindCheckpoint:  {SafeToRepeat: true, FollowUp: FollowNone},
	KindTask:        {SafeToRepeat: false, FollowUp: FollowReissue},
	KindFinalReport: {SafeToRepeat: true, FollowUp: FollowNone},
}

// RuleOf returns a kind's rule.
func RuleOf(kind string) (Rule, bool) {
	r, ok := rules[kind]
	return r, ok
}

// The states of a notice.
const (
	StateIssued   = "issued"
	StateClaimed  = "claimed"
	StateHandled  = "handled"
	StateReturned = "returned"
)

// The outcomes of an acknowledgement.
const (
	OutcomeCompleted   = "completed"
	OutcomeInterrupted = "interrupted"
	OutcomeAbandoned   = "abandoned"
)

// ValidOutcome reports an outcome Ack accepts.
func ValidOutcome(o string) bool {
	return o == OutcomeCompleted || o == OutcomeInterrupted || o == OutcomeAbandoned
}

// The gates a claim or an issue can fail, as Read reports them.
const (
	GateApproval      = "approval"
	GatePolicy        = "policy"
	GateIdentity      = "identity"
	GateVisibility    = "visibility"
	GateHeld          = "route_held"
	GateStopping      = "route_stopping"
	GateOrder         = "order"
	GateDeadline      = "deadline"
	GateReturned      = "returned"
	GateNotStopping   = "not_stopping"
	GateStopOperation = "stop_operation"
)

// Limits of a notice's parts.
const (
	MaxContent = 64 << 10
	MaxRecord  = 64 << 10
)

// Notice is one notice with its journal state. It is stored as one document.
type Notice struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	// Key makes issuing idempotent: one operation step, one notice.
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Repo string `json:"repo"`
	// Route and Agent name where the execution belongs.
	Route string `json:"route"`
	Agent string `json:"agent"`
	// Folder is the route folder at issue, where the record is kept.
	Folder string `json:"folder,omitempty"`
	// Execution, Generation and Endpoint are the exact target.
	Execution  string `json:"execution"`
	Generation int64  `json:"generation"`
	Endpoint   string `json:"endpoint"`
	// Issuer is the operation that issued it.
	Issuer string `json:"issuer"`
	// Seq orders the notices of the daemon as they were issued.
	Seq      int64     `json:"seq"`
	IssuedAt time.Time `json:"issued_at"`
	Content  string    `json:"content"`
	Digest   string    `json:"digest"`
	State    string    `json:"state"`

	Token      string    `json:"token,omitempty"`
	ClaimedAt  time.Time `json:"claimed_at,omitzero"`
	RecordPath string    `json:"record_path,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Record     string    `json:"record,omitempty"`
	HandledAt  time.Time `json:"handled_at,omitzero"`
	ReturnedAt time.Time `json:"returned_at,omitzero"`
	// Returned says why a notice was returned.
	ReturnReason string `json:"return_reason,omitempty"`
	// Previous is the record of a returned notice this one replaces.
	Previous string `json:"previous,omitempty"`

	// Recovery of an open claim on a live execution.
	RecoveryDue     bool `json:"recovery_due,omitempty"`
	ResumedSince    bool `json:"resumed_since,omitempty"`
	RecoveryAnswers int  `json:"recovery_answers,omitempty"`
	StuckReported   bool `json:"stuck_reported,omitempty"`

	// The final-report notice's deadline, and whether a daemon restart moved
	// it once. Boot is the daemon run that set it.
	Deadline      time.Time `json:"deadline,omitzero"`
	DeadlineMoved bool      `json:"deadline_moved,omitempty"`
	Boot          string    `json:"boot,omitempty"`

	// A notice that cannot be claimed: since when, by which gate, and
	// whether notice_held was reported.
	HeldSince    time.Time `json:"held_since,omitzero"`
	HeldGate     string    `json:"held_gate,omitempty"`
	HeldReported bool      `json:"held_reported,omitempty"`
	// Attention: needs_attention was raised for an attended execution.
	// A queue reservation survives daemon restart and uncertain queue replies.
	WakePending  bool      `json:"wake_pending,omitempty"`
	WakeAt       time.Time `json:"wake_at,omitzero"`
	WakeReported bool      `json:"wake_reported,omitempty"`
	Attention    bool      `json:"attention,omitempty"`
	Interrupted  bool      `json:"interrupted,omitempty"`
}

// Open reports a claim that is not closed.
func (n Notice) Open() bool { return n.State == StateClaimed }

// Ordinary reports a notice that every ordinary gate governs.
func (n Notice) Ordinary() bool { return n.Kind != KindFinalReport }

// WaitRecord is one wait id an execution's waits used, with the generation
// the daemon gave it. The owner is the highest generation.
type WaitRecord struct {
	Execution  string    `json:"execution"`
	WaitID     string    `json:"wait_id"`
	Generation int64     `json:"generation"`
	Created    time.Time `json:"created"`
}

// RouteState is what a route's lifecycle says to the gates.
type RouteState struct {
	Repo  string `json:"repo"`
	Route string `json:"route"`
	// Held: a pause holds every new claim.
	Held   bool   `json:"held"`
	HeldBy string `json:"held_by,omitempty"`
	// Stopping: a stop operation is under way; only it issues, and only the
	// final-report notice.
	Stopping bool          `json:"stopping"`
	StopOp   string        `json:"stop_op,omitempty"`
	Grace    time.Duration `json:"grace,omitempty"`
	// Folder is the route folder, where processing records are kept.
	Folder string `json:"folder,omitempty"`
}

// Approval is an execution's place in an approved pair, with the policy it
// was launched under.
type Approval struct {
	Execution    string    `json:"execution"`
	Repo         string    `json:"repo"`
	Route        string    `json:"route"`
	PolicyDigest string    `json:"policy_digest"`
	At           time.Time `json:"at"`
}

// Errors of the service.
var (
	ErrUnknown  = errors.New("notice: no such notice of this execution")
	ErrToken    = errors.New("notice: the claim token does not match")
	ErrOutcome  = errors.New("notice: the notice is handled with another outcome")
	ErrRecord   = errors.New("notice: an acknowledgement needs a processing record")
	ErrNotClaim = errors.New("notice: the notice is not claimed")
	ErrBusy     = errors.New("notice: too many waits")
)

// Refusal is an issue or a claim that a gate refused.
type Refusal struct {
	Gate   string
	Detail string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return "notice: refused by the " + r.Gate + " gate"
	}
	return fmt.Sprintf("notice: refused by the %s gate: %s", r.Gate, r.Detail)
}

// GateOf returns the gate of a refusal, or "".
func GateOf(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Gate
	}
	return ""
}

// Digest is the content digest of a notice document.
func Digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on the supported platforms
	}
	return hex.EncodeToString(b)
}

// NewID is a notice id: "n-" and 24 hex digits.
func NewID() string { return "n-" + randomHex(12) }
