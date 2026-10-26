package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APIError is the daemon's error answer (design 5.2): one code, one line of
// text, whether a retry may succeed and the HTTP status. Update names the
// side to update on a version refusal (5.6).
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Update    string `json:"update,omitempty"`
	Status    int    `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// errorSpec is one row of the 5.2 table.
type errorSpec struct {
	status    int
	retryable bool
	exit      int
}

// errorTable is design 5.2: each code's HTTP status, whether it is
// retryable, and the CLI's exit code.
var errorTable = map[string]errorSpec{
	"bad_request":          {400, false, 2},
	"unauthorized":         {401, false, 2},
	"identity_mismatch":    {403, false, 2},
	"wrong_route":          {403, false, 2},
	"unknown_route":        {404, false, 2},
	"version_missing":      {400, false, 2},
	"version_malformed":    {400, false, 2},
	"version_mismatch":     {426, false, 2},
	"registry_pending":     {409, true, 2},
	"proof_stale":          {409, false, 2},
	"human_boundary":       {409, false, 3},
	"backlog_full":         {409, false, 2},
	"budget_full":          {409, false, 2},
	"claim_held":           {409, false, 2},
	"no_claim":             {409, false, 2},
	"unacked_work":         {409, false, 2},
	"terminal_conflict":    {409, false, 2},
	"key_conflict":         {409, false, 2},
	"key_window_full":      {503, true, 75},
	"ack_too_large":        {413, false, 2},
	"question_exists":      {409, false, 2},
	"question_limit":       {409, false, 2},
	"adopt_busy":           {409, true, 75},
	"busy":                 {503, true, 75},
	"route_unowned":        {503, true, 75},
	"internal":             {500, true, 1},
	"route_exists":         {409, false, 2},
	"adopt_locked":         {409, true, 2},
	"herdr_unreachable":    {503, true, 75},
	"identity_unavailable": {503, true, 75},
	"name_retired":         {409, false, 2},
	"role_taken":           {409, false, 2},
	"unknown_name":         {404, false, 2},
	"unknown_target":       {404, false, 2},
	"no_hold":              {404, false, 2},
	"checksum_mismatch":    {409, false, 2},
	"incomplete_pair":      {409, false, 2},
	"option_unavailable":   {409, false, 2},
	"daemon_running":       {0, false, 2},
	"not_found":            {404, false, 2},
	"method_not_allowed":   {405, false, 2},
}

// Errf builds the error of a code with a formatted message. An unknown code
// is a bug and becomes internal.
func Errf(code, format string, a ...any) *APIError {
	spec, ok := errorTable[code]
	if !ok {
		code, spec = "internal", errorTable["internal"]
	}
	return &APIError{Code: code, Message: fmt.Sprintf(format, a...), Retryable: spec.retryable, Status: spec.status}
}

// ExitCode is the CLI's exit code for an error code (5.2).
func ExitCode(code string) int {
	if spec, ok := errorTable[code]; ok {
		return spec.exit
	}
	return 1
}

// errorBody is the JSON of an error answer.
type errorBody struct {
	Error *APIError `json:"error"`
}

// writeError answers r with e.
func writeError(w http.ResponseWriter, e *APIError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	b, _ := json.Marshal(errorBody{Error: e})
	_, _ = w.Write(append(b, '\n'))
}
