package main

import (
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

// These cover the paths reachable without an outbound GitHub API call:
// config validation and "no PR linked to this task". The GitHub-API-backed
// happy path (like other ghClient-dependent handlers in this plugin) isn't
// unit-testable outside a WASM build — see plugin_test.go's note on this.
//
// ProjectID is supplied the same way the host always supplies it — as a
// top-level request field, not folded into Config — mirroring how
// pluginNodePayload builds a real automation run's request in the core.

func conditionReqWithConfig(cfg any) plugintest.ConditionRequest {
	return plugintest.ConditionRequest{
		Task:      plugin.TaskSnapshot{ID: testTaskID},
		ProjectID: testProjectID,
	}.WithJSONConfig(cfg)
}

func actionReqWithConfig(cfg any) plugintest.ActionRequest {
	return plugintest.ActionRequest{
		Task:      plugin.TaskSnapshot{ID: testTaskID},
		ProjectID: testProjectID,
	}.WithJSONConfig(cfg)
}

func TestConditionPRState_MissingConfig(t *testing.T) {
	tc := setupPlugin(t)
	result := tc.EvaluateCondition(automationConditionPRState, conditionReqWithConfig(map[string]string{}))
	if result.Matched {
		t.Fatal("expected Matched=false for missing config")
	}
}

func TestConditionPRState_MissingProjectID(t *testing.T) {
	tc := setupPlugin(t)
	req := plugintest.ConditionRequest{Task: plugin.TaskSnapshot{ID: testTaskID}}.
		WithJSONConfig(map[string]string{"expected_state": "merged"})
	result := tc.EvaluateCondition(automationConditionPRState, req)
	if result.Matched {
		t.Fatal("expected Matched=false when the host supplies no project_id")
	}
}

func TestConditionPRState_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"expected_state": "merged"}
	result := tc.EvaluateCondition(automationConditionPRState, conditionReqWithConfig(cfg))
	if result.Matched {
		t.Fatal("expected Matched=false when no PR is linked to the task")
	}
}

func TestActionMergePR_MissingProjectID(t *testing.T) {
	tc := setupPlugin(t)
	req := plugintest.ActionRequest{Task: plugin.TaskSnapshot{ID: testTaskID}}.
		WithJSONConfig(map[string]string{})
	result := tc.RunAction(automationActionMergePR, req)
	if result.Applied {
		t.Fatal("expected Applied=false when the host supplies no project_id")
	}
	if result.Error == "" {
		t.Fatal("expected an error message for missing project_id")
	}
}

func TestActionMergePR_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	result := tc.RunAction(automationActionMergePR, actionReqWithConfig(map[string]string{}))
	if result.Applied {
		t.Fatal("expected Applied=false when no PR is linked to the task")
	}
	if result.Error == "" {
		t.Fatal("expected an error message when no PR is linked")
	}
}

func TestActionCommentPR_MissingBody(t *testing.T) {
	tc := setupPlugin(t)
	result := tc.RunAction(automationActionCommentPR, actionReqWithConfig(map[string]string{}))
	if result.Applied {
		t.Fatal("expected Applied=false for missing body")
	}
	if result.Error == "" {
		t.Fatal("expected an error message for missing body")
	}
}

func TestActionCommentPR_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"body": "looks good"}
	result := tc.RunAction(automationActionCommentPR, actionReqWithConfig(cfg))
	if result.Applied {
		t.Fatal("expected Applied=false when no PR is linked to the task")
	}
	if result.Error == "" {
		t.Fatal("expected an error message when no PR is linked")
	}
}
