package main

import (
	"encoding/json"
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Paca-AI/plugin-sdk-go/plugintest"
)

// These cover the paths reachable without an outbound GitHub API call:
// config validation and "no PR linked to this task". The GitHub-API-backed
// happy path (like other ghClient-dependent handlers in this plugin) isn't
// unit-testable outside a WASM build — see plugin_test.go's note on this.

func conditionReqWithConfig(cfg any) plugintest.ConditionRequest {
	return plugintest.ConditionRequest{Task: plugin.TaskSnapshot{ID: testTaskID}}.WithJSONConfig(cfg)
}

func actionReqWithConfig(cfg any) plugintest.ActionRequest {
	return plugintest.ActionRequest{Task: plugin.TaskSnapshot{ID: testTaskID}}.WithJSONConfig(cfg)
}

func TestConditionPRState_MissingConfig(t *testing.T) {
	tc := setupPlugin(t)
	result := tc.EvaluateCondition(automationConditionPRState, conditionReqWithConfig(map[string]string{}))
	if result.Matched {
		t.Fatal("expected Matched=false for missing config")
	}
}

func TestConditionPRState_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"project_id": testProjectID, "expected_state": "merged"}
	result := tc.EvaluateCondition(automationConditionPRState, conditionReqWithConfig(cfg))
	if result.Matched {
		t.Fatal("expected Matched=false when no PR is linked to the task")
	}
}

func TestActionMergePR_MissingProjectID(t *testing.T) {
	tc := setupPlugin(t)
	result := tc.RunAction(automationActionMergePR, actionReqWithConfig(map[string]string{}))
	if result.Applied {
		t.Fatal("expected Applied=false for missing project_id")
	}
	if result.Error == "" {
		t.Fatal("expected an error message for missing project_id")
	}
}

func TestActionMergePR_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"project_id": testProjectID}
	result := tc.RunAction(automationActionMergePR, actionReqWithConfig(cfg))
	if result.Applied {
		t.Fatal("expected Applied=false when no PR is linked to the task")
	}
	if result.Error == "" {
		t.Fatal("expected an error message when no PR is linked")
	}
}

func TestActionCommentPR_MissingBody(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"project_id": testProjectID}
	result := tc.RunAction(automationActionCommentPR, actionReqWithConfig(cfg))
	if result.Applied {
		t.Fatal("expected Applied=false for missing body")
	}
	if result.Error == "" {
		t.Fatal("expected an error message for missing body")
	}
}

func TestActionCommentPR_NoLinkedPR(t *testing.T) {
	tc := setupPlugin(t)
	cfg := map[string]string{"project_id": testProjectID, "body": "looks good"}
	result := tc.RunAction(automationActionCommentPR, actionReqWithConfig(cfg))
	if result.Applied {
		t.Fatal("expected Applied=false when no PR is linked to the task")
	}
	if result.Error == "" {
		t.Fatal("expected an error message when no PR is linked")
	}
}

func TestAutomationProjectID_Missing(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{})
	if _, err := automationProjectID(raw); err == nil {
		t.Fatal("expected an error for missing project_id")
	}
}

func TestAutomationProjectID_Present(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"project_id": testProjectID})
	got, err := automationProjectID(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != testProjectID {
		t.Fatalf("expected %s, got %s", testProjectID, got)
	}
}
