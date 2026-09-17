package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestReadModelResolvesUnknownOptionalComputedCollections(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "text-embedding-3-small",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/text-embedding-3-small",
					},
					"model_info": map[string]interface{}{
						"base_model": "text-embedding-3-small",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	data := ModelResourceModel{
		ID:                      types.StringValue("model-123"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapUnknown(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	if data.AccessGroups.IsUnknown() {
		t.Fatal("access_groups should be known after read")
	}
	if data.AdditionalLiteLLMParams.IsUnknown() {
		t.Fatal("additional_litellm_params should be known after read")
	}
}

func TestReadModelTeamScopedPrefersTeamPublicModelName(t *testing.T) {
	t.Parallel()

	const publicName = "gpt-4o-2024-11-20-prod-filter"
	const internalName = "model_name_GRP-DCAI-LLM-GATEWAY_a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	const teamID = "GRP-DCAI-LLM-GATEWAY"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": internalName,
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/gpt-4o",
					},
					"model_info": map[string]interface{}{
						"base_model":             "gpt-4o",
						"team_id":                teamID,
						"team_public_model_name": publicName,
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	data := ModelResourceModel{
		ID:                      types.StringValue("model-123"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapUnknown(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	if got := data.ModelName.ValueString(); got != publicName {
		t.Fatalf("expected model_name=%q (team_public_model_name), got %q", publicName, got)
	}
	if got := data.TeamID.ValueString(); got != teamID {
		t.Fatalf("expected team_id=%q, got %q", teamID, got)
	}
}

// TestReadModelResolvesUnknownModeForWildcardRouting verifies that when the
// LiteLLM API does not return a "mode" value (e.g. for wildcard routes like
// openai/*), readModel resolves the Unknown mode to Null rather than leaving
// it Unknown. Terraform requires all Computed attributes to be known (or null)
// after apply, so leaving it Unknown causes:
//
//	"provider still indicated an unknown value for litellm_model.*.mode"
func TestReadModelResolvesUnknownModeForWildcardRouting(t *testing.T) {
	t.Parallel()

	// Simulate LiteLLM returning a model with no "mode" in model_info,
	// which happens for wildcard routes (e.g. openai/*).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "openai/*",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/openai/*",
					},
					"model_info": map[string]interface{}{
						"base_model": "openai/*",
						// "mode" is intentionally absent – wildcard routes have no mode
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate the state that Terraform builds from the plan when mode is not
	// specified in config: Computed+Optional means mode is Unknown pre-apply.
	data := ModelResourceModel{
		ID:                      types.StringValue("wildcard-model-123"),
		Mode:                    types.StringUnknown(),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapUnknown(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	if data.Mode.IsUnknown() {
		t.Fatal("mode must not be Unknown after read (would cause 'provider returned unknown value after apply' error)")
	}
	// When no mode is returned by the API, the attribute should be null
	// (not an empty string – that would be a non-null known value).
	if !data.Mode.IsNull() {
		t.Fatalf("mode should be null when API returns no mode, got %q", data.Mode.ValueString())
	}
}

func TestConvertStringValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected interface{}
	}{
		{"0", int64(0)},
		{"42", int64(42)},
		{"-1", int64(-1)},
		{"3.14", float64(3.14)},
		{"true", true},
		{"false", false},
		{"hello", "hello"},
		{`["a","b"]`, []interface{}{"a", "b"}},
		{`{"key":"val"}`, map[string]interface{}{"key": "val"}},
		{"not json {", "not json {"},
	}

	for _, tt := range tests {
		got := convertStringValue(tt.input)
		gotJSON, _ := json.Marshal(got)
		expJSON, _ := json.Marshal(tt.expected)
		if string(gotJSON) != string(expJSON) {
			t.Errorf("convertStringValue(%q) = %v (%T), want %v (%T)", tt.input, got, got, tt.expected, tt.expected)
		}
	}
}

func TestCreateModelSendsAdditionalLiteLLMParams(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	additionalParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"cooldown_time":  types.StringValue("0"),
		"timeout":        types.StringValue("500"),
		"custom_flag":    types.StringValue("true"),
		"stream_timeout": types.StringValue("300"),
	})

	data := &ModelResourceModel{
		ModelName:               types.StringValue("test-model"),
		CustomLLMProvider:       types.StringValue("openai"),
		BaseModel:               types.StringValue("gpt-4o-mini"),
		Tier:                    types.StringNull(),
		Mode:                    types.StringNull(),
		AdditionalLiteLLMParams: additionalParams,
		AccessGroups:            types.ListNull(types.StringType),
	}

	err := r.createOrUpdateModel(context.Background(), data, "test-id", false)
	if err != nil {
		t.Fatalf("createOrUpdateModel returned error: %v", err)
	}

	litellmParams, ok := capturedBody["litellm_params"].(map[string]interface{})
	if !ok {
		t.Fatal("litellm_params not found in request body")
	}

	// cooldown_time should be sent as int 0
	if v, ok := litellmParams["cooldown_time"]; !ok {
		t.Fatal("cooldown_time not found in litellm_params")
	} else if v != float64(0) { // JSON numbers decode as float64
		t.Fatalf("expected cooldown_time=0, got %v (%T)", v, v)
	}

	// timeout should be sent as int 500
	if v := litellmParams["timeout"]; v != float64(500) {
		t.Fatalf("expected timeout=500, got %v (%T)", v, v)
	}

	// custom_flag should be sent as bool true
	if v := litellmParams["custom_flag"]; v != true {
		t.Fatalf("expected custom_flag=true, got %v (%T)", v, v)
	}

	// stream_timeout should be sent as int 300
	if v := litellmParams["stream_timeout"]; v != float64(300) {
		t.Fatalf("expected stream_timeout=300, got %v (%T)", v, v)
	}
}

func TestCreateModelSendsTeamPublicModelNameWhenTeamIDSet(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	const wantName = "gpt-4o-2024-11-20-prod-filter"
	const wantTeamID = "GRP-DCAI-LLM-GATEWAY"

	data := &ModelResourceModel{
		ModelName:         types.StringValue(wantName),
		CustomLLMProvider: types.StringValue("openai"),
		BaseModel:         types.StringValue("gpt-4o"),
		TeamID:            types.StringValue(wantTeamID),
		Tier:              types.StringNull(),
		Mode:              types.StringNull(),
		AccessGroups:      types.ListNull(types.StringType),
	}

	err := r.createOrUpdateModel(context.Background(), data, "test-id", false)
	if err != nil {
		t.Fatalf("createOrUpdateModel returned error: %v", err)
	}

	modelInfo, ok := capturedBody["model_info"].(map[string]interface{})
	if !ok {
		t.Fatal("model_info not found in request body")
	}
	if got := modelInfo["team_public_model_name"]; got != wantName {
		t.Fatalf("expected model_info.team_public_model_name=%q, got %v", wantName, got)
	}
	if got := modelInfo["team_id"]; got != wantTeamID {
		t.Fatalf("expected model_info.team_id=%q, got %v", wantTeamID, got)
	}
}

func TestPatchModelSendsAdditionalLiteLLMParams(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	additionalParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"cooldown_time": types.StringValue("0"),
		"max_retries":   types.StringValue("3"),
	})

	data := &ModelResourceModel{
		ID:                      types.StringValue("model-789"),
		ModelName:               types.StringValue("test-model"),
		CustomLLMProvider:       types.StringValue("openrouter"),
		BaseModel:               types.StringValue("anthropic/claude-3.7-sonnet"),
		Tier:                    types.StringNull(),
		Mode:                    types.StringNull(),
		AdditionalLiteLLMParams: additionalParams,
		AccessGroups:            types.ListNull(types.StringType),
	}

	err := r.patchModel(context.Background(), data)
	if err != nil {
		t.Fatalf("patchModel returned error: %v", err)
	}

	litellmParams, ok := capturedBody["litellm_params"].(map[string]interface{})
	if !ok {
		t.Fatal("litellm_params not found in request body")
	}

	if v := litellmParams["cooldown_time"]; v != float64(0) {
		t.Fatalf("expected cooldown_time=0, got %v (%T)", v, v)
	}
	if v := litellmParams["max_retries"]; v != float64(3) {
		t.Fatalf("expected max_retries=3, got %v (%T)", v, v)
	}
}

func TestPatchModelSendsTeamPublicModelNameWhenTeamIDSet(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	const wantName = "gpt-4o-prod-filter"
	const wantTeamID = "GRP-TEAM-1"

	data := &ModelResourceModel{
		ID:                types.StringValue("model-789"),
		ModelName:         types.StringValue(wantName),
		CustomLLMProvider: types.StringValue("openai"),
		BaseModel:         types.StringValue("gpt-4o"),
		TeamID:            types.StringValue(wantTeamID),
		Tier:              types.StringNull(),
		Mode:              types.StringNull(),
		AccessGroups:     types.ListNull(types.StringType),
	}

	err := r.patchModel(context.Background(), data)
	if err != nil {
		t.Fatalf("patchModel returned error: %v", err)
	}

	modelInfo, ok := capturedBody["model_info"].(map[string]interface{})
	if !ok {
		t.Fatal("model_info not found in request body")
	}
	if got := modelInfo["team_public_model_name"]; got != wantName {
		t.Fatalf("expected model_info.team_public_model_name=%q, got %v", wantName, got)
	}
	if got := modelInfo["team_id"]; got != wantTeamID {
		t.Fatalf("expected model_info.team_id=%q, got %v", wantTeamID, got)
	}
}

func TestReadModelExtractsAdditionalLiteLLMParams(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "gpt-4o-mini",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/gpt-4o-mini",
						"custom_flag":         true,
						"max_retries":         3.0,
					},
					"model_info": map[string]interface{}{
						"base_model":    "gpt-4o-mini",
						"access_groups": []interface{}{"team-a"},
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate state with keys the user configured — readModel only reads back
	// keys that already exist in state to avoid "new element appeared" errors.
	priorParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"custom_flag": types.StringValue(""),
		"max_retries": types.StringValue(""),
	})

	data := ModelResourceModel{
		ID:                      types.StringValue("model-456"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: priorParams,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	if got := additional["custom_flag"]; got != "true" {
		t.Fatalf("expected custom_flag=true, got %q", got)
	}
	if got := additional["max_retries"]; got != "3" {
		t.Fatalf("expected max_retries=3, got %q", got)
	}
}

func TestReadModelUnwrapsDataArray(t *testing.T) {
	t.Parallel()

	// LiteLLM /model/info API returns {"data": [{...}]}, not a flat object.
	// readModel must unwrap the data array to extract model fields.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "openrouter/anthropic/claude-3.7-sonnet",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openrouter",
						"model":               "openrouter/anthropic/claude-3.7-sonnet",
						"cooldown_time":       0,
						"timeout":             500.0,
						"stream_timeout":      500.0,
						"max_retries":         1,
					},
					"model_info": map[string]interface{}{
						"id":         "test-uuid",
						"base_model": "anthropic/claude-3.7-sonnet",
						"tier":       "paid",
						"mode":       "chat",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate state with keys the user configured
	priorParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"cooldown_time":  types.StringValue("0"),
		"timeout":        types.StringValue("500"),
		"stream_timeout": types.StringValue("500"),
		"max_retries":    types.StringValue("1"),
	})

	data := ModelResourceModel{
		ID:                      types.StringValue("test-uuid"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: priorParams,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	// Verify model_name was read
	if data.ModelName.ValueString() != "openrouter/anthropic/claude-3.7-sonnet" {
		t.Fatalf("expected model_name='openrouter/anthropic/claude-3.7-sonnet', got %q", data.ModelName.ValueString())
	}

	// Verify custom_llm_provider was read
	if data.CustomLLMProvider.ValueString() != "openrouter" {
		t.Fatalf("expected custom_llm_provider='openrouter', got %q", data.CustomLLMProvider.ValueString())
	}

	// Verify base_model was read from model_info
	if data.BaseModel.ValueString() != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("expected base_model='anthropic/claude-3.7-sonnet', got %q", data.BaseModel.ValueString())
	}

	// Verify additional_litellm_params were extracted
	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	if got := additional["cooldown_time"]; got != "0" {
		t.Fatalf("expected cooldown_time='0', got %q", got)
	}
	if got := additional["timeout"]; got != "500" {
		t.Fatalf("expected timeout='500', got %q", got)
	}
	if got := additional["max_retries"]; got != "1" {
		t.Fatalf("expected max_retries='1', got %q", got)
	}
}

func TestReadModelPassesMergeReasoningThroughAdditionalParams(t *testing.T) {
	t.Parallel()

	// merge_reasoning_content_in_choices can be passed both as a top-level attribute
	// and via additional_litellm_params. Since templates commonly use additional_litellm_params,
	// readModel should pass it through additional params (not filter it as "known").
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider":                "openrouter",
						"model":                              "openrouter/test-model",
						"merge_reasoning_content_in_choices": false,
						"use_in_pass_through":                false,
						"cooldown_time":                      0,
					},
					"model_info": map[string]interface{}{
						"base_model": "test-model",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate state with keys the user configured via additional_litellm_params
	priorParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"merge_reasoning_content_in_choices": types.StringValue("false"),
		"use_in_pass_through":                types.StringValue("false"),
		"cooldown_time":                      types.StringValue("0"),
	})

	data := ModelResourceModel{
		ID:                      types.StringValue("test-uuid"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: priorParams,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	// merge_reasoning_content_in_choices must be in additional_litellm_params
	if got, ok := additional["merge_reasoning_content_in_choices"]; !ok {
		t.Fatal("merge_reasoning_content_in_choices missing from additional_litellm_params")
	} else if got != "false" {
		t.Fatalf("expected merge_reasoning_content_in_choices='false', got %q", got)
	}

	// use_in_pass_through and cooldown_time should also be present
	if _, ok := additional["use_in_pass_through"]; !ok {
		t.Fatal("use_in_pass_through missing from additional_litellm_params")
	}
	if _, ok := additional["cooldown_time"]; !ok {
		t.Fatal("cooldown_time missing from additional_litellm_params")
	}
}

func TestNormalizeNumericString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		// Scientific notation → decimal
		{"1.75e-07", "0.000000175"},
		{"2.5e-06", "0.0000025"},
		{"1.25e-05", "0.0000125"},
		{"5e-08", "0.00000005"},
		{"4e-06", "0.000004"},
		{"6e-06", "0.000006"},
		{"1e-06", "0.000001"},
		{"1.8e-05", "0.000018"},
		{"2e-07", "0.0000002"},
		{"4e-07", "0.0000004"},
		// Already decimal — should stay the same
		{"0.000000175", "0.000000175"},
		{"0.0000025", "0.0000025"},
		{"0.0016384", "0.0016384"},
		{"3.14", "3.14"},
		// Integers — should stay the same
		{"0", "0"},
		{"42", "42"},
		{"500", "500"},
		{"-1", "-1"},
		// Non-numeric strings — unchanged
		{"hello", "hello"},
		{"true", "true"},
		{`["a"]`, `["a"]`},
	}

	for _, tt := range tests {
		got := normalizeNumericString(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeNumericString(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestReadModelNormalizesScientificNotationStrings(t *testing.T) {
	t.Parallel()

	// The API may return numeric values as JSON strings in scientific notation.
	// readModel must normalise them to decimal notation to match the user's config.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider":          "openai",
						"model":                        "openai/test-model",
						"cache_read_input_token_cost":  "1.75e-07",
						"input_cost_per_token_batches": "2.5e-06",
					},
					"model_info": map[string]interface{}{
						"base_model": "test-model",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	priorParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"cache_read_input_token_cost":  types.StringValue("0.000000175"),
		"input_cost_per_token_batches": types.StringValue("0.0000025"),
	})

	data := ModelResourceModel{
		ID:                      types.StringValue("test-id"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: priorParams,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	// Scientific notation strings should be normalised to decimal
	if got := additional["cache_read_input_token_cost"]; got != "0.000000175" {
		t.Fatalf("expected cache_read_input_token_cost='0.000000175', got %q", got)
	}
	if got := additional["input_cost_per_token_batches"]; got != "0.0000025" {
		t.Fatalf("expected input_cost_per_token_batches='0.0000025', got %q", got)
	}
}

func TestReadModelPreservesKnownParamsInAdditionalWhenUserConfigured(t *testing.T) {
	t.Parallel()

	// When a user explicitly puts input_cost_per_token in additional_litellm_params,
	// readModel must NOT filter it out (the "element has vanished" bug).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider":   "openai",
						"model":                 "openai/test-model",
						"input_cost_per_token":  0.000001,
						"output_cost_per_token": 0.000002,
						"cooldown_time":         0,
					},
					"model_info": map[string]interface{}{
						"base_model": "test-model",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// User configured these "known" params in additional_litellm_params
	priorParams, _ := types.MapValue(types.StringType, map[string]attr.Value{
		"input_cost_per_token":  types.StringValue("0.000001"),
		"output_cost_per_token": types.StringValue("0.000002"),
		"cooldown_time":         types.StringValue("0"),
	})

	data := ModelResourceModel{
		ID:                      types.StringValue("test-id"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: priorParams,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	// The "known" params should NOT have vanished
	if _, ok := additional["input_cost_per_token"]; !ok {
		t.Fatal("input_cost_per_token should NOT be filtered when user configured it in additional_litellm_params")
	}
	if _, ok := additional["output_cost_per_token"]; !ok {
		t.Fatal("output_cost_per_token should NOT be filtered when user configured it in additional_litellm_params")
	}
	if _, ok := additional["cooldown_time"]; !ok {
		t.Fatal("cooldown_time missing from additional_litellm_params")
	}
}

func TestReadModelDoesNotSetModeWhenNull(t *testing.T) {
	t.Parallel()

	// When the user didn't set mode (null), readModel must NOT populate it
	// from the API response. This prevents "was null, but now 'video_generation'" errors.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "sora-2",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "azure",
						"model":               "azure/sora-2",
					},
					"model_info": map[string]interface{}{
						"base_model": "sora-2",
						"mode":       "video_generation",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	data := ModelResourceModel{
		ID:                      types.StringValue("test-id"),
		Mode:                    types.StringNull(), // User did NOT set mode
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapNull(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	if !data.Mode.IsNull() {
		t.Fatalf("expected mode to remain null, got %q", data.Mode.ValueString())
	}
}

func TestReadModelSetsModeWhenAlreadySet(t *testing.T) {
	t.Parallel()

	// When the user set mode, readModel should update it from the API.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "sora-2",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "azure",
						"model":               "azure/sora-2",
					},
					"model_info": map[string]interface{}{
						"base_model": "sora-2",
						"mode":       "video_generation",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	data := ModelResourceModel{
		ID:                      types.StringValue("test-id"),
		Mode:                    types.StringValue("chat"), // User set mode
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapNull(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	if data.Mode.ValueString() != "video_generation" {
		t.Fatalf("expected mode='video_generation', got %q", data.Mode.ValueString())
	}
}

func TestReadModelImportReadsAllAdditionalParams(t *testing.T) {
	t.Parallel()

	// During Import, additional_litellm_params is Unknown (no prior state).
	// readModel should read ALL non-known params from the API so the imported
	// resource captures the full state.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/test-model",
						"cooldown_time":       0,
						"timeout":             500.0,
						"custom_flag":         true,
					},
					"model_info": map[string]interface{}{
						"base_model": "test-model",
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate Import: additional_litellm_params is Unknown
	data := ModelResourceModel{
		ID:                      types.StringValue("import-id"),
		AccessGroups:            types.ListUnknown(types.StringType),
		AdditionalLiteLLMParams: types.MapUnknown(types.StringType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	additional := map[string]string{}
	if diags := data.AdditionalLiteLLMParams.ElementsAs(context.Background(), &additional, false); diags.HasError() {
		t.Fatalf("failed to decode additional_litellm_params: %v", diags)
	}

	// All non-known params should be present
	if _, ok := additional["cooldown_time"]; !ok {
		t.Fatal("cooldown_time missing after import")
	}
	if _, ok := additional["timeout"]; !ok {
		t.Fatal("timeout missing after import")
	}
	if _, ok := additional["custom_flag"]; !ok {
		t.Fatal("custom_flag missing after import")
	}
}

// TestCreateModelSendsAdditionalModelInfo verifies that additional_model_info
// values of mixed types (bool, []string, number) are sent to the LiteLLM API
// as native JSON types, not stringified.
func TestCreateModelSendsAdditionalModelInfo(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Build additional_model_info with bool, []string, and number — the three
	// types that must reach the API as native JSON (not stringified).
	reasoningLevels, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("low"), types.StringValue("medium"), types.StringValue("high"),
	})
	additionalMI, _ := types.MapValue(types.DynamicType, map[string]attr.Value{
		"supports_max_reasoning_effort": types.DynamicValue(types.BoolValue(false)),
		"reasoning_effort_levels":      types.DynamicValue(reasoningLevels),
		"max_retries":                  types.DynamicValue(types.NumberValue(big.NewFloat(3))),
	})

	data := &ModelResourceModel{
		ModelName:           types.StringValue("test-model"),
		CustomLLMProvider:   types.StringValue("openai"),
		BaseModel:           types.StringValue("gpt-4o-mini"),
		Tier:                types.StringNull(),
		Mode:                types.StringNull(),
		AdditionalModelInfo: additionalMI,
		AccessGroups:        types.ListNull(types.StringType),
	}

	err := r.createOrUpdateModel(context.Background(), data, "test-id", false)
	if err != nil {
		t.Fatalf("createOrUpdateModel returned error: %v", err)
	}

	modelInfo, ok := capturedBody["model_info"].(map[string]interface{})
	if !ok {
		t.Fatal("model_info not found in request body")
	}

	// bool must be native false, not string "false"
	if v, ok := modelInfo["supports_max_reasoning_effort"]; !ok {
		t.Fatal("supports_max_reasoning_effort not found in model_info")
	} else if v != false {
		t.Fatalf("expected supports_max_reasoning_effort=false (bool), got %v (%T)", v, v)
	}

	// []string must be a native JSON array, not a JSON string
	if v, ok := modelInfo["reasoning_effort_levels"].([]interface{}); !ok {
		t.Fatalf("expected reasoning_effort_levels to be []interface{}, got %T", modelInfo["reasoning_effort_levels"])
	} else if len(v) != 3 || v[0] != "low" || v[2] != "high" {
		t.Fatalf("expected reasoning_effort_levels=[low,medium,high], got %v", v)
	}

	// number must be native JSON number, not string "3"
	if v, ok := modelInfo["max_retries"]; !ok {
		t.Fatal("max_retries not found in model_info")
	} else if v != float64(3) {
		t.Fatalf("expected max_retries=3 (number), got %v (%T)", v, v)
	}
}

// TestPatchModelSendsAdditionalModelInfo verifies the PATCH path also
// merges additional_model_info into model_info as native types.
func TestPatchModelSendsAdditionalModelInfo(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	additionalMI, _ := types.MapValue(types.DynamicType, map[string]attr.Value{
		"supports_max_reasoning_effort": types.DynamicValue(types.BoolValue(true)),
		"max_retries":                  types.DynamicValue(types.NumberValue(big.NewFloat(5))),
	})

	data := &ModelResourceModel{
		ID:                  types.StringValue("model-789"),
		ModelName:           types.StringValue("test-model"),
		CustomLLMProvider:   types.StringValue("openai"),
		BaseModel:           types.StringValue("gpt-4o-mini"),
		Tier:                types.StringNull(),
		Mode:                types.StringNull(),
		AdditionalModelInfo: additionalMI,
		AccessGroups:        types.ListNull(types.StringType),
	}

	err := r.patchModel(context.Background(), data)
	if err != nil {
		t.Fatalf("patchModel returned error: %v", err)
	}

	modelInfo, ok := capturedBody["model_info"].(map[string]interface{})
	if !ok {
		t.Fatal("model_info not found in request body")
	}

	if v := modelInfo["supports_max_reasoning_effort"]; v != true {
		t.Fatalf("expected supports_max_reasoning_effort=true (bool), got %v (%T)", v, v)
	}
	if v := modelInfo["max_retries"]; v != float64(5) {
		t.Fatalf("expected max_retries=5 (number), got %v (%T)", v, v)
	}
}

// TestReadModelExtractsAdditionalModelInfo verifies that readModel reads
// additional_model_info keys from the API response as native types.
func TestReadModelExtractsAdditionalModelInfo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/test-model",
					},
					"model_info": map[string]interface{}{
						"base_model":                 "test-model",
						"supports_max_reasoning_effort": false,
						"reasoning_effort_levels":      []interface{}{"low", "medium", "high"},
						"max_retries":                 3,
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate state with keys the user configured — readModel only reads
	// back keys that already exist in state.
	priorMI, _ := types.MapValue(types.DynamicType, map[string]attr.Value{
		"supports_max_reasoning_effort": types.DynamicValue(types.BoolNull()),
		"reasoning_effort_levels":       types.DynamicValue(types.StringNull()),
		"max_retries":                   types.DynamicValue(types.NumberNull()),
	})

	data := ModelResourceModel{
		ID:                  types.StringValue("model-456"),
		AccessGroups:        types.ListUnknown(types.StringType),
		AdditionalModelInfo: priorMI,
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	// Verify additional_model_info was populated with native-type values
	elements := data.AdditionalModelInfo.Elements()

	// bool → types.Bool
	if dv, ok := elements["supports_max_reasoning_effort"].(types.Dynamic); ok {
		if bv, ok := dv.UnderlyingValue().(types.Bool); ok {
			if bv.ValueBool() != false {
				t.Fatalf("expected supports_max_reasoning_effort=false, got %v", bv.ValueBool())
			}
		} else {
			t.Fatalf("expected types.Bool, got %T", dv.UnderlyingValue())
		}
	} else {
		t.Fatal("supports_max_reasoning_effort missing from additional_model_info")
	}

	// []string → types.List
	if dv, ok := elements["reasoning_effort_levels"].(types.Dynamic); ok {
		if lv, ok := dv.UnderlyingValue().(types.List); ok {
			if len(lv.Elements()) != 3 {
				t.Fatalf("expected 3 elements, got %d", len(lv.Elements()))
			}
			if sv, ok := lv.Elements()[0].(types.String); ok {
				if sv.ValueString() != "low" {
					t.Fatalf("expected first element 'low', got %q", sv.ValueString())
				}
			} else {
				t.Fatalf("expected types.String element, got %T", lv.Elements()[0])
			}
		} else {
			t.Fatalf("expected types.List, got %T", dv.UnderlyingValue())
		}
	} else {
		t.Fatal("reasoning_effort_levels missing from additional_model_info")
	}

	// number → types.Number
	if dv, ok := elements["max_retries"].(types.Dynamic); ok {
		if nv, ok := dv.UnderlyingValue().(types.Number); ok {
			bf := nv.ValueBigFloat()
			if i, acc := bf.Int64(); i != 3 || acc != big.Exact {
				t.Fatalf("expected max_retries=3, got %v (acc=%v)", i, acc)
			}
		} else {
			t.Fatalf("expected types.Number, got %T", dv.UnderlyingValue())
		}
	} else {
		t.Fatal("max_retries missing from additional_model_info")
	}
}

// TestReadModelImportReadsAllAdditionalModelInfo verifies that during Import
// (state is unknown), readModel reads ALL non-known model_info keys.
func TestReadModelImportReadsAllAdditionalModelInfo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"model_name": "test-model",
					"litellm_params": map[string]interface{}{
						"custom_llm_provider": "openai",
						"model":               "openai/test-model",
					},
					"model_info": map[string]interface{}{
						"base_model":                    "test-model",
						"supports_max_reasoning_effort": true,
						"max_retries":                   7,
					},
				},
			},
		})
	}))
	defer server.Close()

	r := &ModelResource{
		client: &Client{
			APIBase:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
		},
	}

	// Simulate Import: additional_model_info is Unknown
	data := ModelResourceModel{
		ID:                  types.StringValue("import-id"),
		AccessGroups:        types.ListUnknown(types.StringType),
		AdditionalModelInfo: types.MapUnknown(types.DynamicType),
	}

	if err := r.readModel(context.Background(), &data); err != nil {
		t.Fatalf("readModel returned error: %v", err)
	}

	elements := data.AdditionalModelInfo.Elements()
	if _, ok := elements["supports_max_reasoning_effort"]; !ok {
		t.Fatal("supports_max_reasoning_effort missing after import")
	}
	if _, ok := elements["max_retries"]; !ok {
		t.Fatal("max_retries missing after import")
	}
}
