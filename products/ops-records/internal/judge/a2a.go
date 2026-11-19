package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
)

// A2A task states (A2A 1.0).
const (
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

// maxA2ABody is the largest A2A answer that is read.
const maxA2ABody = 1 << 20

// cancelWait is how long a CancelTask call may take.
const cancelWait = 10 * time.Second

// ErrNoAnswer is wrapped when the agent ended its turn without an answer.
var ErrNoAnswer = errors.New("judge: the agent gave no answer")

// Agent asks one agent one question.
type Agent interface {
	// Ask sends text as the one text of a user message and returns the answer, a valid JSON
	// document. It returns when the agent's turn has ended or ctx is done.
	Ask(ctx context.Context, url, bearer, text string) (json.RawMessage, error)
}

// A2A is an Agent that speaks A2A 1.0 JSON-RPC over HTTP.
//
// Ask sends SendMessage with returnImmediately false, so the one HTTP request waits for the end
// of the turn and nothing polls. A new message starts a new task: no context is shared between
// turns. The answer is taken from the parts of the task's artifacts, else from the parts of its
// status message, else from a message the agent answered with instead of a task: the last data
// part, else the last text part. A data part whose value is an object with the one key "data"
// is unwrapped once. A text part is the answer when it is one JSON document; any other text is
// returned as a JSON string, so that it fails validation and is still recorded. A task that
// waits for input or for authentication is cancelled with CancelTask, and Ask returns an error
// wrapping ErrNoAnswer. A failed, cancelled or rejected task is an error.
type A2A struct {
	Client *http.Client // nil means http.DefaultClient
	User   string       // the header X-User-Id; empty means "ops-records"
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type a2aPart struct {
	Text *string         `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type a2aMessage struct {
	MessageID string    `json:"messageId,omitempty"`
	Role      string    `json:"role,omitempty"`
	Parts     []a2aPart `json:"parts"`
}

type a2aTask struct {
	ID     string `json:"id"`
	Status struct {
		State   string      `json:"state"`
		Message *a2aMessage `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []a2aPart `json:"parts"`
	} `json:"artifacts"`
}

// Ask implements Agent.
func (a *A2A) Ask(ctx context.Context, url, bearer, text string) (json.RawMessage, error) {
	params := map[string]any{
		"message":       a2aMessage{MessageID: ids.New(), Role: "ROLE_USER", Parts: []a2aPart{{Text: &text}}},
		"configuration": map[string]any{"returnImmediately": false},
	}
	result, err := a.call(ctx, url, bearer, "SendMessage", params)
	if err != nil {
		return nil, err
	}
	var outer struct {
		Task    *a2aTask    `json:"task"`
		Message *a2aMessage `json:"message"`
		Status  *struct{}   `json:"status"`
		Parts   []a2aPart   `json:"parts"`
	}
	if err := json.Unmarshal(result, &outer); err != nil {
		return nil, fmt.Errorf("judge: A2A SendMessage: the result is not a task or a message: %w", err)
	}
	switch {
	case outer.Task != nil:
		return a.fromTask(ctx, url, bearer, *outer.Task)
	case outer.Message != nil:
		return answerOf(outer.Message.Parts)
	case outer.Status != nil:
		var task a2aTask
		if err := json.Unmarshal(result, &task); err != nil {
			return nil, fmt.Errorf("judge: A2A SendMessage: %w", err)
		}
		return a.fromTask(ctx, url, bearer, task)
	case outer.Parts != nil:
		return answerOf(outer.Parts)
	}
	return nil, fmt.Errorf("%w: the result is neither a task nor a message", ErrNoAnswer)
}

func (a *A2A) fromTask(ctx context.Context, url, bearer string, task a2aTask) (json.RawMessage, error) {
	switch task.Status.State {
	case StateCompleted:
		var parts []a2aPart
		for _, art := range task.Artifacts {
			parts = append(parts, art.Parts...)
		}
		if len(parts) == 0 && task.Status.Message != nil {
			parts = task.Status.Message.Parts
		}
		return answerOf(parts)
	case StateFailed, StateCanceled, StateRejected:
		return nil, fmt.Errorf("judge: A2A task %s ended %s", task.ID, task.Status.State)
	}
	// Waiting for input or authentication, or not ended: a judge cannot continue it.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelWait)
	defer cancel()
	if _, err := a.call(cctx, url, bearer, "CancelTask", map[string]any{"id": task.ID}); err != nil {
		return nil, fmt.Errorf("%w: task %s is %s and CancelTask failed: %v", ErrNoAnswer, task.ID, task.Status.State, err)
	}
	return nil, fmt.Errorf("%w: task %s was %s and is cancelled", ErrNoAnswer, task.ID, task.Status.State)
}

// answerOf returns the last data part, else the last text part, as a JSON document.
func answerOf(parts []a2aPart) (json.RawMessage, error) {
	for i := len(parts) - 1; i >= 0; i-- {
		if d := bytes.TrimSpace(parts[i].Data); len(d) > 0 && json.Valid(d) {
			var wrapped map[string]json.RawMessage
			if json.Unmarshal(d, &wrapped) == nil && len(wrapped) == 1 && wrapped["data"] != nil &&
				bytes.HasPrefix(bytes.TrimSpace(wrapped["data"]), []byte("{")) {
				d = bytes.TrimSpace(wrapped["data"])
			}
			return d, nil
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Text == nil || strings.TrimSpace(*parts[i].Text) == "" {
			continue
		}
		t := []byte(strings.TrimSpace(*parts[i].Text))
		if json.Valid(t) {
			return t, nil
		}
		return json.Marshal(*parts[i].Text)
	}
	return nil, fmt.Errorf("%w: no data part and no text part", ErrNoAnswer)
}

// call sends one JSON-RPC request and returns its result.
func (a *A2A) call(ctx context.Context, url, bearer, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: ids.New(), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("judge: A2A %s: %w", method, err)
	}
	user := a.User
	if user == "" {
		user = "ops-records"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set("X-User-Id", user)
	req.Header.Set("Authorization", "Bearer "+bearer)
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("judge: A2A %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxA2ABody+1))
	if err != nil {
		return nil, fmt.Errorf("judge: A2A %s: %w", method, err)
	}
	if len(data) > maxA2ABody {
		return nil, fmt.Errorf("judge: A2A %s: the answer is longer than %d bytes", method, maxA2ABody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("judge: A2A %s: HTTP %d", method, resp.StatusCode)
	}
	var r rpcResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("judge: A2A %s: the answer is not JSON-RPC: %w", method, err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("judge: A2A %s: error %d: %s", method, r.Error.Code, r.Error.Message)
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return nil, fmt.Errorf("judge: A2A %s: the answer has no result", method)
	}
	return r.Result, nil
}
