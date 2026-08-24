package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ConfigResource{}
var _ resource.ResourceWithImportState = &ConfigResource{}

func NewConfigResource() resource.Resource {
	return &ConfigResource{}
}

type ConfigResource struct {
	client *Client
}

// FallbackBlock models one router_settings.fallbacks entry. LiteLLM stores
// fallbacks as a list of single-key dicts [{"primary": ["fb1","fb2"]}] — TF
// exposes them as explicit primary + fallback_models so HCL is declarative
// rather than jsonencode'd.
type FallbackBlock struct {
	Primary        types.String `tfsdk:"primary"`
	FallbackModels types.List   `tfsdk:"fallback_models"`
}

type ConfigResourceModel struct {
	ParamName       types.String `tfsdk:"param_name"`
	CooldownTime    types.Int64  `tfsdk:"cooldown_time"`
	NumRetries      types.Int64  `tfsdk:"num_retries"`
	AllowedFails    types.Int64  `tfsdk:"allowed_fails"`
	RoutingStrategy types.String `tfsdk:"routing_strategy"`
	Fallbacks       types.List   `tfsdk:"fallbacks"`
}

// fallbackObjType is the attr.Type for a single fallback block inside the
// Fallbacks list — needed to build ObjectValue during Read.
var fallbackObjType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"primary":         types.StringType,
		"fallback_models": types.ListType{ElemType: types.StringType},
	},
}

func (r *ConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config"
}

func (r *ConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a LiteLLM config section stored as a row in the LiteLLM_Config DB table. " +
			"Currently scopes the router_settings section: cooldown_time, num_retries, allowed_fails, " +
			"routing_strategy, fallbacks — the run-time-tunable fields in the update_settings whitelist " +
			"(router.py). These live in DB and override config.yaml on router reload (add_deployment); " +
			"config.yaml keeps an emergency baseline used only if DB is unreachable. " +
			"allowed_fails_policy stays in config.yaml (not in update_settings whitelist; requires restart).",
		Attributes: map[string]schema.Attribute{
			"param_name": schema.StringAttribute{
				Description: "The LiteLLM_Config param_name (section) to manage. Currently only 'router_settings' is supported.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cooldown_time": schema.Int64Attribute{
				Description: "Seconds to cooldown a deployment after failure. DB override; upstream Retry-After header wins per-request.",
				Optional:    true,
			},
			"num_retries": schema.Int64Attribute{
				Description: "Number of retries for failed requests.",
				Optional:    true,
			},
			"allowed_fails": schema.Int64Attribute{
				Description: "Times a deployment can fail before being added to cooldown.",
				Optional:    true,
			},
			"routing_strategy": schema.StringAttribute{
				Description: "Routing strategy (simple-shuffle, least-busy, latency-based-routing, cost-based-routing, usage-based-routing-v2, lar1).",
				Optional:    true,
			},
			"fallbacks": schema.ListNestedAttribute{
				Description: "Fallback model mappings. Each entry maps a primary model to an ordered list of fallback models.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"primary": schema.StringAttribute{
							Description: "The primary model name (e.g. 'round-robin/glm-5.2').",
							Required:    true,
						},
						"fallback_models": schema.ListAttribute{
							Description: "Ordered list of fallback model names.",
							Required:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
	}
}

func (r *ConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *Client, got: %T.", req.ProviderData))
		return
	}
	r.client = client
}

// buildSectionValue converts the TF model into the section dict that
// POST /config/update expects under the param_name key. Only set fields are
// included so the server's per-key merge leaves untouched keys alone.
func (r *ConfigResource) buildSectionValue(ctx context.Context, data *ConfigResourceModel) map[string]interface{} {
	rs := map[string]interface{}{}
	if !data.CooldownTime.IsNull() {
		rs["cooldown_time"] = data.CooldownTime.ValueInt64()
	}
	if !data.NumRetries.IsNull() {
		rs["num_retries"] = data.NumRetries.ValueInt64()
	}
	if !data.AllowedFails.IsNull() {
		rs["allowed_fails"] = data.AllowedFails.ValueInt64()
	}
	if !data.RoutingStrategy.IsNull() {
		rs["routing_strategy"] = data.RoutingStrategy.ValueString()
	}
	if !data.Fallbacks.IsNull() {
		var blocks []FallbackBlock
		data.Fallbacks.ElementsAs(ctx, &blocks, false)
		fbs := make([]map[string]interface{}, 0, len(blocks))
		for _, b := range blocks {
			var models []string
			b.FallbackModels.ElementsAs(ctx, &models, false)
			fbs = append(fbs, map[string]interface{}{
				b.Primary.ValueString(): models,
			})
		}
		rs["fallbacks"] = fbs
	}
	return rs
}

// mapRouterSettingsToState reads the DB param_value (raw router_settings
// jsonb) into the TF model. Only schema-declared fields are mapped; unknown
// DB keys are ignored (belong to other tools or future fields).
func (r *ConfigResource) mapRouterSettingsToState(ctx context.Context, paramValue map[string]interface{}, data *ConfigResourceModel) error {
	if v, ok := paramValue["cooldown_time"]; ok {
		if n, ok := toInt64Value(v); ok {
			data.CooldownTime = types.Int64Value(n)
		}
	}
	if v, ok := paramValue["num_retries"]; ok {
		if n, ok := toInt64Value(v); ok {
			data.NumRetries = types.Int64Value(n)
		}
	}
	if v, ok := paramValue["allowed_fails"]; ok {
		if n, ok := toInt64Value(v); ok {
			data.AllowedFails = types.Int64Value(n)
		}
	}
	if v, ok := paramValue["routing_strategy"]; ok {
		if s, ok := v.(string); ok {
			data.RoutingStrategy = types.StringValue(s)
		}
	}
	if v, ok := paramValue["fallbacks"]; ok {
		if arr, ok := v.([]interface{}); ok {
			listVal, err := liteLLMFallbacksToList(arr)
			if err != nil {
				return err
			}
			data.Fallbacks = listVal
		}
	}
	return nil
}

func toInt64Value(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// liteLLMFallbacksToList converts LiteLLM's [{"primary": ["fb1","fb2"]}] into
// a types.List of objects matching the fallbacks schema.
func liteLLMFallbacksToList(raw []interface{}) (types.List, error) {
	elems := make([]attr.Value, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// each entry is a single-key dict: primary → [models]
		for primary, modelsRaw := range m {
			modelElems := make([]attr.Value, 0)
			if arr, ok := modelsRaw.([]interface{}); ok {
				for _, mm := range arr {
					if s, ok := mm.(string); ok {
						modelElems = append(modelElems, types.StringValue(s))
					}
				}
			}
			modelsList, d := types.ListValue(types.StringType, modelElems)
			if d.HasError() {
				return types.List{}, fmt.Errorf("build fallback_models list: %v", d.Errors())
			}
			objVal, d := types.ObjectValue(fallbackObjType.AttrTypes, map[string]attr.Value{
				"primary":         types.StringValue(primary),
				"fallback_models": modelsList,
			})
			if d.HasError() {
				return types.List{}, fmt.Errorf("build fallback object: %v", d.Errors())
			}
			elems = append(elems, objVal)
		}
	}
	listVal, d := types.ListValue(fallbackObjType, elems)
	if d.HasError() {
		return types.List{}, fmt.Errorf("build fallbacks list: %v", d.Errors())
	}
	return listVal, nil
}

func (r *ConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.ParamName.ValueString() != "router_settings" {
		resp.Diagnostics.AddError("Unsupported param_name", `litellm_config currently only supports param_name="router_settings"`)
		return
	}
	body := map[string]interface{}{
		data.ParamName.ValueString(): r.buildSectionValue(ctx, &data),
	}
	if err := r.client.DoRequestWithResponse(ctx, "POST", "/config/update", body, nil); err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to create config: %s", err))
		return
	}
	if err := r.readConfig(ctx, &data); err != nil {
		resp.Diagnostics.AddWarning("Read Error", fmt.Sprintf("Config created but failed to read back: %s", err))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.readConfig(ctx, &data); err != nil {
		if IsNotFoundError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read config: %s", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.ParamName.ValueString() != "router_settings" {
		resp.Diagnostics.AddError("Unsupported param_name", `litellm_config currently only supports param_name="router_settings"`)
		return
	}
	body := map[string]interface{}{
		data.ParamName.ValueString(): r.buildSectionValue(ctx, &data),
	}
	if err := r.client.DoRequestWithResponse(ctx, "POST", "/config/update", body, nil); err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to update config: %s", err))
		return
	}
	if err := r.readConfig(ctx, &data); err != nil {
		resp.Diagnostics.AddWarning("Read Error", fmt.Sprintf("Config updated but failed to read back: %s", err))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	endpoint := fmt.Sprintf("/config/param?param_name=%s", url.QueryEscape(data.ParamName.ValueString()))
	if err := r.client.DoRequestWithResponse(ctx, "DELETE", endpoint, nil, nil); err != nil {
		if !IsNotFoundError(err) {
			resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to delete config: %s", err))
			return
		}
	}
}

func (r *ConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	data := ConfigResourceModel{ParamName: types.StringValue(req.ID)}
	if err := r.readConfig(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Read Error", fmt.Sprintf("Unable to read config after import: %s", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) readConfig(ctx context.Context, data *ConfigResourceModel) error {
	endpoint := fmt.Sprintf("/config/get?param_name=%s", url.QueryEscape(data.ParamName.ValueString()))
	var result struct {
		ParamName  string                 `json:"param_name"`
		ParamValue map[string]interface{} `json:"param_value"`
	}
	if err := r.client.DoRequestWithResponse(ctx, "GET", endpoint, nil, &result); err != nil {
		return err
	}
	if result.ParamValue == nil {
		result.ParamValue = map[string]interface{}{}
	}
	return r.mapRouterSettingsToState(ctx, result.ParamValue, data)
}
