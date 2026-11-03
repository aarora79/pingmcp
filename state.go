package main

// state.go implements the needs_input tool: a multi round-trip request (MRTR).
// The first call returns resultType "input_required" with a form elicitation
// and an HMAC-signed requestState. The client retries with inputResponses and
// the same requestState. Following the spec's SHOULD list, the state is bound
// to the caller, expires quickly, and is tied to the tool and its arguments.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const stateTTL = 2 * time.Minute

// stateKey signs requestState. It comes from PINGMCP_STATE_KEY, or is random
// per process, which is enough for a single test instance.
var stateKey []byte

type requestState struct {
	Principal  string `json:"p"`
	Tool       string `json:"t"`
	ArgsDigest string `json:"a"`
	Expires    int64  `json:"e"`
}

func initStateKey() {
	if key := os.Getenv("PINGMCP_STATE_KEY"); key != "" {
		stateKey = []byte(key)
		return
	}
	stateKey = make([]byte, 32)
	_, _ = rand.Read(stateKey)
}

func argsDigest(arguments map[string]any) string {
	// json.Marshal sorts map keys, so equal arguments give equal digests.
	data, _ := json.Marshal(arguments)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sign(payload []byte) string {
	mac := hmac.New(sha256.New, stateKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signState(state requestState) string {
	payload, _ := json.Marshal(state)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + sign(payload)
}

// verifyState checks the signature, expiry, caller, tool and arguments.
func verifyState(token string, expected requestState) error {
	encoded, signature, found := strings.Cut(token, ".")
	if !found {
		return errors.New("malformed requestState")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !hmac.Equal([]byte(sign(payload)), []byte(signature)) {
		return errors.New("requestState signature check failed")
	}
	var state requestState
	if err := json.Unmarshal(payload, &state); err != nil {
		return errors.New("malformed requestState")
	}
	switch {
	case time.Now().Unix() > state.Expires:
		return errors.New("requestState expired")
	case state.Principal != expected.Principal:
		return errors.New("requestState belongs to a different caller")
	case state.Tool != expected.Tool || state.ArgsDigest != expected.ArgsDigest:
		return errors.New("requestState was issued for a different request")
	}
	return nil
}

// declaresElicitation reports whether the client declared form elicitation.
func declaresElicitation(p modernParams) bool {
	var capabilities map[string]json.RawMessage
	_ = json.Unmarshal(p.Meta[metaCapabilitiesKey], &capabilities)
	_, ok := capabilities["elicitation"]
	return ok
}

func inputRequiredResult(state string) map[string]any {
	return map[string]any{
		"resultType": "input_required",
		"inputRequests": map[string]any{
			"confirm": map[string]any{
				"method": "elicitation/create",
				"params": map[string]any{
					"mode":            "form",
					"message":         "Type any answer to finish needs_input",
					"requestedSchema": objectSchema(map[string]any{"answer": map[string]any{"type": "string"}}, "answer"),
				},
			},
		},
		"requestState": state,
	}
}

// writeNeedsInput handles both the first call and the retry.
func writeNeedsInput(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) {
	if !declaresElicitation(p) {
		writeResult(w, req.ID, completeResult(errorResult("needs_input requires a client that declares elicitation")))
		return
	}
	expected := requestState{Principal: principal(r), Tool: p.Name, ArgsDigest: argsDigest(p.Arguments)}
	if p.RequestState == "" {
		expected.Expires = time.Now().Add(stateTTL).Unix()
		log.Printf("needs_input: asking for input, principal=%s", expected.Principal)
		writeResult(w, req.ID, completeResult(inputRequiredResult(signState(expected))))
		return
	}
	if err := verifyState(p.RequestState, expected); err != nil {
		log.Printf("needs_input: rejected retry: %v", err)
		writeErrorStatus(w, http.StatusOK, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	finishNeedsInput(w, req, p, expected)
}

// finishNeedsInput reads the answer from inputResponses. A missing answer
// asks again, as the spec suggests, rather than failing.
func finishNeedsInput(w http.ResponseWriter, req rpcRequest, p modernParams, expected requestState) {
	var response struct {
		Action  string `json:"action"`
		Content struct {
			Answer string `json:"answer"`
		} `json:"content"`
	}
	if json.Unmarshal(p.InputResponses["confirm"], &response) != nil || response.Action == "" {
		expected.Expires = time.Now().Add(stateTTL).Unix()
		writeResult(w, req.ID, completeResult(inputRequiredResult(signState(expected))))
		return
	}
	if response.Action != "accept" {
		writeResult(w, req.ID, completeResult(textResult("needs_input: the user chose "+response.Action)))
		return
	}
	log.Printf("needs_input: completed, principal=%s", expected.Principal)
	result := textResult("needs_input: answer=" + response.Content.Answer)
	result["structuredContent"] = map[string]any{"answer": response.Content.Answer, "principal": expected.Principal}
	writeResult(w, req.ID, completeResult(result))
}
