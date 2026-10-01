/* eslint-disable */
// @ts-nocheck
/**
 * GENERATED FILE: do not edit. The policy.v1 schema (contracts/schema/policy.v1.schema.json)
 * compiled by ajv standalone (src/lib/policy/generateValidator.ts). Regenerate with
 * `make gen-policy-validator`; policyValidator.gen.test.ts fails when this file is stale.
 */

function ucs2length(str) {
  const len = str.length;
  let length = 0;
  let pos = 0;
  let value;
  while (pos < len) {
    length++;
    value = str.charCodeAt(pos++);
    if (value >= 0xd800 && value <= 0xdbff && pos < len) {
      value = str.charCodeAt(pos);
      if ((value & 0xfc00) === 0xdc00) pos++;
    }
  }
  return length;
}

function equal(a, b) {
  if (a === b) return true;
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    if (Array.isArray(a) !== Array.isArray(b)) return false;
    if (Array.isArray(a)) return a.length === b.length && a.every((v, i) => equal(v, b[i]));
    const keys = Object.keys(a);
    if (keys.length !== Object.keys(b).length) return false;
    return keys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && equal(a[k], b[k]));
  }
  return a !== a && b !== b;
}

"use strict";
export const validate = validate20;
export default validate20;
const schema31 = {"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://github.com/timkush1/TrustAgent/contracts/schema/policy.v1.schema.json","title":"PolicyDoc","description":"config/policies.yaml, schema policy/v1 (SPEC §2.7). Parsed with yaml.v3 KnownFields(true). routes[] merge over defaults: nested objects merge key by key, arrays replace. The first matching route wins; every matcher given in a route must match; with no match, defaults apply as route \"default\". Cross-field rules that need the merged route (hedge_after_ms < deadline_ms, unique evaluator ids, param names unique across the route and declared by the evaluator, escalate_from < warn_at <= critical_at, evidence_sources a subset of evidence.sources, knowledge only with the add-on) are enforced by the Go loader.","type":"object","additionalProperties":false,"required":["version"],"properties":{"version":{"const":1},"defaults":{"$ref":"#/$defs/RouteSettings"},"overrides":{"$ref":"#/$defs/Overrides"},"routes":{"type":"array","maxItems":64,"items":{"$ref":"#/$defs/Route"}}},"$defs":{"Overrides":{"title":"PolicyOverrides","type":"object","additionalProperties":false,"properties":{"client_mode":{"enum":["none","stricter_only","any"],"default":"none","description":"Whether the X-TrustAgent-Mode request header may change the mode. stricter_only allows observe -> flag -> block/fallback only."},"client_rules":{"type":"boolean","default":false,"description":"Honour the request body trustagent.rules object. Off by default: end users must not rewrite rules."}}},"Route":{"title":"PolicyRoute","description":"A route: name, match and any RouteSettings field (merged over defaults).","type":"object","additionalProperties":false,"required":["name","match"],"properties":{"name":{"allOf":[{"$ref":"common.v1.schema.json#/$defs/RouteName"},{"not":{"const":"default"}}],"description":"\"default\" is reserved for the defaults route."},"description":{"type":"string","maxLength":600},"match":{"$ref":"#/$defs/RouteMatch"},"mode":{"$ref":"#/$defs/RouteSettings/properties/mode"},"fail":{"$ref":"#/$defs/RouteSettings/properties/fail"},"deadline_ms":{"$ref":"#/$defs/RouteSettings/properties/deadline_ms"},"hedge_after_ms":{"$ref":"#/$defs/RouteSettings/properties/hedge_after_ms"},"sample_rate":{"$ref":"#/$defs/RouteSettings/properties/sample_rate"},"require_calibration":{"$ref":"#/$defs/RouteSettings/properties/require_calibration"},"on_no_evidence":{"$ref":"#/$defs/RouteSettings/properties/on_no_evidence"},"streaming":{"$ref":"#/$defs/RouteSettings/properties/streaming"},"stream_annotations":{"$ref":"#/$defs/RouteSettings/properties/stream_annotations"},"buffer_keepalive_s":{"$ref":"#/$defs/RouteSettings/properties/buffer_keepalive_s"},"headers":{"$ref":"#/$defs/RouteSettings/properties/headers"},"evidence":{"$ref":"#/$defs/RouteSettings/properties/evidence"},"evaluators":{"$ref":"#/$defs/RouteSettings/properties/evaluators"},"detectors":{"$ref":"#/$defs/RouteSettings/properties/detectors"},"decision":{"$ref":"#/$defs/RouteSettings/properties/decision"},"deep_audit":{"$ref":"#/$defs/RouteSettings/properties/deep_audit"},"block":{"$ref":"#/$defs/RouteSettings/properties/block"},"fallback":{"$ref":"#/$defs/RouteSettings/properties/fallback"},"store_bodies":{"$ref":"#/$defs/RouteSettings/properties/store_bodies"},"body_sample_rate_pass":{"$ref":"#/$defs/RouteSettings/properties/body_sample_rate_pass"}}},"RouteMatch":{"title":"RouteMatch","type":"object","additionalProperties":false,"description":"Every matcher given must match.","properties":{"headers":{"type":"object","maxProperties":16,"propertyNames":{"pattern":"^[A-Za-z0-9-]{1,64}$"},"additionalProperties":{"type":"string","maxLength":256},"description":"Header name (case-insensitive) -> exact value, e.g. {X-TrustAgent-Route: support-bot}."},"paths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","pattern":"^/","maxLength":256},"description":"Request path globs."},"models":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256},"description":"Model name globs."},"key_labels":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64},"description":"API key labels."}}},"RouteSettings":{"title":"RouteSettings","description":"The fields shared by defaults and routes (SPEC §2.7 RouteSettings). Every field is optional in a route and merged over defaults.","type":"object","additionalProperties":false,"properties":{"mode":{"$ref":"common.v1.schema.json#/$defs/PolicyMode","description":"Default comes from the TRUTHTABLE_GATE_DEFAULT_MODE seed (observe)."},"fail":{"$ref":"common.v1.schema.json#/$defs/FailMode","default":"open","description":"\"closed\" only matters in block and fallback modes."},"deadline_ms":{"type":"integer","minimum":100,"maximum":5000,"default":600,"description":"Synchronous gate budget, excluding the upstream call."},"hedge_after_ms":{"type":"integer","minimum":0,"maximum":4999,"not":{"minimum":1,"maximum":99},"default":0,"description":"0 (no hedge) or [100, deadline_ms)."},"sample_rate":{"type":"number","minimum":0,"maximum":1,"default":1,"description":"Observe routes only: share of requests sent to the fast check. Detectors always run."},"require_calibration":{"type":"boolean","default":true,"description":"Caps block/fallback at flag unless every block-set evaluator is calibrated."},"on_no_evidence":{"enum":["ignore","flag","block"],"default":"ignore","description":"SPEC §2.9 no-evidence rule."},"streaming":{"enum":["passthrough","buffer"],"default":"passthrough","description":"Block/fallback with passthrough become effective flag (stream_not_buffered)."},"stream_annotations":{"enum":["comment","none"],"default":"comment","description":"Write \": trustagent {...}\" before the terminal SSE event."},"buffer_keepalive_s":{"type":"integer","minimum":0,"maximum":60,"default":15,"description":"Buffer mode SSE keepalive interval; 0 = none."},"headers":{"enum":["full","action_only"],"default":"full","description":"action_only sends only Trace-Id, Request-ID and Action."},"evidence":{"$ref":"#/$defs/EvidenceSettings"},"evaluators":{"type":"array","maxItems":16,"items":{"$ref":"#/$defs/RouteEvaluatorRef"},"default":[{"ref":"groundedness@1"},{"ref":"contradiction@1"},{"ref":"refusal@1"},{"ref":"pii_secret_leak@1"}],"description":"Replaces (does not merge) the defaults list. Default: question set gateway-default. Unique ids; param names unique across the route."},"detectors":{"$ref":"#/$defs/DetectorSettings"},"decision":{"$ref":"#/$defs/DecisionSettings"},"deep_audit":{"$ref":"#/$defs/DeepAuditSettings"},"block":{"$ref":"#/$defs/BlockSettings"},"fallback":{"$ref":"#/$defs/FallbackSettings"},"store_bodies":{"enum":["all","sampled","none"],"default":"sampled"},"body_sample_rate_pass":{"type":"number","minimum":0,"maximum":1,"default":0.2,"description":"With store_bodies: sampled, the share of passed traces whose bodies are kept."}}},"EvidenceSettings":{"title":"EvidenceSettings","type":"object","additionalProperties":false,"properties":{"sources":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/EvidenceSource"},"default":["request_system","request_context","tool_results","extension"],"description":"Priority order. knowledge needs the knowledge add-on."},"system_mode":{"enum":["off","documents_only","whole"],"default":"documents_only"},"line_headers":{"type":"boolean","default":true,"description":"Treat \"Context:\" style line headers as block starts (SPEC §2.10)."},"tag_patterns":{"type":"array","maxItems":16,"default":[],"items":{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1,"maxLength":256,"description":"RE2 syntax, no inline flags."},"flags":{"type":"string","pattern":"^[ims]{0,3}$","default":"","description":"Subset of \"ims\"."}}}},"max_tokens":{"type":"integer","minimum":500,"maximum":20000,"default":8000},"untrusted_tools":{"type":"array","maxItems":32,"uniqueItems":true,"default":[],"items":{"type":"string","minLength":1,"maxLength":128},"description":"Tool-name globs excluded from evidence and marked untrusted."},"knowledge":{"type":"object","additionalProperties":false,"default":{"when":"fallback","top_k":5,"deadline_ms":150,"min_score":0.3},"properties":{"when":{"enum":["fallback","always"],"default":"fallback","description":"fallback: only when the request sources are empty."},"top_k":{"type":"integer","minimum":1,"maximum":50,"default":5},"deadline_ms":{"type":"integer","minimum":1,"maximum":5000,"default":150},"min_score":{"type":"number","minimum":0,"maximum":1,"default":0.3}}}}},"DetectorSettings":{"title":"DetectorSettings","type":"object","additionalProperties":false,"properties":{"flag_on":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/DetectorId"},"default":["urls_not_in_evidence","quotes_not_in_evidence","citation_out_of_range","secret_pattern","system_prompt_leak"],"description":"Numbers and dates never flag alone."},"block_on":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/DetectorId"},"default":[]},"min_integer":{"type":"integer","minimum":0,"default":10,"description":"Integers above min_integer are always checked; unit-adjacent integers are always checked."}}},"DecisionSettings":{"title":"DecisionSettings","type":"object","additionalProperties":false,"properties":{"model":{"type":"string","minLength":1,"maxLength":128,"description":"Per-route model pin. Default: TRUTHTABLE_JEV_MODEL."},"max_response_tokens":{"type":"integer","minimum":500,"maximum":12000,"default":6000}}},"DeepAuditSettings":{"title":"DeepAuditSettings","type":"object","additionalProperties":false,"default":{"enabled":true,"pass_sample_rate":0.02,"uniform_sample_rate":0.01,"error_sample_rate":0.1,"verifier":"auto"},"properties":{"enabled":{"type":"boolean","default":true},"pass_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.02,"description":"Share of passes sent to the deep audit (lane \"sample\")."},"uniform_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.01,"description":"Pre-gate Bernoulli over all gated traces; the unbiased hallucination rate (capped by TRUTHTABLE_UNIFORM_AUDITS_PER_DAY)."},"error_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.1,"description":"Share of skipped/timeout/error gates sent to the deep audit (lane \"sample_error\")."},"verifier":{"enum":["auto","llm"],"default":"auto","description":"auto = llm in 2.0."}}},"BlockSettings":{"title":"BlockSettings","type":"object","additionalProperties":false,"properties":{"style":{"enum":["message","error"],"default":"message","description":"message: HTTP 200 with finish_reason content_filter; error: HTTP block.status."},"message":{"type":"string","minLength":1,"maxLength":2000,"default":"I can't provide a verified answer to that right now."},"status":{"enum":[400,403,409,422,451],"default":422,"description":"Error style only; never 429 or 5xx."},"anthropic_stop_reason":{"enum":["refusal","end_turn"],"default":"refusal"}}},"FallbackSettings":{"title":"FallbackSettings","type":"object","additionalProperties":false,"properties":{"upstream":{"enum":["same","fallback"],"default":"same","description":"Never a URL (prevents SSRF and key exfiltration)."},"model":{"type":"string","maxLength":128,"default":"","description":"Empty = the original model."},"system_prompt":{"type":"string","minLength":1,"maxLength":4000,"default":"Answer using only the information in CONTEXT. If CONTEXT does not contain the answer, say you don't have that information. Do not add facts, numbers, dates, names or links that are not in CONTEXT."},"on_fail":{"enum":["block","flag","original"],"default":"block"}}},"RouteEvaluatorRef":{"title":"RouteEvaluatorRef","type":"object","additionalProperties":false,"required":["ref"],"properties":{"ref":{"$ref":"common.v1.schema.json#/$defs/EvaluatorRef"},"stage":{"$ref":"common.v1.schema.json#/$defs/EvaluatorStage","default":"active","description":"YAML default active; the API and Studio default to shadow. Shadow evaluators are asked but never enforced."},"params":{"type":"object","maxProperties":8,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{1,31}$"},"additionalProperties":{"type":"string","maxLength":2000}},"thresholds":{"type":"object","additionalProperties":false,"properties":{"space":{"$ref":"common.v1.schema.json#/$defs/ThresholdSpace","default":"raw","description":"raw until a calibration is adopted, so activating a calibration never silently changes decisions."},"warn_at":{"type":"number","minimum":0,"maximum":1},"critical_at":{"anyOf":[{"type":"number","minimum":0,"maximum":1},{"type":"null"}]},"escalate_from":{"anyOf":[{"type":"number","minimum":0,"maximum":1},{"type":"null"}]}}},"enabled":{"type":"boolean","default":true},"evidence_sources":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/EvidenceSource"},"description":"Narrows (never widens) the route evidence.sources."}}}}};
const schema50 = {"title":"PolicyOverrides","type":"object","additionalProperties":false,"properties":{"client_mode":{"enum":["none","stricter_only","any"],"default":"none","description":"Whether the X-TrustAgent-Mode request header may change the mode. stricter_only allows observe -> flag -> block/fallback only."},"client_rules":{"type":"boolean","default":false,"description":"Honour the request body trustagent.rules object. Off by default: end users must not rewrite rules."}}};
const schema32 = {"title":"RouteSettings","description":"The fields shared by defaults and routes (SPEC §2.7 RouteSettings). Every field is optional in a route and merged over defaults.","type":"object","additionalProperties":false,"properties":{"mode":{"$ref":"common.v1.schema.json#/$defs/PolicyMode","description":"Default comes from the TRUTHTABLE_GATE_DEFAULT_MODE seed (observe)."},"fail":{"$ref":"common.v1.schema.json#/$defs/FailMode","default":"open","description":"\"closed\" only matters in block and fallback modes."},"deadline_ms":{"type":"integer","minimum":100,"maximum":5000,"default":600,"description":"Synchronous gate budget, excluding the upstream call."},"hedge_after_ms":{"type":"integer","minimum":0,"maximum":4999,"not":{"minimum":1,"maximum":99},"default":0,"description":"0 (no hedge) or [100, deadline_ms)."},"sample_rate":{"type":"number","minimum":0,"maximum":1,"default":1,"description":"Observe routes only: share of requests sent to the fast check. Detectors always run."},"require_calibration":{"type":"boolean","default":true,"description":"Caps block/fallback at flag unless every block-set evaluator is calibrated."},"on_no_evidence":{"enum":["ignore","flag","block"],"default":"ignore","description":"SPEC §2.9 no-evidence rule."},"streaming":{"enum":["passthrough","buffer"],"default":"passthrough","description":"Block/fallback with passthrough become effective flag (stream_not_buffered)."},"stream_annotations":{"enum":["comment","none"],"default":"comment","description":"Write \": trustagent {...}\" before the terminal SSE event."},"buffer_keepalive_s":{"type":"integer","minimum":0,"maximum":60,"default":15,"description":"Buffer mode SSE keepalive interval; 0 = none."},"headers":{"enum":["full","action_only"],"default":"full","description":"action_only sends only Trace-Id, Request-ID and Action."},"evidence":{"$ref":"#/$defs/EvidenceSettings"},"evaluators":{"type":"array","maxItems":16,"items":{"$ref":"#/$defs/RouteEvaluatorRef"},"default":[{"ref":"groundedness@1"},{"ref":"contradiction@1"},{"ref":"refusal@1"},{"ref":"pii_secret_leak@1"}],"description":"Replaces (does not merge) the defaults list. Default: question set gateway-default. Unique ids; param names unique across the route."},"detectors":{"$ref":"#/$defs/DetectorSettings"},"decision":{"$ref":"#/$defs/DecisionSettings"},"deep_audit":{"$ref":"#/$defs/DeepAuditSettings"},"block":{"$ref":"#/$defs/BlockSettings"},"fallback":{"$ref":"#/$defs/FallbackSettings"},"store_bodies":{"enum":["all","sampled","none"],"default":"sampled"},"body_sample_rate_pass":{"type":"number","minimum":0,"maximum":1,"default":0.2,"description":"With store_bodies: sampled, the share of passed traces whose bodies are kept."}}};
const schema34 = {"enum":["observe","flag","block","fallback"]};
const schema35 = {"enum":["open","closed"]};
const schema46 = {"title":"DecisionSettings","type":"object","additionalProperties":false,"properties":{"model":{"type":"string","minLength":1,"maxLength":128,"description":"Per-route model pin. Default: TRUTHTABLE_JEV_MODEL."},"max_response_tokens":{"type":"integer","minimum":500,"maximum":12000,"default":6000}}};
const schema47 = {"title":"DeepAuditSettings","type":"object","additionalProperties":false,"default":{"enabled":true,"pass_sample_rate":0.02,"uniform_sample_rate":0.01,"error_sample_rate":0.1,"verifier":"auto"},"properties":{"enabled":{"type":"boolean","default":true},"pass_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.02,"description":"Share of passes sent to the deep audit (lane \"sample\")."},"uniform_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.01,"description":"Pre-gate Bernoulli over all gated traces; the unbiased hallucination rate (capped by TRUTHTABLE_UNIFORM_AUDITS_PER_DAY)."},"error_sample_rate":{"type":"number","minimum":0,"maximum":1,"default":0.1,"description":"Share of skipped/timeout/error gates sent to the deep audit (lane \"sample_error\")."},"verifier":{"enum":["auto","llm"],"default":"auto","description":"auto = llm in 2.0."}}};
const schema48 = {"title":"BlockSettings","type":"object","additionalProperties":false,"properties":{"style":{"enum":["message","error"],"default":"message","description":"message: HTTP 200 with finish_reason content_filter; error: HTTP block.status."},"message":{"type":"string","minLength":1,"maxLength":2000,"default":"I can't provide a verified answer to that right now."},"status":{"enum":[400,403,409,422,451],"default":422,"description":"Error style only; never 429 or 5xx."},"anthropic_stop_reason":{"enum":["refusal","end_turn"],"default":"refusal"}}};
const schema49 = {"title":"FallbackSettings","type":"object","additionalProperties":false,"properties":{"upstream":{"enum":["same","fallback"],"default":"same","description":"Never a URL (prevents SSRF and key exfiltration)."},"model":{"type":"string","maxLength":128,"default":"","description":"Empty = the original model."},"system_prompt":{"type":"string","minLength":1,"maxLength":4000,"default":"Answer using only the information in CONTEXT. If CONTEXT does not contain the answer, say you don't have that information. Do not add facts, numbers, dates, names or links that are not in CONTEXT."},"on_fail":{"enum":["block","flag","original"],"default":"block"}}};
const func1 = Object.prototype.hasOwnProperty;
const func3 = ucs2length;
const schema36 = {"title":"EvidenceSettings","type":"object","additionalProperties":false,"properties":{"sources":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/EvidenceSource"},"default":["request_system","request_context","tool_results","extension"],"description":"Priority order. knowledge needs the knowledge add-on."},"system_mode":{"enum":["off","documents_only","whole"],"default":"documents_only"},"line_headers":{"type":"boolean","default":true,"description":"Treat \"Context:\" style line headers as block starts (SPEC §2.10)."},"tag_patterns":{"type":"array","maxItems":16,"default":[],"items":{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1,"maxLength":256,"description":"RE2 syntax, no inline flags."},"flags":{"type":"string","pattern":"^[ims]{0,3}$","default":"","description":"Subset of \"ims\"."}}}},"max_tokens":{"type":"integer","minimum":500,"maximum":20000,"default":8000},"untrusted_tools":{"type":"array","maxItems":32,"uniqueItems":true,"default":[],"items":{"type":"string","minLength":1,"maxLength":128},"description":"Tool-name globs excluded from evidence and marked untrusted."},"knowledge":{"type":"object","additionalProperties":false,"default":{"when":"fallback","top_k":5,"deadline_ms":150,"min_score":0.3},"properties":{"when":{"enum":["fallback","always"],"default":"fallback","description":"fallback: only when the request sources are empty."},"top_k":{"type":"integer","minimum":1,"maximum":50,"default":5},"deadline_ms":{"type":"integer","minimum":1,"maximum":5000,"default":150},"min_score":{"type":"number","minimum":0,"maximum":1,"default":0.3}}}}};
const schema37 = {"enum":["request_system","request_context","tool_results","extension","knowledge"]};
const func0 = equal;
const pattern4 = new RegExp("^[ims]{0,3}$", "u");

function validate23(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate23.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
for(const key0 in data){
if(!(((((((key0 === "sources") || (key0 === "system_mode")) || (key0 === "line_headers")) || (key0 === "tag_patterns")) || (key0 === "max_tokens")) || (key0 === "untrusted_tools")) || (key0 === "knowledge"))){
const err0 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
if(data.sources !== undefined){
let data0 = data.sources;
if(Array.isArray(data0)){
const len0 = data0.length;
for(let i0=0; i0<len0; i0++){
let data1 = data0[i0];
if(!(((((data1 === "request_system") || (data1 === "request_context")) || (data1 === "tool_results")) || (data1 === "extension")) || (data1 === "knowledge"))){
const err1 = {instancePath:instancePath+"/sources/" + i0,schemaPath:"common.v1.schema.json#/$defs/EvidenceSource/enum",keyword:"enum",params:{allowedValues: schema37.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
let i1 = data0.length;
let j0;
if(i1 > 1){
outer0:
for(;i1--;){
for(j0 = i1; j0--;){
if(func0(data0[i1], data0[j0])){
const err2 = {instancePath:instancePath+"/sources",schemaPath:"#/properties/sources/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err3 = {instancePath:instancePath+"/sources",schemaPath:"#/properties/sources/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.system_mode !== undefined){
let data2 = data.system_mode;
if(!(((data2 === "off") || (data2 === "documents_only")) || (data2 === "whole"))){
const err4 = {instancePath:instancePath+"/system_mode",schemaPath:"#/properties/system_mode/enum",keyword:"enum",params:{allowedValues: schema36.properties.system_mode.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
if(data.line_headers !== undefined){
if(typeof data.line_headers !== "boolean"){
const err5 = {instancePath:instancePath+"/line_headers",schemaPath:"#/properties/line_headers/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
}
if(data.tag_patterns !== undefined){
let data4 = data.tag_patterns;
if(Array.isArray(data4)){
if(data4.length > 16){
const err6 = {instancePath:instancePath+"/tag_patterns",schemaPath:"#/properties/tag_patterns/maxItems",keyword:"maxItems",params:{limit: 16},message:"must NOT have more than 16 items"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
const len1 = data4.length;
for(let i2=0; i2<len1; i2++){
let data5 = data4[i2];
if(data5 && typeof data5 == "object" && !Array.isArray(data5)){
if(data5.pattern === undefined){
const err7 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/required",keyword:"required",params:{missingProperty: "pattern"},message:"must have required property '"+"pattern"+"'"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
for(const key1 in data5){
if(!((key1 === "pattern") || (key1 === "flags"))){
const err8 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
if(data5.pattern !== undefined){
let data6 = data5.pattern;
if(typeof data6 === "string"){
if(func3(data6) > 256){
const err9 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/maxLength",keyword:"maxLength",params:{limit: 256},message:"must NOT have more than 256 characters"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
if(func3(data6) < 1){
const err10 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
}
else {
const err11 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
}
if(data5.flags !== undefined){
let data7 = data5.flags;
if(typeof data7 === "string"){
if(!pattern4.test(data7)){
const err12 = {instancePath:instancePath+"/tag_patterns/" + i2+"/flags",schemaPath:"#/properties/tag_patterns/items/properties/flags/pattern",keyword:"pattern",params:{pattern: "^[ims]{0,3}$"},message:"must match pattern \""+"^[ims]{0,3}$"+"\""};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
else {
const err13 = {instancePath:instancePath+"/tag_patterns/" + i2+"/flags",schemaPath:"#/properties/tag_patterns/items/properties/flags/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
}
}
else {
const err14 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
}
else {
const err15 = {instancePath:instancePath+"/tag_patterns",schemaPath:"#/properties/tag_patterns/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
}
if(data.max_tokens !== undefined){
let data8 = data.max_tokens;
if(!(((typeof data8 == "number") && (!(data8 % 1) && !isNaN(data8))) && (isFinite(data8)))){
const err16 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
if((typeof data8 == "number") && (isFinite(data8))){
if(data8 > 20000 || isNaN(data8)){
const err17 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/maximum",keyword:"maximum",params:{comparison: "<=", limit: 20000},message:"must be <= 20000"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
if(data8 < 500 || isNaN(data8)){
const err18 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/minimum",keyword:"minimum",params:{comparison: ">=", limit: 500},message:"must be >= 500"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
}
}
if(data.untrusted_tools !== undefined){
let data9 = data.untrusted_tools;
if(Array.isArray(data9)){
if(data9.length > 32){
const err19 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/maxItems",keyword:"maxItems",params:{limit: 32},message:"must NOT have more than 32 items"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
const len2 = data9.length;
for(let i3=0; i3<len2; i3++){
let data10 = data9[i3];
if(typeof data10 === "string"){
if(func3(data10) > 128){
const err20 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
if(func3(data10) < 1){
const err21 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
}
else {
const err22 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
}
let i4 = data9.length;
let j1;
if(i4 > 1){
const indices0 = {};
for(;i4--;){
let item0 = data9[i4];
if(typeof item0 !== "string"){
continue;
}
if(typeof indices0[item0] == "number"){
j1 = indices0[item0];
const err23 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/uniqueItems",keyword:"uniqueItems",params:{i: i4, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i4+" are identical)"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
break;
}
indices0[item0] = i4;
}
}
}
else {
const err24 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
}
if(data.knowledge !== undefined){
let data11 = data.knowledge;
if(data11 && typeof data11 == "object" && !Array.isArray(data11)){
for(const key2 in data11){
if(!((((key2 === "when") || (key2 === "top_k")) || (key2 === "deadline_ms")) || (key2 === "min_score"))){
const err25 = {instancePath:instancePath+"/knowledge",schemaPath:"#/properties/knowledge/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key2},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
}
if(data11.when !== undefined){
let data12 = data11.when;
if(!((data12 === "fallback") || (data12 === "always"))){
const err26 = {instancePath:instancePath+"/knowledge/when",schemaPath:"#/properties/knowledge/properties/when/enum",keyword:"enum",params:{allowedValues: schema36.properties.knowledge.properties.when.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
if(data11.top_k !== undefined){
let data13 = data11.top_k;
if(!(((typeof data13 == "number") && (!(data13 % 1) && !isNaN(data13))) && (isFinite(data13)))){
const err27 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
if((typeof data13 == "number") && (isFinite(data13))){
if(data13 > 50 || isNaN(data13)){
const err28 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/maximum",keyword:"maximum",params:{comparison: "<=", limit: 50},message:"must be <= 50"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
if(data13 < 1 || isNaN(data13)){
const err29 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/minimum",keyword:"minimum",params:{comparison: ">=", limit: 1},message:"must be >= 1"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
}
}
if(data11.deadline_ms !== undefined){
let data14 = data11.deadline_ms;
if(!(((typeof data14 == "number") && (!(data14 % 1) && !isNaN(data14))) && (isFinite(data14)))){
const err30 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
if((typeof data14 == "number") && (isFinite(data14))){
if(data14 > 5000 || isNaN(data14)){
const err31 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 5000},message:"must be <= 5000"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
if(data14 < 1 || isNaN(data14)){
const err32 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 1},message:"must be >= 1"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
}
}
if(data11.min_score !== undefined){
let data15 = data11.min_score;
if((typeof data15 == "number") && (isFinite(data15))){
if(data15 > 1 || isNaN(data15)){
const err33 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
if(data15 < 0 || isNaN(data15)){
const err34 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
else {
const err35 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
}
}
else {
const err36 = {instancePath:instancePath+"/knowledge",schemaPath:"#/properties/knowledge/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
}
else {
const err37 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
validate23.errors = vErrors;
return errors === 0;
}
validate23.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};

const schema38 = {"title":"RouteEvaluatorRef","type":"object","additionalProperties":false,"required":["ref"],"properties":{"ref":{"$ref":"common.v1.schema.json#/$defs/EvaluatorRef"},"stage":{"$ref":"common.v1.schema.json#/$defs/EvaluatorStage","default":"active","description":"YAML default active; the API and Studio default to shadow. Shadow evaluators are asked but never enforced."},"params":{"type":"object","maxProperties":8,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{1,31}$"},"additionalProperties":{"type":"string","maxLength":2000}},"thresholds":{"type":"object","additionalProperties":false,"properties":{"space":{"$ref":"common.v1.schema.json#/$defs/ThresholdSpace","default":"raw","description":"raw until a calibration is adopted, so activating a calibration never silently changes decisions."},"warn_at":{"type":"number","minimum":0,"maximum":1},"critical_at":{"anyOf":[{"type":"number","minimum":0,"maximum":1},{"type":"null"}]},"escalate_from":{"anyOf":[{"type":"number","minimum":0,"maximum":1},{"type":"null"}]}}},"enabled":{"type":"boolean","default":true},"evidence_sources":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/EvidenceSource"},"description":"Narrows (never widens) the route evidence.sources."}}};
const schema39 = {"type":"string","pattern":"^[a-z][a-z0-9_]{1,47}@[0-9]+$","description":"\"<id>@<version>\". \"@latest\" is forbidden (SPEC §2.7)."};
const schema40 = {"enum":["active","shadow"]};
const schema41 = {"enum":["raw","calibrated"]};
const pattern5 = new RegExp("^[a-z][a-z0-9_]{1,47}@[0-9]+$", "u");
const pattern6 = new RegExp("^[a-z][a-z0-9_]{1,31}$", "u");

function validate25(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate25.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.ref === undefined){
const err0 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "ref"},message:"must have required property '"+"ref"+"'"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
for(const key0 in data){
if(!((((((key0 === "ref") || (key0 === "stage")) || (key0 === "params")) || (key0 === "thresholds")) || (key0 === "enabled")) || (key0 === "evidence_sources"))){
const err1 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
if(data.ref !== undefined){
let data0 = data.ref;
if(typeof data0 === "string"){
if(!pattern5.test(data0)){
const err2 = {instancePath:instancePath+"/ref",schemaPath:"common.v1.schema.json#/$defs/EvaluatorRef/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,47}@[0-9]+$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,47}@[0-9]+$"+"\""};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
}
else {
const err3 = {instancePath:instancePath+"/ref",schemaPath:"common.v1.schema.json#/$defs/EvaluatorRef/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.stage !== undefined){
let data1 = data.stage;
if(!((data1 === "active") || (data1 === "shadow"))){
const err4 = {instancePath:instancePath+"/stage",schemaPath:"common.v1.schema.json#/$defs/EvaluatorStage/enum",keyword:"enum",params:{allowedValues: schema40.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
if(data.params !== undefined){
let data2 = data.params;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
if(Object.keys(data2).length > 8){
const err5 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/maxProperties",keyword:"maxProperties",params:{limit: 8},message:"must NOT have more than 8 properties"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
for(const key1 in data2){
const _errs9 = errors;
if(typeof key1 === "string"){
if(!pattern6.test(key1)){
const err6 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,31}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,31}$"+"\"",propertyName:key1};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
var valid3 = _errs9 === errors;
if(!valid3){
const err7 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/propertyNames",keyword:"propertyNames",params:{propertyName: key1},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
for(const key2 in data2){
let data3 = data2[key2];
if(typeof data3 === "string"){
if(func3(data3) > 2000){
const err8 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/params/additionalProperties/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
else {
const err9 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/params/additionalProperties/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
}
}
else {
const err10 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
}
if(data.thresholds !== undefined){
let data4 = data.thresholds;
if(data4 && typeof data4 == "object" && !Array.isArray(data4)){
for(const key3 in data4){
if(!((((key3 === "space") || (key3 === "warn_at")) || (key3 === "critical_at")) || (key3 === "escalate_from"))){
const err11 = {instancePath:instancePath+"/thresholds",schemaPath:"#/properties/thresholds/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key3},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
}
if(data4.space !== undefined){
let data5 = data4.space;
if(!((data5 === "raw") || (data5 === "calibrated"))){
const err12 = {instancePath:instancePath+"/thresholds/space",schemaPath:"common.v1.schema.json#/$defs/ThresholdSpace/enum",keyword:"enum",params:{allowedValues: schema41.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
if(data4.warn_at !== undefined){
let data6 = data4.warn_at;
if((typeof data6 == "number") && (isFinite(data6))){
if(data6 > 1 || isNaN(data6)){
const err13 = {instancePath:instancePath+"/thresholds/warn_at",schemaPath:"#/properties/thresholds/properties/warn_at/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
if(data6 < 0 || isNaN(data6)){
const err14 = {instancePath:instancePath+"/thresholds/warn_at",schemaPath:"#/properties/thresholds/properties/warn_at/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
else {
const err15 = {instancePath:instancePath+"/thresholds/warn_at",schemaPath:"#/properties/thresholds/properties/warn_at/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
}
if(data4.critical_at !== undefined){
let data7 = data4.critical_at;
const _errs21 = errors;
let valid7 = false;
const _errs22 = errors;
if((typeof data7 == "number") && (isFinite(data7))){
if(data7 > 1 || isNaN(data7)){
const err16 = {instancePath:instancePath+"/thresholds/critical_at",schemaPath:"#/properties/thresholds/properties/critical_at/anyOf/0/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
if(data7 < 0 || isNaN(data7)){
const err17 = {instancePath:instancePath+"/thresholds/critical_at",schemaPath:"#/properties/thresholds/properties/critical_at/anyOf/0/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
}
else {
const err18 = {instancePath:instancePath+"/thresholds/critical_at",schemaPath:"#/properties/thresholds/properties/critical_at/anyOf/0/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
var _valid0 = _errs22 === errors;
valid7 = valid7 || _valid0;
const _errs24 = errors;
if(data7 !== null){
const err19 = {instancePath:instancePath+"/thresholds/critical_at",schemaPath:"#/properties/thresholds/properties/critical_at/anyOf/1/type",keyword:"type",params:{type: "null"},message:"must be null"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
var _valid0 = _errs24 === errors;
valid7 = valid7 || _valid0;
if(!valid7){
const err20 = {instancePath:instancePath+"/thresholds/critical_at",schemaPath:"#/properties/thresholds/properties/critical_at/anyOf",keyword:"anyOf",params:{},message:"must match a schema in anyOf"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
else {
errors = _errs21;
if(vErrors !== null){
if(_errs21){
vErrors.length = _errs21;
}
else {
vErrors = null;
}
}
}
}
if(data4.escalate_from !== undefined){
let data8 = data4.escalate_from;
const _errs27 = errors;
let valid8 = false;
const _errs28 = errors;
if((typeof data8 == "number") && (isFinite(data8))){
if(data8 > 1 || isNaN(data8)){
const err21 = {instancePath:instancePath+"/thresholds/escalate_from",schemaPath:"#/properties/thresholds/properties/escalate_from/anyOf/0/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
if(data8 < 0 || isNaN(data8)){
const err22 = {instancePath:instancePath+"/thresholds/escalate_from",schemaPath:"#/properties/thresholds/properties/escalate_from/anyOf/0/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
}
else {
const err23 = {instancePath:instancePath+"/thresholds/escalate_from",schemaPath:"#/properties/thresholds/properties/escalate_from/anyOf/0/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
var _valid1 = _errs28 === errors;
valid8 = valid8 || _valid1;
const _errs30 = errors;
if(data8 !== null){
const err24 = {instancePath:instancePath+"/thresholds/escalate_from",schemaPath:"#/properties/thresholds/properties/escalate_from/anyOf/1/type",keyword:"type",params:{type: "null"},message:"must be null"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
var _valid1 = _errs30 === errors;
valid8 = valid8 || _valid1;
if(!valid8){
const err25 = {instancePath:instancePath+"/thresholds/escalate_from",schemaPath:"#/properties/thresholds/properties/escalate_from/anyOf",keyword:"anyOf",params:{},message:"must match a schema in anyOf"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
else {
errors = _errs27;
if(vErrors !== null){
if(_errs27){
vErrors.length = _errs27;
}
else {
vErrors = null;
}
}
}
}
}
else {
const err26 = {instancePath:instancePath+"/thresholds",schemaPath:"#/properties/thresholds/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
if(data.enabled !== undefined){
if(typeof data.enabled !== "boolean"){
const err27 = {instancePath:instancePath+"/enabled",schemaPath:"#/properties/enabled/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
if(data.evidence_sources !== undefined){
let data10 = data.evidence_sources;
if(Array.isArray(data10)){
const len0 = data10.length;
for(let i0=0; i0<len0; i0++){
let data11 = data10[i0];
if(!(((((data11 === "request_system") || (data11 === "request_context")) || (data11 === "tool_results")) || (data11 === "extension")) || (data11 === "knowledge"))){
const err28 = {instancePath:instancePath+"/evidence_sources/" + i0,schemaPath:"common.v1.schema.json#/$defs/EvidenceSource/enum",keyword:"enum",params:{allowedValues: schema37.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
}
let i1 = data10.length;
let j0;
if(i1 > 1){
outer0:
for(;i1--;){
for(j0 = i1; j0--;){
if(func0(data10[i1], data10[j0])){
const err29 = {instancePath:instancePath+"/evidence_sources",schemaPath:"#/properties/evidence_sources/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err30 = {instancePath:instancePath+"/evidence_sources",schemaPath:"#/properties/evidence_sources/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
}
}
else {
const err31 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
validate25.errors = vErrors;
return errors === 0;
}
validate25.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};

const schema43 = {"title":"DetectorSettings","type":"object","additionalProperties":false,"properties":{"flag_on":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/DetectorId"},"default":["urls_not_in_evidence","quotes_not_in_evidence","citation_out_of_range","secret_pattern","system_prompt_leak"],"description":"Numbers and dates never flag alone."},"block_on":{"type":"array","uniqueItems":true,"items":{"$ref":"common.v1.schema.json#/$defs/DetectorId"},"default":[]},"min_integer":{"type":"integer","minimum":0,"default":10,"description":"Integers above min_integer are always checked; unit-adjacent integers are always checked."}}};
const schema44 = {"enum":["numbers_not_in_evidence","dates_not_in_evidence","urls_not_in_evidence","quotes_not_in_evidence","citation_out_of_range","secret_pattern","system_prompt_leak"]};

function validate27(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate27.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
for(const key0 in data){
if(!(((key0 === "flag_on") || (key0 === "block_on")) || (key0 === "min_integer"))){
const err0 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
if(data.flag_on !== undefined){
let data0 = data.flag_on;
if(Array.isArray(data0)){
const len0 = data0.length;
for(let i0=0; i0<len0; i0++){
let data1 = data0[i0];
if(!(((((((data1 === "numbers_not_in_evidence") || (data1 === "dates_not_in_evidence")) || (data1 === "urls_not_in_evidence")) || (data1 === "quotes_not_in_evidence")) || (data1 === "citation_out_of_range")) || (data1 === "secret_pattern")) || (data1 === "system_prompt_leak"))){
const err1 = {instancePath:instancePath+"/flag_on/" + i0,schemaPath:"common.v1.schema.json#/$defs/DetectorId/enum",keyword:"enum",params:{allowedValues: schema44.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
let i1 = data0.length;
let j0;
if(i1 > 1){
outer0:
for(;i1--;){
for(j0 = i1; j0--;){
if(func0(data0[i1], data0[j0])){
const err2 = {instancePath:instancePath+"/flag_on",schemaPath:"#/properties/flag_on/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err3 = {instancePath:instancePath+"/flag_on",schemaPath:"#/properties/flag_on/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.block_on !== undefined){
let data2 = data.block_on;
if(Array.isArray(data2)){
const len1 = data2.length;
for(let i2=0; i2<len1; i2++){
let data3 = data2[i2];
if(!(((((((data3 === "numbers_not_in_evidence") || (data3 === "dates_not_in_evidence")) || (data3 === "urls_not_in_evidence")) || (data3 === "quotes_not_in_evidence")) || (data3 === "citation_out_of_range")) || (data3 === "secret_pattern")) || (data3 === "system_prompt_leak"))){
const err4 = {instancePath:instancePath+"/block_on/" + i2,schemaPath:"common.v1.schema.json#/$defs/DetectorId/enum",keyword:"enum",params:{allowedValues: schema44.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
let i3 = data2.length;
let j1;
if(i3 > 1){
outer1:
for(;i3--;){
for(j1 = i3; j1--;){
if(func0(data2[i3], data2[j1])){
const err5 = {instancePath:instancePath+"/block_on",schemaPath:"#/properties/block_on/uniqueItems",keyword:"uniqueItems",params:{i: i3, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i3+" are identical)"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
break outer1;
}
}
}
}
}
else {
const err6 = {instancePath:instancePath+"/block_on",schemaPath:"#/properties/block_on/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
if(data.min_integer !== undefined){
let data4 = data.min_integer;
if(!(((typeof data4 == "number") && (!(data4 % 1) && !isNaN(data4))) && (isFinite(data4)))){
const err7 = {instancePath:instancePath+"/min_integer",schemaPath:"#/properties/min_integer/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
if((typeof data4 == "number") && (isFinite(data4))){
if(data4 < 0 || isNaN(data4)){
const err8 = {instancePath:instancePath+"/min_integer",schemaPath:"#/properties/min_integer/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
}
}
else {
const err9 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
validate27.errors = vErrors;
return errors === 0;
}
validate27.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};


function validate21(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate21.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
for(const key0 in data){
if(!(func1.call(schema32.properties, key0))){
const err0 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
if(data.mode !== undefined){
let data0 = data.mode;
if(!((((data0 === "observe") || (data0 === "flag")) || (data0 === "block")) || (data0 === "fallback"))){
const err1 = {instancePath:instancePath+"/mode",schemaPath:"common.v1.schema.json#/$defs/PolicyMode/enum",keyword:"enum",params:{allowedValues: schema34.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
if(data.fail !== undefined){
let data1 = data.fail;
if(!((data1 === "open") || (data1 === "closed"))){
const err2 = {instancePath:instancePath+"/fail",schemaPath:"common.v1.schema.json#/$defs/FailMode/enum",keyword:"enum",params:{allowedValues: schema35.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
}
if(data.deadline_ms !== undefined){
let data2 = data.deadline_ms;
if(!(((typeof data2 == "number") && (!(data2 % 1) && !isNaN(data2))) && (isFinite(data2)))){
const err3 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/properties/deadline_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
if((typeof data2 == "number") && (isFinite(data2))){
if(data2 > 5000 || isNaN(data2)){
const err4 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/properties/deadline_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 5000},message:"must be <= 5000"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
if(data2 < 100 || isNaN(data2)){
const err5 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/properties/deadline_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 100},message:"must be >= 100"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
}
}
if(data.hedge_after_ms !== undefined){
let data3 = data.hedge_after_ms;
if(!(((typeof data3 == "number") && (!(data3 % 1) && !isNaN(data3))) && (isFinite(data3)))){
const err6 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/properties/hedge_after_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
const _errs10 = errors;
const _errs11 = errors;
if((typeof data3 == "number") && (isFinite(data3))){
if(data3 > 99 || isNaN(data3)){
const err7 = {};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
else {
if(data3 < 1 || isNaN(data3)){
const err8 = {};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
}
var valid3 = _errs11 === errors;
if(valid3){
const err9 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/properties/hedge_after_ms/not",keyword:"not",params:{},message:"must NOT be valid"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
else {
errors = _errs10;
if(vErrors !== null){
if(_errs10){
vErrors.length = _errs10;
}
else {
vErrors = null;
}
}
}
if((typeof data3 == "number") && (isFinite(data3))){
if(data3 > 4999 || isNaN(data3)){
const err10 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/properties/hedge_after_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 4999},message:"must be <= 4999"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
if(data3 < 0 || isNaN(data3)){
const err11 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/properties/hedge_after_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
}
}
if(data.sample_rate !== undefined){
let data4 = data.sample_rate;
if((typeof data4 == "number") && (isFinite(data4))){
if(data4 > 1 || isNaN(data4)){
const err12 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/properties/sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
if(data4 < 0 || isNaN(data4)){
const err13 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/properties/sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
}
else {
const err14 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/properties/sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
if(data.require_calibration !== undefined){
if(typeof data.require_calibration !== "boolean"){
const err15 = {instancePath:instancePath+"/require_calibration",schemaPath:"#/properties/require_calibration/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
}
if(data.on_no_evidence !== undefined){
let data6 = data.on_no_evidence;
if(!(((data6 === "ignore") || (data6 === "flag")) || (data6 === "block"))){
const err16 = {instancePath:instancePath+"/on_no_evidence",schemaPath:"#/properties/on_no_evidence/enum",keyword:"enum",params:{allowedValues: schema32.properties.on_no_evidence.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
}
if(data.streaming !== undefined){
let data7 = data.streaming;
if(!((data7 === "passthrough") || (data7 === "buffer"))){
const err17 = {instancePath:instancePath+"/streaming",schemaPath:"#/properties/streaming/enum",keyword:"enum",params:{allowedValues: schema32.properties.streaming.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
}
if(data.stream_annotations !== undefined){
let data8 = data.stream_annotations;
if(!((data8 === "comment") || (data8 === "none"))){
const err18 = {instancePath:instancePath+"/stream_annotations",schemaPath:"#/properties/stream_annotations/enum",keyword:"enum",params:{allowedValues: schema32.properties.stream_annotations.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
}
if(data.buffer_keepalive_s !== undefined){
let data9 = data.buffer_keepalive_s;
if(!(((typeof data9 == "number") && (!(data9 % 1) && !isNaN(data9))) && (isFinite(data9)))){
const err19 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/properties/buffer_keepalive_s/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
if((typeof data9 == "number") && (isFinite(data9))){
if(data9 > 60 || isNaN(data9)){
const err20 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/properties/buffer_keepalive_s/maximum",keyword:"maximum",params:{comparison: "<=", limit: 60},message:"must be <= 60"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
if(data9 < 0 || isNaN(data9)){
const err21 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/properties/buffer_keepalive_s/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
}
}
if(data.headers !== undefined){
let data10 = data.headers;
if(!((data10 === "full") || (data10 === "action_only"))){
const err22 = {instancePath:instancePath+"/headers",schemaPath:"#/properties/headers/enum",keyword:"enum",params:{allowedValues: schema32.properties.headers.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
}
if(data.evidence !== undefined){
if(!(validate23(data.evidence, {instancePath:instancePath+"/evidence",parentData:data,parentDataProperty:"evidence",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate23.errors : vErrors.concat(validate23.errors);
errors = vErrors.length;
}
}
if(data.evaluators !== undefined){
let data12 = data.evaluators;
if(Array.isArray(data12)){
if(data12.length > 16){
const err23 = {instancePath:instancePath+"/evaluators",schemaPath:"#/properties/evaluators/maxItems",keyword:"maxItems",params:{limit: 16},message:"must NOT have more than 16 items"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
const len0 = data12.length;
for(let i0=0; i0<len0; i0++){
if(!(validate25(data12[i0], {instancePath:instancePath+"/evaluators/" + i0,parentData:data12,parentDataProperty:i0,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate25.errors : vErrors.concat(validate25.errors);
errors = vErrors.length;
}
}
}
else {
const err24 = {instancePath:instancePath+"/evaluators",schemaPath:"#/properties/evaluators/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
}
if(data.detectors !== undefined){
if(!(validate27(data.detectors, {instancePath:instancePath+"/detectors",parentData:data,parentDataProperty:"detectors",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate27.errors : vErrors.concat(validate27.errors);
errors = vErrors.length;
}
}
if(data.decision !== undefined){
let data15 = data.decision;
if(data15 && typeof data15 == "object" && !Array.isArray(data15)){
for(const key1 in data15){
if(!((key1 === "model") || (key1 === "max_response_tokens"))){
const err25 = {instancePath:instancePath+"/decision",schemaPath:"#/$defs/DecisionSettings/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
}
if(data15.model !== undefined){
let data16 = data15.model;
if(typeof data16 === "string"){
if(func3(data16) > 128){
const err26 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/DecisionSettings/properties/model/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
if(func3(data16) < 1){
const err27 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/DecisionSettings/properties/model/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
else {
const err28 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/DecisionSettings/properties/model/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
}
if(data15.max_response_tokens !== undefined){
let data17 = data15.max_response_tokens;
if(!(((typeof data17 == "number") && (!(data17 % 1) && !isNaN(data17))) && (isFinite(data17)))){
const err29 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/DecisionSettings/properties/max_response_tokens/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
if((typeof data17 == "number") && (isFinite(data17))){
if(data17 > 12000 || isNaN(data17)){
const err30 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/DecisionSettings/properties/max_response_tokens/maximum",keyword:"maximum",params:{comparison: "<=", limit: 12000},message:"must be <= 12000"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
if(data17 < 500 || isNaN(data17)){
const err31 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/DecisionSettings/properties/max_response_tokens/minimum",keyword:"minimum",params:{comparison: ">=", limit: 500},message:"must be >= 500"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
}
}
}
else {
const err32 = {instancePath:instancePath+"/decision",schemaPath:"#/$defs/DecisionSettings/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
}
if(data.deep_audit !== undefined){
let data18 = data.deep_audit;
if(data18 && typeof data18 == "object" && !Array.isArray(data18)){
for(const key2 in data18){
if(!(((((key2 === "enabled") || (key2 === "pass_sample_rate")) || (key2 === "uniform_sample_rate")) || (key2 === "error_sample_rate")) || (key2 === "verifier"))){
const err33 = {instancePath:instancePath+"/deep_audit",schemaPath:"#/$defs/DeepAuditSettings/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key2},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
}
if(data18.enabled !== undefined){
if(typeof data18.enabled !== "boolean"){
const err34 = {instancePath:instancePath+"/deep_audit/enabled",schemaPath:"#/$defs/DeepAuditSettings/properties/enabled/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
if(data18.pass_sample_rate !== undefined){
let data20 = data18.pass_sample_rate;
if((typeof data20 == "number") && (isFinite(data20))){
if(data20 > 1 || isNaN(data20)){
const err35 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/pass_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
if(data20 < 0 || isNaN(data20)){
const err36 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/pass_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
else {
const err37 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/pass_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
}
if(data18.uniform_sample_rate !== undefined){
let data21 = data18.uniform_sample_rate;
if((typeof data21 == "number") && (isFinite(data21))){
if(data21 > 1 || isNaN(data21)){
const err38 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/uniform_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err38];
}
else {
vErrors.push(err38);
}
errors++;
}
if(data21 < 0 || isNaN(data21)){
const err39 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/uniform_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err39];
}
else {
vErrors.push(err39);
}
errors++;
}
}
else {
const err40 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/uniform_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err40];
}
else {
vErrors.push(err40);
}
errors++;
}
}
if(data18.error_sample_rate !== undefined){
let data22 = data18.error_sample_rate;
if((typeof data22 == "number") && (isFinite(data22))){
if(data22 > 1 || isNaN(data22)){
const err41 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/error_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err41];
}
else {
vErrors.push(err41);
}
errors++;
}
if(data22 < 0 || isNaN(data22)){
const err42 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/error_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err42];
}
else {
vErrors.push(err42);
}
errors++;
}
}
else {
const err43 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/DeepAuditSettings/properties/error_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err43];
}
else {
vErrors.push(err43);
}
errors++;
}
}
if(data18.verifier !== undefined){
let data23 = data18.verifier;
if(!((data23 === "auto") || (data23 === "llm"))){
const err44 = {instancePath:instancePath+"/deep_audit/verifier",schemaPath:"#/$defs/DeepAuditSettings/properties/verifier/enum",keyword:"enum",params:{allowedValues: schema47.properties.verifier.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err44];
}
else {
vErrors.push(err44);
}
errors++;
}
}
}
else {
const err45 = {instancePath:instancePath+"/deep_audit",schemaPath:"#/$defs/DeepAuditSettings/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err45];
}
else {
vErrors.push(err45);
}
errors++;
}
}
if(data.block !== undefined){
let data24 = data.block;
if(data24 && typeof data24 == "object" && !Array.isArray(data24)){
for(const key3 in data24){
if(!((((key3 === "style") || (key3 === "message")) || (key3 === "status")) || (key3 === "anthropic_stop_reason"))){
const err46 = {instancePath:instancePath+"/block",schemaPath:"#/$defs/BlockSettings/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key3},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err46];
}
else {
vErrors.push(err46);
}
errors++;
}
}
if(data24.style !== undefined){
let data25 = data24.style;
if(!((data25 === "message") || (data25 === "error"))){
const err47 = {instancePath:instancePath+"/block/style",schemaPath:"#/$defs/BlockSettings/properties/style/enum",keyword:"enum",params:{allowedValues: schema48.properties.style.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err47];
}
else {
vErrors.push(err47);
}
errors++;
}
}
if(data24.message !== undefined){
let data26 = data24.message;
if(typeof data26 === "string"){
if(func3(data26) > 2000){
const err48 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/BlockSettings/properties/message/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
if(vErrors === null){
vErrors = [err48];
}
else {
vErrors.push(err48);
}
errors++;
}
if(func3(data26) < 1){
const err49 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/BlockSettings/properties/message/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err49];
}
else {
vErrors.push(err49);
}
errors++;
}
}
else {
const err50 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/BlockSettings/properties/message/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err50];
}
else {
vErrors.push(err50);
}
errors++;
}
}
if(data24.status !== undefined){
let data27 = data24.status;
if(!(((((data27 === 400) || (data27 === 403)) || (data27 === 409)) || (data27 === 422)) || (data27 === 451))){
const err51 = {instancePath:instancePath+"/block/status",schemaPath:"#/$defs/BlockSettings/properties/status/enum",keyword:"enum",params:{allowedValues: schema48.properties.status.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err51];
}
else {
vErrors.push(err51);
}
errors++;
}
}
if(data24.anthropic_stop_reason !== undefined){
let data28 = data24.anthropic_stop_reason;
if(!((data28 === "refusal") || (data28 === "end_turn"))){
const err52 = {instancePath:instancePath+"/block/anthropic_stop_reason",schemaPath:"#/$defs/BlockSettings/properties/anthropic_stop_reason/enum",keyword:"enum",params:{allowedValues: schema48.properties.anthropic_stop_reason.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err52];
}
else {
vErrors.push(err52);
}
errors++;
}
}
}
else {
const err53 = {instancePath:instancePath+"/block",schemaPath:"#/$defs/BlockSettings/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err53];
}
else {
vErrors.push(err53);
}
errors++;
}
}
if(data.fallback !== undefined){
let data29 = data.fallback;
if(data29 && typeof data29 == "object" && !Array.isArray(data29)){
for(const key4 in data29){
if(!((((key4 === "upstream") || (key4 === "model")) || (key4 === "system_prompt")) || (key4 === "on_fail"))){
const err54 = {instancePath:instancePath+"/fallback",schemaPath:"#/$defs/FallbackSettings/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key4},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err54];
}
else {
vErrors.push(err54);
}
errors++;
}
}
if(data29.upstream !== undefined){
let data30 = data29.upstream;
if(!((data30 === "same") || (data30 === "fallback"))){
const err55 = {instancePath:instancePath+"/fallback/upstream",schemaPath:"#/$defs/FallbackSettings/properties/upstream/enum",keyword:"enum",params:{allowedValues: schema49.properties.upstream.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err55];
}
else {
vErrors.push(err55);
}
errors++;
}
}
if(data29.model !== undefined){
let data31 = data29.model;
if(typeof data31 === "string"){
if(func3(data31) > 128){
const err56 = {instancePath:instancePath+"/fallback/model",schemaPath:"#/$defs/FallbackSettings/properties/model/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err56];
}
else {
vErrors.push(err56);
}
errors++;
}
}
else {
const err57 = {instancePath:instancePath+"/fallback/model",schemaPath:"#/$defs/FallbackSettings/properties/model/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err57];
}
else {
vErrors.push(err57);
}
errors++;
}
}
if(data29.system_prompt !== undefined){
let data32 = data29.system_prompt;
if(typeof data32 === "string"){
if(func3(data32) > 4000){
const err58 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/FallbackSettings/properties/system_prompt/maxLength",keyword:"maxLength",params:{limit: 4000},message:"must NOT have more than 4000 characters"};
if(vErrors === null){
vErrors = [err58];
}
else {
vErrors.push(err58);
}
errors++;
}
if(func3(data32) < 1){
const err59 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/FallbackSettings/properties/system_prompt/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err59];
}
else {
vErrors.push(err59);
}
errors++;
}
}
else {
const err60 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/FallbackSettings/properties/system_prompt/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err60];
}
else {
vErrors.push(err60);
}
errors++;
}
}
if(data29.on_fail !== undefined){
let data33 = data29.on_fail;
if(!(((data33 === "block") || (data33 === "flag")) || (data33 === "original"))){
const err61 = {instancePath:instancePath+"/fallback/on_fail",schemaPath:"#/$defs/FallbackSettings/properties/on_fail/enum",keyword:"enum",params:{allowedValues: schema49.properties.on_fail.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err61];
}
else {
vErrors.push(err61);
}
errors++;
}
}
}
else {
const err62 = {instancePath:instancePath+"/fallback",schemaPath:"#/$defs/FallbackSettings/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err62];
}
else {
vErrors.push(err62);
}
errors++;
}
}
if(data.store_bodies !== undefined){
let data34 = data.store_bodies;
if(!(((data34 === "all") || (data34 === "sampled")) || (data34 === "none"))){
const err63 = {instancePath:instancePath+"/store_bodies",schemaPath:"#/properties/store_bodies/enum",keyword:"enum",params:{allowedValues: schema32.properties.store_bodies.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err63];
}
else {
vErrors.push(err63);
}
errors++;
}
}
if(data.body_sample_rate_pass !== undefined){
let data35 = data.body_sample_rate_pass;
if((typeof data35 == "number") && (isFinite(data35))){
if(data35 > 1 || isNaN(data35)){
const err64 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/properties/body_sample_rate_pass/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err64];
}
else {
vErrors.push(err64);
}
errors++;
}
if(data35 < 0 || isNaN(data35)){
const err65 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/properties/body_sample_rate_pass/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err65];
}
else {
vErrors.push(err65);
}
errors++;
}
}
else {
const err66 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/properties/body_sample_rate_pass/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err66];
}
else {
vErrors.push(err66);
}
errors++;
}
}
}
else {
const err67 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err67];
}
else {
vErrors.push(err67);
}
errors++;
}
validate21.errors = vErrors;
return errors === 0;
}
validate21.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};

const schema51 = {"title":"PolicyRoute","description":"A route: name, match and any RouteSettings field (merged over defaults).","type":"object","additionalProperties":false,"required":["name","match"],"properties":{"name":{"allOf":[{"$ref":"common.v1.schema.json#/$defs/RouteName"},{"not":{"const":"default"}}],"description":"\"default\" is reserved for the defaults route."},"description":{"type":"string","maxLength":600},"match":{"$ref":"#/$defs/RouteMatch"},"mode":{"$ref":"#/$defs/RouteSettings/properties/mode"},"fail":{"$ref":"#/$defs/RouteSettings/properties/fail"},"deadline_ms":{"$ref":"#/$defs/RouteSettings/properties/deadline_ms"},"hedge_after_ms":{"$ref":"#/$defs/RouteSettings/properties/hedge_after_ms"},"sample_rate":{"$ref":"#/$defs/RouteSettings/properties/sample_rate"},"require_calibration":{"$ref":"#/$defs/RouteSettings/properties/require_calibration"},"on_no_evidence":{"$ref":"#/$defs/RouteSettings/properties/on_no_evidence"},"streaming":{"$ref":"#/$defs/RouteSettings/properties/streaming"},"stream_annotations":{"$ref":"#/$defs/RouteSettings/properties/stream_annotations"},"buffer_keepalive_s":{"$ref":"#/$defs/RouteSettings/properties/buffer_keepalive_s"},"headers":{"$ref":"#/$defs/RouteSettings/properties/headers"},"evidence":{"$ref":"#/$defs/RouteSettings/properties/evidence"},"evaluators":{"$ref":"#/$defs/RouteSettings/properties/evaluators"},"detectors":{"$ref":"#/$defs/RouteSettings/properties/detectors"},"decision":{"$ref":"#/$defs/RouteSettings/properties/decision"},"deep_audit":{"$ref":"#/$defs/RouteSettings/properties/deep_audit"},"block":{"$ref":"#/$defs/RouteSettings/properties/block"},"fallback":{"$ref":"#/$defs/RouteSettings/properties/fallback"},"store_bodies":{"$ref":"#/$defs/RouteSettings/properties/store_bodies"},"body_sample_rate_pass":{"$ref":"#/$defs/RouteSettings/properties/body_sample_rate_pass"}}};
const schema52 = {"type":"string","pattern":"^[a-z0-9-]{1,48}$"};
const schema53 = {"title":"RouteMatch","type":"object","additionalProperties":false,"description":"Every matcher given must match.","properties":{"headers":{"type":"object","maxProperties":16,"propertyNames":{"pattern":"^[A-Za-z0-9-]{1,64}$"},"additionalProperties":{"type":"string","maxLength":256},"description":"Header name (case-insensitive) -> exact value, e.g. {X-TrustAgent-Route: support-bot}."},"paths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","pattern":"^/","maxLength":256},"description":"Request path globs."},"models":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256},"description":"Model name globs."},"key_labels":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64},"description":"API key labels."}}};
const schema56 = {"type":"integer","minimum":100,"maximum":5000,"default":600,"description":"Synchronous gate budget, excluding the upstream call."};
const schema57 = {"type":"integer","minimum":0,"maximum":4999,"not":{"minimum":1,"maximum":99},"default":0,"description":"0 (no hedge) or [100, deadline_ms)."};
const schema58 = {"type":"number","minimum":0,"maximum":1,"default":1,"description":"Observe routes only: share of requests sent to the fast check. Detectors always run."};
const schema59 = {"type":"boolean","default":true,"description":"Caps block/fallback at flag unless every block-set evaluator is calibrated."};
const schema60 = {"enum":["ignore","flag","block"],"default":"ignore","description":"SPEC §2.9 no-evidence rule."};
const schema61 = {"enum":["passthrough","buffer"],"default":"passthrough","description":"Block/fallback with passthrough become effective flag (stream_not_buffered)."};
const schema62 = {"enum":["comment","none"],"default":"comment","description":"Write \": trustagent {...}\" before the terminal SSE event."};
const schema63 = {"type":"integer","minimum":0,"maximum":60,"default":15,"description":"Buffer mode SSE keepalive interval; 0 = none."};
const schema64 = {"enum":["full","action_only"],"default":"full","description":"action_only sends only Trace-Id, Request-ID and Action."};
const schema75 = {"enum":["all","sampled","none"],"default":"sampled"};
const schema76 = {"type":"number","minimum":0,"maximum":1,"default":0.2,"description":"With store_bodies: sampled, the share of passed traces whose bodies are kept."};
const pattern7 = new RegExp("^[a-z0-9-]{1,48}$", "u");
const pattern8 = new RegExp("^[A-Za-z0-9-]{1,64}$", "u");
const pattern9 = new RegExp("^/", "u");

function validate31(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate31.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
for(const key0 in data){
if(!(((((((key0 === "sources") || (key0 === "system_mode")) || (key0 === "line_headers")) || (key0 === "tag_patterns")) || (key0 === "max_tokens")) || (key0 === "untrusted_tools")) || (key0 === "knowledge"))){
const err0 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
if(data.sources !== undefined){
let data0 = data.sources;
if(Array.isArray(data0)){
const len0 = data0.length;
for(let i0=0; i0<len0; i0++){
let data1 = data0[i0];
if(!(((((data1 === "request_system") || (data1 === "request_context")) || (data1 === "tool_results")) || (data1 === "extension")) || (data1 === "knowledge"))){
const err1 = {instancePath:instancePath+"/sources/" + i0,schemaPath:"common.v1.schema.json#/$defs/EvidenceSource/enum",keyword:"enum",params:{allowedValues: schema37.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
let i1 = data0.length;
let j0;
if(i1 > 1){
outer0:
for(;i1--;){
for(j0 = i1; j0--;){
if(func0(data0[i1], data0[j0])){
const err2 = {instancePath:instancePath+"/sources",schemaPath:"#/properties/sources/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err3 = {instancePath:instancePath+"/sources",schemaPath:"#/properties/sources/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.system_mode !== undefined){
let data2 = data.system_mode;
if(!(((data2 === "off") || (data2 === "documents_only")) || (data2 === "whole"))){
const err4 = {instancePath:instancePath+"/system_mode",schemaPath:"#/properties/system_mode/enum",keyword:"enum",params:{allowedValues: schema36.properties.system_mode.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
if(data.line_headers !== undefined){
if(typeof data.line_headers !== "boolean"){
const err5 = {instancePath:instancePath+"/line_headers",schemaPath:"#/properties/line_headers/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
}
if(data.tag_patterns !== undefined){
let data4 = data.tag_patterns;
if(Array.isArray(data4)){
if(data4.length > 16){
const err6 = {instancePath:instancePath+"/tag_patterns",schemaPath:"#/properties/tag_patterns/maxItems",keyword:"maxItems",params:{limit: 16},message:"must NOT have more than 16 items"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
const len1 = data4.length;
for(let i2=0; i2<len1; i2++){
let data5 = data4[i2];
if(data5 && typeof data5 == "object" && !Array.isArray(data5)){
if(data5.pattern === undefined){
const err7 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/required",keyword:"required",params:{missingProperty: "pattern"},message:"must have required property '"+"pattern"+"'"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
for(const key1 in data5){
if(!((key1 === "pattern") || (key1 === "flags"))){
const err8 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
if(data5.pattern !== undefined){
let data6 = data5.pattern;
if(typeof data6 === "string"){
if(func3(data6) > 256){
const err9 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/maxLength",keyword:"maxLength",params:{limit: 256},message:"must NOT have more than 256 characters"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
if(func3(data6) < 1){
const err10 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
}
else {
const err11 = {instancePath:instancePath+"/tag_patterns/" + i2+"/pattern",schemaPath:"#/properties/tag_patterns/items/properties/pattern/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
}
if(data5.flags !== undefined){
let data7 = data5.flags;
if(typeof data7 === "string"){
if(!pattern4.test(data7)){
const err12 = {instancePath:instancePath+"/tag_patterns/" + i2+"/flags",schemaPath:"#/properties/tag_patterns/items/properties/flags/pattern",keyword:"pattern",params:{pattern: "^[ims]{0,3}$"},message:"must match pattern \""+"^[ims]{0,3}$"+"\""};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
else {
const err13 = {instancePath:instancePath+"/tag_patterns/" + i2+"/flags",schemaPath:"#/properties/tag_patterns/items/properties/flags/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
}
}
else {
const err14 = {instancePath:instancePath+"/tag_patterns/" + i2,schemaPath:"#/properties/tag_patterns/items/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
}
else {
const err15 = {instancePath:instancePath+"/tag_patterns",schemaPath:"#/properties/tag_patterns/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
}
if(data.max_tokens !== undefined){
let data8 = data.max_tokens;
if(!(((typeof data8 == "number") && (!(data8 % 1) && !isNaN(data8))) && (isFinite(data8)))){
const err16 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
if((typeof data8 == "number") && (isFinite(data8))){
if(data8 > 20000 || isNaN(data8)){
const err17 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/maximum",keyword:"maximum",params:{comparison: "<=", limit: 20000},message:"must be <= 20000"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
if(data8 < 500 || isNaN(data8)){
const err18 = {instancePath:instancePath+"/max_tokens",schemaPath:"#/properties/max_tokens/minimum",keyword:"minimum",params:{comparison: ">=", limit: 500},message:"must be >= 500"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
}
}
if(data.untrusted_tools !== undefined){
let data9 = data.untrusted_tools;
if(Array.isArray(data9)){
if(data9.length > 32){
const err19 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/maxItems",keyword:"maxItems",params:{limit: 32},message:"must NOT have more than 32 items"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
const len2 = data9.length;
for(let i3=0; i3<len2; i3++){
let data10 = data9[i3];
if(typeof data10 === "string"){
if(func3(data10) > 128){
const err20 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
if(func3(data10) < 1){
const err21 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
}
else {
const err22 = {instancePath:instancePath+"/untrusted_tools/" + i3,schemaPath:"#/properties/untrusted_tools/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
}
let i4 = data9.length;
let j1;
if(i4 > 1){
const indices0 = {};
for(;i4--;){
let item0 = data9[i4];
if(typeof item0 !== "string"){
continue;
}
if(typeof indices0[item0] == "number"){
j1 = indices0[item0];
const err23 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/uniqueItems",keyword:"uniqueItems",params:{i: i4, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i4+" are identical)"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
break;
}
indices0[item0] = i4;
}
}
}
else {
const err24 = {instancePath:instancePath+"/untrusted_tools",schemaPath:"#/properties/untrusted_tools/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
}
if(data.knowledge !== undefined){
let data11 = data.knowledge;
if(data11 && typeof data11 == "object" && !Array.isArray(data11)){
for(const key2 in data11){
if(!((((key2 === "when") || (key2 === "top_k")) || (key2 === "deadline_ms")) || (key2 === "min_score"))){
const err25 = {instancePath:instancePath+"/knowledge",schemaPath:"#/properties/knowledge/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key2},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
}
if(data11.when !== undefined){
let data12 = data11.when;
if(!((data12 === "fallback") || (data12 === "always"))){
const err26 = {instancePath:instancePath+"/knowledge/when",schemaPath:"#/properties/knowledge/properties/when/enum",keyword:"enum",params:{allowedValues: schema36.properties.knowledge.properties.when.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
if(data11.top_k !== undefined){
let data13 = data11.top_k;
if(!(((typeof data13 == "number") && (!(data13 % 1) && !isNaN(data13))) && (isFinite(data13)))){
const err27 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
if((typeof data13 == "number") && (isFinite(data13))){
if(data13 > 50 || isNaN(data13)){
const err28 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/maximum",keyword:"maximum",params:{comparison: "<=", limit: 50},message:"must be <= 50"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
if(data13 < 1 || isNaN(data13)){
const err29 = {instancePath:instancePath+"/knowledge/top_k",schemaPath:"#/properties/knowledge/properties/top_k/minimum",keyword:"minimum",params:{comparison: ">=", limit: 1},message:"must be >= 1"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
}
}
if(data11.deadline_ms !== undefined){
let data14 = data11.deadline_ms;
if(!(((typeof data14 == "number") && (!(data14 % 1) && !isNaN(data14))) && (isFinite(data14)))){
const err30 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
if((typeof data14 == "number") && (isFinite(data14))){
if(data14 > 5000 || isNaN(data14)){
const err31 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 5000},message:"must be <= 5000"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
if(data14 < 1 || isNaN(data14)){
const err32 = {instancePath:instancePath+"/knowledge/deadline_ms",schemaPath:"#/properties/knowledge/properties/deadline_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 1},message:"must be >= 1"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
}
}
if(data11.min_score !== undefined){
let data15 = data11.min_score;
if((typeof data15 == "number") && (isFinite(data15))){
if(data15 > 1 || isNaN(data15)){
const err33 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
if(data15 < 0 || isNaN(data15)){
const err34 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
else {
const err35 = {instancePath:instancePath+"/knowledge/min_score",schemaPath:"#/properties/knowledge/properties/min_score/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
}
}
else {
const err36 = {instancePath:instancePath+"/knowledge",schemaPath:"#/properties/knowledge/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
}
else {
const err37 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
validate31.errors = vErrors;
return errors === 0;
}
validate31.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};

const schema67 = {"type":"array","maxItems":16,"items":{"$ref":"#/$defs/RouteEvaluatorRef"},"default":[{"ref":"groundedness@1"},{"ref":"contradiction@1"},{"ref":"refusal@1"},{"ref":"pii_secret_leak@1"}],"description":"Replaces (does not merge) the defaults list. Default: question set gateway-default. Unique ids; param names unique across the route."};

function validate33(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate33.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(Array.isArray(data)){
if(data.length > 16){
const err0 = {instancePath,schemaPath:"#/maxItems",keyword:"maxItems",params:{limit: 16},message:"must NOT have more than 16 items"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
const len0 = data.length;
for(let i0=0; i0<len0; i0++){
if(!(validate25(data[i0], {instancePath:instancePath+"/" + i0,parentData:data,parentDataProperty:i0,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate25.errors : vErrors.concat(validate25.errors);
errors = vErrors.length;
}
}
}
else {
const err1 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
validate33.errors = vErrors;
return errors === 0;
}
validate33.evaluated = {"items":true,"dynamicProps":false,"dynamicItems":false};


function validate36(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate36.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
for(const key0 in data){
if(!(((key0 === "flag_on") || (key0 === "block_on")) || (key0 === "min_integer"))){
const err0 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
if(data.flag_on !== undefined){
let data0 = data.flag_on;
if(Array.isArray(data0)){
const len0 = data0.length;
for(let i0=0; i0<len0; i0++){
let data1 = data0[i0];
if(!(((((((data1 === "numbers_not_in_evidence") || (data1 === "dates_not_in_evidence")) || (data1 === "urls_not_in_evidence")) || (data1 === "quotes_not_in_evidence")) || (data1 === "citation_out_of_range")) || (data1 === "secret_pattern")) || (data1 === "system_prompt_leak"))){
const err1 = {instancePath:instancePath+"/flag_on/" + i0,schemaPath:"common.v1.schema.json#/$defs/DetectorId/enum",keyword:"enum",params:{allowedValues: schema44.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
let i1 = data0.length;
let j0;
if(i1 > 1){
outer0:
for(;i1--;){
for(j0 = i1; j0--;){
if(func0(data0[i1], data0[j0])){
const err2 = {instancePath:instancePath+"/flag_on",schemaPath:"#/properties/flag_on/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err3 = {instancePath:instancePath+"/flag_on",schemaPath:"#/properties/flag_on/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.block_on !== undefined){
let data2 = data.block_on;
if(Array.isArray(data2)){
const len1 = data2.length;
for(let i2=0; i2<len1; i2++){
let data3 = data2[i2];
if(!(((((((data3 === "numbers_not_in_evidence") || (data3 === "dates_not_in_evidence")) || (data3 === "urls_not_in_evidence")) || (data3 === "quotes_not_in_evidence")) || (data3 === "citation_out_of_range")) || (data3 === "secret_pattern")) || (data3 === "system_prompt_leak"))){
const err4 = {instancePath:instancePath+"/block_on/" + i2,schemaPath:"common.v1.schema.json#/$defs/DetectorId/enum",keyword:"enum",params:{allowedValues: schema44.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
let i3 = data2.length;
let j1;
if(i3 > 1){
outer1:
for(;i3--;){
for(j1 = i3; j1--;){
if(func0(data2[i3], data2[j1])){
const err5 = {instancePath:instancePath+"/block_on",schemaPath:"#/properties/block_on/uniqueItems",keyword:"uniqueItems",params:{i: i3, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i3+" are identical)"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
break outer1;
}
}
}
}
}
else {
const err6 = {instancePath:instancePath+"/block_on",schemaPath:"#/properties/block_on/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
if(data.min_integer !== undefined){
let data4 = data.min_integer;
if(!(((typeof data4 == "number") && (!(data4 % 1) && !isNaN(data4))) && (isFinite(data4)))){
const err7 = {instancePath:instancePath+"/min_integer",schemaPath:"#/properties/min_integer/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
if((typeof data4 == "number") && (isFinite(data4))){
if(data4 < 0 || isNaN(data4)){
const err8 = {instancePath:instancePath+"/min_integer",schemaPath:"#/properties/min_integer/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
}
}
else {
const err9 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
validate36.errors = vErrors;
return errors === 0;
}
validate36.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};


function validate30(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
let vErrors = null;
let errors = 0;
const evaluated0 = validate30.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.name === undefined){
const err0 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "name"},message:"must have required property '"+"name"+"'"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
if(data.match === undefined){
const err1 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "match"},message:"must have required property '"+"match"+"'"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
for(const key0 in data){
if(!(func1.call(schema51.properties, key0))){
const err2 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
}
if(data.name !== undefined){
let data0 = data.name;
if(typeof data0 === "string"){
if(!pattern7.test(data0)){
const err3 = {instancePath:instancePath+"/name",schemaPath:"common.v1.schema.json#/$defs/RouteName/pattern",keyword:"pattern",params:{pattern: "^[a-z0-9-]{1,48}$"},message:"must match pattern \""+"^[a-z0-9-]{1,48}$"+"\""};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
else {
const err4 = {instancePath:instancePath+"/name",schemaPath:"common.v1.schema.json#/$defs/RouteName/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
const _errs7 = errors;
const _errs8 = errors;
if("default" !== data0){
const err5 = {};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
var valid3 = _errs8 === errors;
if(valid3){
const err6 = {instancePath:instancePath+"/name",schemaPath:"#/properties/name/allOf/1/not",keyword:"not",params:{},message:"must NOT be valid"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
else {
errors = _errs7;
if(vErrors !== null){
if(_errs7){
vErrors.length = _errs7;
}
else {
vErrors = null;
}
}
}
}
if(data.description !== undefined){
let data1 = data.description;
if(typeof data1 === "string"){
if(func3(data1) > 600){
const err7 = {instancePath:instancePath+"/description",schemaPath:"#/properties/description/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
else {
const err8 = {instancePath:instancePath+"/description",schemaPath:"#/properties/description/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
if(data.match !== undefined){
let data2 = data.match;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
for(const key1 in data2){
if(!((((key1 === "headers") || (key1 === "paths")) || (key1 === "models")) || (key1 === "key_labels"))){
const err9 = {instancePath:instancePath+"/match",schemaPath:"#/$defs/RouteMatch/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
}
if(data2.headers !== undefined){
let data3 = data2.headers;
if(data3 && typeof data3 == "object" && !Array.isArray(data3)){
if(Object.keys(data3).length > 16){
const err10 = {instancePath:instancePath+"/match/headers",schemaPath:"#/$defs/RouteMatch/properties/headers/maxProperties",keyword:"maxProperties",params:{limit: 16},message:"must NOT have more than 16 properties"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
for(const key2 in data3){
const _errs17 = errors;
if(typeof key2 === "string"){
if(!pattern8.test(key2)){
const err11 = {instancePath:instancePath+"/match/headers",schemaPath:"#/$defs/RouteMatch/properties/headers/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[A-Za-z0-9-]{1,64}$"},message:"must match pattern \""+"^[A-Za-z0-9-]{1,64}$"+"\"",propertyName:key2};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
}
var valid6 = _errs17 === errors;
if(!valid6){
const err12 = {instancePath:instancePath+"/match/headers",schemaPath:"#/$defs/RouteMatch/properties/headers/propertyNames",keyword:"propertyNames",params:{propertyName: key2},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
for(const key3 in data3){
let data4 = data3[key3];
if(typeof data4 === "string"){
if(func3(data4) > 256){
const err13 = {instancePath:instancePath+"/match/headers/" + key3.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/RouteMatch/properties/headers/additionalProperties/maxLength",keyword:"maxLength",params:{limit: 256},message:"must NOT have more than 256 characters"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
}
else {
const err14 = {instancePath:instancePath+"/match/headers/" + key3.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/RouteMatch/properties/headers/additionalProperties/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
}
else {
const err15 = {instancePath:instancePath+"/match/headers",schemaPath:"#/$defs/RouteMatch/properties/headers/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
}
if(data2.paths !== undefined){
let data5 = data2.paths;
if(Array.isArray(data5)){
if(data5.length > 32){
const err16 = {instancePath:instancePath+"/match/paths",schemaPath:"#/$defs/RouteMatch/properties/paths/maxItems",keyword:"maxItems",params:{limit: 32},message:"must NOT have more than 32 items"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
const len0 = data5.length;
for(let i0=0; i0<len0; i0++){
let data6 = data5[i0];
if(typeof data6 === "string"){
if(func3(data6) > 256){
const err17 = {instancePath:instancePath+"/match/paths/" + i0,schemaPath:"#/$defs/RouteMatch/properties/paths/items/maxLength",keyword:"maxLength",params:{limit: 256},message:"must NOT have more than 256 characters"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
if(!pattern9.test(data6)){
const err18 = {instancePath:instancePath+"/match/paths/" + i0,schemaPath:"#/$defs/RouteMatch/properties/paths/items/pattern",keyword:"pattern",params:{pattern: "^/"},message:"must match pattern \""+"^/"+"\""};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
}
else {
const err19 = {instancePath:instancePath+"/match/paths/" + i0,schemaPath:"#/$defs/RouteMatch/properties/paths/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
}
let i1 = data5.length;
let j0;
if(i1 > 1){
const indices0 = {};
for(;i1--;){
let item0 = data5[i1];
if(typeof item0 !== "string"){
continue;
}
if(typeof indices0[item0] == "number"){
j0 = indices0[item0];
const err20 = {instancePath:instancePath+"/match/paths",schemaPath:"#/$defs/RouteMatch/properties/paths/uniqueItems",keyword:"uniqueItems",params:{i: i1, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i1+" are identical)"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
break;
}
indices0[item0] = i1;
}
}
}
else {
const err21 = {instancePath:instancePath+"/match/paths",schemaPath:"#/$defs/RouteMatch/properties/paths/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
}
if(data2.models !== undefined){
let data7 = data2.models;
if(Array.isArray(data7)){
if(data7.length > 32){
const err22 = {instancePath:instancePath+"/match/models",schemaPath:"#/$defs/RouteMatch/properties/models/maxItems",keyword:"maxItems",params:{limit: 32},message:"must NOT have more than 32 items"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
const len1 = data7.length;
for(let i2=0; i2<len1; i2++){
let data8 = data7[i2];
if(typeof data8 === "string"){
if(func3(data8) > 256){
const err23 = {instancePath:instancePath+"/match/models/" + i2,schemaPath:"#/$defs/RouteMatch/properties/models/items/maxLength",keyword:"maxLength",params:{limit: 256},message:"must NOT have more than 256 characters"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
if(func3(data8) < 1){
const err24 = {instancePath:instancePath+"/match/models/" + i2,schemaPath:"#/$defs/RouteMatch/properties/models/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
}
else {
const err25 = {instancePath:instancePath+"/match/models/" + i2,schemaPath:"#/$defs/RouteMatch/properties/models/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
}
let i3 = data7.length;
let j1;
if(i3 > 1){
const indices1 = {};
for(;i3--;){
let item1 = data7[i3];
if(typeof item1 !== "string"){
continue;
}
if(typeof indices1[item1] == "number"){
j1 = indices1[item1];
const err26 = {instancePath:instancePath+"/match/models",schemaPath:"#/$defs/RouteMatch/properties/models/uniqueItems",keyword:"uniqueItems",params:{i: i3, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i3+" are identical)"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
break;
}
indices1[item1] = i3;
}
}
}
else {
const err27 = {instancePath:instancePath+"/match/models",schemaPath:"#/$defs/RouteMatch/properties/models/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
if(data2.key_labels !== undefined){
let data9 = data2.key_labels;
if(Array.isArray(data9)){
if(data9.length > 32){
const err28 = {instancePath:instancePath+"/match/key_labels",schemaPath:"#/$defs/RouteMatch/properties/key_labels/maxItems",keyword:"maxItems",params:{limit: 32},message:"must NOT have more than 32 items"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
const len2 = data9.length;
for(let i4=0; i4<len2; i4++){
let data10 = data9[i4];
if(typeof data10 === "string"){
if(func3(data10) > 64){
const err29 = {instancePath:instancePath+"/match/key_labels/" + i4,schemaPath:"#/$defs/RouteMatch/properties/key_labels/items/maxLength",keyword:"maxLength",params:{limit: 64},message:"must NOT have more than 64 characters"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
if(func3(data10) < 1){
const err30 = {instancePath:instancePath+"/match/key_labels/" + i4,schemaPath:"#/$defs/RouteMatch/properties/key_labels/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
}
else {
const err31 = {instancePath:instancePath+"/match/key_labels/" + i4,schemaPath:"#/$defs/RouteMatch/properties/key_labels/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
}
let i5 = data9.length;
let j2;
if(i5 > 1){
const indices2 = {};
for(;i5--;){
let item2 = data9[i5];
if(typeof item2 !== "string"){
continue;
}
if(typeof indices2[item2] == "number"){
j2 = indices2[item2];
const err32 = {instancePath:instancePath+"/match/key_labels",schemaPath:"#/$defs/RouteMatch/properties/key_labels/uniqueItems",keyword:"uniqueItems",params:{i: i5, j: j2},message:"must NOT have duplicate items (items ## "+j2+" and "+i5+" are identical)"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
break;
}
indices2[item2] = i5;
}
}
}
else {
const err33 = {instancePath:instancePath+"/match/key_labels",schemaPath:"#/$defs/RouteMatch/properties/key_labels/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
}
}
else {
const err34 = {instancePath:instancePath+"/match",schemaPath:"#/$defs/RouteMatch/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
if(data.mode !== undefined){
let data11 = data.mode;
if(!((((data11 === "observe") || (data11 === "flag")) || (data11 === "block")) || (data11 === "fallback"))){
const err35 = {instancePath:instancePath+"/mode",schemaPath:"#/$defs/RouteSettings/properties/mode/enum",keyword:"enum",params:{allowedValues: schema34.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
}
if(data.fail !== undefined){
let data12 = data.fail;
if(!((data12 === "open") || (data12 === "closed"))){
const err36 = {instancePath:instancePath+"/fail",schemaPath:"#/$defs/RouteSettings/properties/fail/enum",keyword:"enum",params:{allowedValues: schema35.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
if(data.deadline_ms !== undefined){
let data13 = data.deadline_ms;
if(!(((typeof data13 == "number") && (!(data13 % 1) && !isNaN(data13))) && (isFinite(data13)))){
const err37 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/$defs/RouteSettings/properties/deadline_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
if((typeof data13 == "number") && (isFinite(data13))){
if(data13 > 5000 || isNaN(data13)){
const err38 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/$defs/RouteSettings/properties/deadline_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 5000},message:"must be <= 5000"};
if(vErrors === null){
vErrors = [err38];
}
else {
vErrors.push(err38);
}
errors++;
}
if(data13 < 100 || isNaN(data13)){
const err39 = {instancePath:instancePath+"/deadline_ms",schemaPath:"#/$defs/RouteSettings/properties/deadline_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 100},message:"must be >= 100"};
if(vErrors === null){
vErrors = [err39];
}
else {
vErrors.push(err39);
}
errors++;
}
}
}
if(data.hedge_after_ms !== undefined){
let data14 = data.hedge_after_ms;
if(!(((typeof data14 == "number") && (!(data14 % 1) && !isNaN(data14))) && (isFinite(data14)))){
const err40 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/$defs/RouteSettings/properties/hedge_after_ms/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err40];
}
else {
vErrors.push(err40);
}
errors++;
}
const _errs43 = errors;
const _errs44 = errors;
if((typeof data14 == "number") && (isFinite(data14))){
if(data14 > 99 || isNaN(data14)){
const err41 = {};
if(vErrors === null){
vErrors = [err41];
}
else {
vErrors.push(err41);
}
errors++;
}
else {
if(data14 < 1 || isNaN(data14)){
const err42 = {};
if(vErrors === null){
vErrors = [err42];
}
else {
vErrors.push(err42);
}
errors++;
}
}
}
var valid21 = _errs44 === errors;
if(valid21){
const err43 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/$defs/RouteSettings/properties/hedge_after_ms/not",keyword:"not",params:{},message:"must NOT be valid"};
if(vErrors === null){
vErrors = [err43];
}
else {
vErrors.push(err43);
}
errors++;
}
else {
errors = _errs43;
if(vErrors !== null){
if(_errs43){
vErrors.length = _errs43;
}
else {
vErrors = null;
}
}
}
if((typeof data14 == "number") && (isFinite(data14))){
if(data14 > 4999 || isNaN(data14)){
const err44 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/$defs/RouteSettings/properties/hedge_after_ms/maximum",keyword:"maximum",params:{comparison: "<=", limit: 4999},message:"must be <= 4999"};
if(vErrors === null){
vErrors = [err44];
}
else {
vErrors.push(err44);
}
errors++;
}
if(data14 < 0 || isNaN(data14)){
const err45 = {instancePath:instancePath+"/hedge_after_ms",schemaPath:"#/$defs/RouteSettings/properties/hedge_after_ms/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err45];
}
else {
vErrors.push(err45);
}
errors++;
}
}
}
if(data.sample_rate !== undefined){
let data15 = data.sample_rate;
if((typeof data15 == "number") && (isFinite(data15))){
if(data15 > 1 || isNaN(data15)){
const err46 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/$defs/RouteSettings/properties/sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err46];
}
else {
vErrors.push(err46);
}
errors++;
}
if(data15 < 0 || isNaN(data15)){
const err47 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/$defs/RouteSettings/properties/sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err47];
}
else {
vErrors.push(err47);
}
errors++;
}
}
else {
const err48 = {instancePath:instancePath+"/sample_rate",schemaPath:"#/$defs/RouteSettings/properties/sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err48];
}
else {
vErrors.push(err48);
}
errors++;
}
}
if(data.require_calibration !== undefined){
if(typeof data.require_calibration !== "boolean"){
const err49 = {instancePath:instancePath+"/require_calibration",schemaPath:"#/$defs/RouteSettings/properties/require_calibration/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err49];
}
else {
vErrors.push(err49);
}
errors++;
}
}
if(data.on_no_evidence !== undefined){
let data17 = data.on_no_evidence;
if(!(((data17 === "ignore") || (data17 === "flag")) || (data17 === "block"))){
const err50 = {instancePath:instancePath+"/on_no_evidence",schemaPath:"#/$defs/RouteSettings/properties/on_no_evidence/enum",keyword:"enum",params:{allowedValues: schema60.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err50];
}
else {
vErrors.push(err50);
}
errors++;
}
}
if(data.streaming !== undefined){
let data18 = data.streaming;
if(!((data18 === "passthrough") || (data18 === "buffer"))){
const err51 = {instancePath:instancePath+"/streaming",schemaPath:"#/$defs/RouteSettings/properties/streaming/enum",keyword:"enum",params:{allowedValues: schema61.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err51];
}
else {
vErrors.push(err51);
}
errors++;
}
}
if(data.stream_annotations !== undefined){
let data19 = data.stream_annotations;
if(!((data19 === "comment") || (data19 === "none"))){
const err52 = {instancePath:instancePath+"/stream_annotations",schemaPath:"#/$defs/RouteSettings/properties/stream_annotations/enum",keyword:"enum",params:{allowedValues: schema62.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err52];
}
else {
vErrors.push(err52);
}
errors++;
}
}
if(data.buffer_keepalive_s !== undefined){
let data20 = data.buffer_keepalive_s;
if(!(((typeof data20 == "number") && (!(data20 % 1) && !isNaN(data20))) && (isFinite(data20)))){
const err53 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/$defs/RouteSettings/properties/buffer_keepalive_s/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err53];
}
else {
vErrors.push(err53);
}
errors++;
}
if((typeof data20 == "number") && (isFinite(data20))){
if(data20 > 60 || isNaN(data20)){
const err54 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/$defs/RouteSettings/properties/buffer_keepalive_s/maximum",keyword:"maximum",params:{comparison: "<=", limit: 60},message:"must be <= 60"};
if(vErrors === null){
vErrors = [err54];
}
else {
vErrors.push(err54);
}
errors++;
}
if(data20 < 0 || isNaN(data20)){
const err55 = {instancePath:instancePath+"/buffer_keepalive_s",schemaPath:"#/$defs/RouteSettings/properties/buffer_keepalive_s/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err55];
}
else {
vErrors.push(err55);
}
errors++;
}
}
}
if(data.headers !== undefined){
let data21 = data.headers;
if(!((data21 === "full") || (data21 === "action_only"))){
const err56 = {instancePath:instancePath+"/headers",schemaPath:"#/$defs/RouteSettings/properties/headers/enum",keyword:"enum",params:{allowedValues: schema64.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err56];
}
else {
vErrors.push(err56);
}
errors++;
}
}
if(data.evidence !== undefined){
if(!(validate31(data.evidence, {instancePath:instancePath+"/evidence",parentData:data,parentDataProperty:"evidence",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate31.errors : vErrors.concat(validate31.errors);
errors = vErrors.length;
}
}
if(data.evaluators !== undefined){
if(!(validate33(data.evaluators, {instancePath:instancePath+"/evaluators",parentData:data,parentDataProperty:"evaluators",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate33.errors : vErrors.concat(validate33.errors);
errors = vErrors.length;
}
}
if(data.detectors !== undefined){
if(!(validate36(data.detectors, {instancePath:instancePath+"/detectors",parentData:data,parentDataProperty:"detectors",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate36.errors : vErrors.concat(validate36.errors);
errors = vErrors.length;
}
}
if(data.decision !== undefined){
let data25 = data.decision;
if(data25 && typeof data25 == "object" && !Array.isArray(data25)){
for(const key4 in data25){
if(!((key4 === "model") || (key4 === "max_response_tokens"))){
const err57 = {instancePath:instancePath+"/decision",schemaPath:"#/$defs/RouteSettings/properties/decision/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key4},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err57];
}
else {
vErrors.push(err57);
}
errors++;
}
}
if(data25.model !== undefined){
let data26 = data25.model;
if(typeof data26 === "string"){
if(func3(data26) > 128){
const err58 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/model/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err58];
}
else {
vErrors.push(err58);
}
errors++;
}
if(func3(data26) < 1){
const err59 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/model/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err59];
}
else {
vErrors.push(err59);
}
errors++;
}
}
else {
const err60 = {instancePath:instancePath+"/decision/model",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/model/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err60];
}
else {
vErrors.push(err60);
}
errors++;
}
}
if(data25.max_response_tokens !== undefined){
let data27 = data25.max_response_tokens;
if(!(((typeof data27 == "number") && (!(data27 % 1) && !isNaN(data27))) && (isFinite(data27)))){
const err61 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/max_response_tokens/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err61];
}
else {
vErrors.push(err61);
}
errors++;
}
if((typeof data27 == "number") && (isFinite(data27))){
if(data27 > 12000 || isNaN(data27)){
const err62 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/max_response_tokens/maximum",keyword:"maximum",params:{comparison: "<=", limit: 12000},message:"must be <= 12000"};
if(vErrors === null){
vErrors = [err62];
}
else {
vErrors.push(err62);
}
errors++;
}
if(data27 < 500 || isNaN(data27)){
const err63 = {instancePath:instancePath+"/decision/max_response_tokens",schemaPath:"#/$defs/RouteSettings/properties/decision/properties/max_response_tokens/minimum",keyword:"minimum",params:{comparison: ">=", limit: 500},message:"must be >= 500"};
if(vErrors === null){
vErrors = [err63];
}
else {
vErrors.push(err63);
}
errors++;
}
}
}
}
else {
const err64 = {instancePath:instancePath+"/decision",schemaPath:"#/$defs/RouteSettings/properties/decision/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err64];
}
else {
vErrors.push(err64);
}
errors++;
}
}
if(data.deep_audit !== undefined){
let data28 = data.deep_audit;
if(data28 && typeof data28 == "object" && !Array.isArray(data28)){
for(const key5 in data28){
if(!(((((key5 === "enabled") || (key5 === "pass_sample_rate")) || (key5 === "uniform_sample_rate")) || (key5 === "error_sample_rate")) || (key5 === "verifier"))){
const err65 = {instancePath:instancePath+"/deep_audit",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key5},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err65];
}
else {
vErrors.push(err65);
}
errors++;
}
}
if(data28.enabled !== undefined){
if(typeof data28.enabled !== "boolean"){
const err66 = {instancePath:instancePath+"/deep_audit/enabled",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/enabled/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err66];
}
else {
vErrors.push(err66);
}
errors++;
}
}
if(data28.pass_sample_rate !== undefined){
let data30 = data28.pass_sample_rate;
if((typeof data30 == "number") && (isFinite(data30))){
if(data30 > 1 || isNaN(data30)){
const err67 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/pass_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err67];
}
else {
vErrors.push(err67);
}
errors++;
}
if(data30 < 0 || isNaN(data30)){
const err68 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/pass_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err68];
}
else {
vErrors.push(err68);
}
errors++;
}
}
else {
const err69 = {instancePath:instancePath+"/deep_audit/pass_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/pass_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err69];
}
else {
vErrors.push(err69);
}
errors++;
}
}
if(data28.uniform_sample_rate !== undefined){
let data31 = data28.uniform_sample_rate;
if((typeof data31 == "number") && (isFinite(data31))){
if(data31 > 1 || isNaN(data31)){
const err70 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/uniform_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err70];
}
else {
vErrors.push(err70);
}
errors++;
}
if(data31 < 0 || isNaN(data31)){
const err71 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/uniform_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err71];
}
else {
vErrors.push(err71);
}
errors++;
}
}
else {
const err72 = {instancePath:instancePath+"/deep_audit/uniform_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/uniform_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err72];
}
else {
vErrors.push(err72);
}
errors++;
}
}
if(data28.error_sample_rate !== undefined){
let data32 = data28.error_sample_rate;
if((typeof data32 == "number") && (isFinite(data32))){
if(data32 > 1 || isNaN(data32)){
const err73 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/error_sample_rate/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err73];
}
else {
vErrors.push(err73);
}
errors++;
}
if(data32 < 0 || isNaN(data32)){
const err74 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/error_sample_rate/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err74];
}
else {
vErrors.push(err74);
}
errors++;
}
}
else {
const err75 = {instancePath:instancePath+"/deep_audit/error_sample_rate",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/error_sample_rate/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err75];
}
else {
vErrors.push(err75);
}
errors++;
}
}
if(data28.verifier !== undefined){
let data33 = data28.verifier;
if(!((data33 === "auto") || (data33 === "llm"))){
const err76 = {instancePath:instancePath+"/deep_audit/verifier",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/properties/verifier/enum",keyword:"enum",params:{allowedValues: schema47.properties.verifier.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err76];
}
else {
vErrors.push(err76);
}
errors++;
}
}
}
else {
const err77 = {instancePath:instancePath+"/deep_audit",schemaPath:"#/$defs/RouteSettings/properties/deep_audit/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err77];
}
else {
vErrors.push(err77);
}
errors++;
}
}
if(data.block !== undefined){
let data34 = data.block;
if(data34 && typeof data34 == "object" && !Array.isArray(data34)){
for(const key6 in data34){
if(!((((key6 === "style") || (key6 === "message")) || (key6 === "status")) || (key6 === "anthropic_stop_reason"))){
const err78 = {instancePath:instancePath+"/block",schemaPath:"#/$defs/RouteSettings/properties/block/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key6},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err78];
}
else {
vErrors.push(err78);
}
errors++;
}
}
if(data34.style !== undefined){
let data35 = data34.style;
if(!((data35 === "message") || (data35 === "error"))){
const err79 = {instancePath:instancePath+"/block/style",schemaPath:"#/$defs/RouteSettings/properties/block/properties/style/enum",keyword:"enum",params:{allowedValues: schema48.properties.style.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err79];
}
else {
vErrors.push(err79);
}
errors++;
}
}
if(data34.message !== undefined){
let data36 = data34.message;
if(typeof data36 === "string"){
if(func3(data36) > 2000){
const err80 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/RouteSettings/properties/block/properties/message/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
if(vErrors === null){
vErrors = [err80];
}
else {
vErrors.push(err80);
}
errors++;
}
if(func3(data36) < 1){
const err81 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/RouteSettings/properties/block/properties/message/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err81];
}
else {
vErrors.push(err81);
}
errors++;
}
}
else {
const err82 = {instancePath:instancePath+"/block/message",schemaPath:"#/$defs/RouteSettings/properties/block/properties/message/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err82];
}
else {
vErrors.push(err82);
}
errors++;
}
}
if(data34.status !== undefined){
let data37 = data34.status;
if(!(((((data37 === 400) || (data37 === 403)) || (data37 === 409)) || (data37 === 422)) || (data37 === 451))){
const err83 = {instancePath:instancePath+"/block/status",schemaPath:"#/$defs/RouteSettings/properties/block/properties/status/enum",keyword:"enum",params:{allowedValues: schema48.properties.status.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err83];
}
else {
vErrors.push(err83);
}
errors++;
}
}
if(data34.anthropic_stop_reason !== undefined){
let data38 = data34.anthropic_stop_reason;
if(!((data38 === "refusal") || (data38 === "end_turn"))){
const err84 = {instancePath:instancePath+"/block/anthropic_stop_reason",schemaPath:"#/$defs/RouteSettings/properties/block/properties/anthropic_stop_reason/enum",keyword:"enum",params:{allowedValues: schema48.properties.anthropic_stop_reason.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err84];
}
else {
vErrors.push(err84);
}
errors++;
}
}
}
else {
const err85 = {instancePath:instancePath+"/block",schemaPath:"#/$defs/RouteSettings/properties/block/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err85];
}
else {
vErrors.push(err85);
}
errors++;
}
}
if(data.fallback !== undefined){
let data39 = data.fallback;
if(data39 && typeof data39 == "object" && !Array.isArray(data39)){
for(const key7 in data39){
if(!((((key7 === "upstream") || (key7 === "model")) || (key7 === "system_prompt")) || (key7 === "on_fail"))){
const err86 = {instancePath:instancePath+"/fallback",schemaPath:"#/$defs/RouteSettings/properties/fallback/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key7},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err86];
}
else {
vErrors.push(err86);
}
errors++;
}
}
if(data39.upstream !== undefined){
let data40 = data39.upstream;
if(!((data40 === "same") || (data40 === "fallback"))){
const err87 = {instancePath:instancePath+"/fallback/upstream",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/upstream/enum",keyword:"enum",params:{allowedValues: schema49.properties.upstream.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err87];
}
else {
vErrors.push(err87);
}
errors++;
}
}
if(data39.model !== undefined){
let data41 = data39.model;
if(typeof data41 === "string"){
if(func3(data41) > 128){
const err88 = {instancePath:instancePath+"/fallback/model",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/model/maxLength",keyword:"maxLength",params:{limit: 128},message:"must NOT have more than 128 characters"};
if(vErrors === null){
vErrors = [err88];
}
else {
vErrors.push(err88);
}
errors++;
}
}
else {
const err89 = {instancePath:instancePath+"/fallback/model",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/model/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err89];
}
else {
vErrors.push(err89);
}
errors++;
}
}
if(data39.system_prompt !== undefined){
let data42 = data39.system_prompt;
if(typeof data42 === "string"){
if(func3(data42) > 4000){
const err90 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/system_prompt/maxLength",keyword:"maxLength",params:{limit: 4000},message:"must NOT have more than 4000 characters"};
if(vErrors === null){
vErrors = [err90];
}
else {
vErrors.push(err90);
}
errors++;
}
if(func3(data42) < 1){
const err91 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/system_prompt/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err91];
}
else {
vErrors.push(err91);
}
errors++;
}
}
else {
const err92 = {instancePath:instancePath+"/fallback/system_prompt",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/system_prompt/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err92];
}
else {
vErrors.push(err92);
}
errors++;
}
}
if(data39.on_fail !== undefined){
let data43 = data39.on_fail;
if(!(((data43 === "block") || (data43 === "flag")) || (data43 === "original"))){
const err93 = {instancePath:instancePath+"/fallback/on_fail",schemaPath:"#/$defs/RouteSettings/properties/fallback/properties/on_fail/enum",keyword:"enum",params:{allowedValues: schema49.properties.on_fail.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err93];
}
else {
vErrors.push(err93);
}
errors++;
}
}
}
else {
const err94 = {instancePath:instancePath+"/fallback",schemaPath:"#/$defs/RouteSettings/properties/fallback/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err94];
}
else {
vErrors.push(err94);
}
errors++;
}
}
if(data.store_bodies !== undefined){
let data44 = data.store_bodies;
if(!(((data44 === "all") || (data44 === "sampled")) || (data44 === "none"))){
const err95 = {instancePath:instancePath+"/store_bodies",schemaPath:"#/$defs/RouteSettings/properties/store_bodies/enum",keyword:"enum",params:{allowedValues: schema75.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err95];
}
else {
vErrors.push(err95);
}
errors++;
}
}
if(data.body_sample_rate_pass !== undefined){
let data45 = data.body_sample_rate_pass;
if((typeof data45 == "number") && (isFinite(data45))){
if(data45 > 1 || isNaN(data45)){
const err96 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/$defs/RouteSettings/properties/body_sample_rate_pass/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err96];
}
else {
vErrors.push(err96);
}
errors++;
}
if(data45 < 0 || isNaN(data45)){
const err97 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/$defs/RouteSettings/properties/body_sample_rate_pass/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err97];
}
else {
vErrors.push(err97);
}
errors++;
}
}
else {
const err98 = {instancePath:instancePath+"/body_sample_rate_pass",schemaPath:"#/$defs/RouteSettings/properties/body_sample_rate_pass/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err98];
}
else {
vErrors.push(err98);
}
errors++;
}
}
}
else {
const err99 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err99];
}
else {
vErrors.push(err99);
}
errors++;
}
validate30.errors = vErrors;
return errors === 0;
}
validate30.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};


function validate20(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
/*# sourceURL="https://github.com/timkush1/TrustAgent/contracts/schema/policy.v1.schema.json" */;
let vErrors = null;
let errors = 0;
const evaluated0 = validate20.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.version === undefined){
const err0 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "version"},message:"must have required property '"+"version"+"'"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
for(const key0 in data){
if(!((((key0 === "version") || (key0 === "defaults")) || (key0 === "overrides")) || (key0 === "routes"))){
const err1 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
if(data.version !== undefined){
if(1 !== data.version){
const err2 = {instancePath:instancePath+"/version",schemaPath:"#/properties/version/const",keyword:"const",params:{allowedValue: 1},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
}
if(data.defaults !== undefined){
if(!(validate21(data.defaults, {instancePath:instancePath+"/defaults",parentData:data,parentDataProperty:"defaults",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data.overrides !== undefined){
let data2 = data.overrides;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
for(const key1 in data2){
if(!((key1 === "client_mode") || (key1 === "client_rules"))){
const err3 = {instancePath:instancePath+"/overrides",schemaPath:"#/$defs/Overrides/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data2.client_mode !== undefined){
let data3 = data2.client_mode;
if(!(((data3 === "none") || (data3 === "stricter_only")) || (data3 === "any"))){
const err4 = {instancePath:instancePath+"/overrides/client_mode",schemaPath:"#/$defs/Overrides/properties/client_mode/enum",keyword:"enum",params:{allowedValues: schema50.properties.client_mode.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
if(data2.client_rules !== undefined){
if(typeof data2.client_rules !== "boolean"){
const err5 = {instancePath:instancePath+"/overrides/client_rules",schemaPath:"#/$defs/Overrides/properties/client_rules/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
}
}
else {
const err6 = {instancePath:instancePath+"/overrides",schemaPath:"#/$defs/Overrides/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
if(data.routes !== undefined){
let data5 = data.routes;
if(Array.isArray(data5)){
if(data5.length > 64){
const err7 = {instancePath:instancePath+"/routes",schemaPath:"#/properties/routes/maxItems",keyword:"maxItems",params:{limit: 64},message:"must NOT have more than 64 items"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
const len0 = data5.length;
for(let i0=0; i0<len0; i0++){
if(!(validate30(data5[i0], {instancePath:instancePath+"/routes/" + i0,parentData:data5,parentDataProperty:i0,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate30.errors : vErrors.concat(validate30.errors);
errors = vErrors.length;
}
}
}
else {
const err8 = {instancePath:instancePath+"/routes",schemaPath:"#/properties/routes/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
}
}
else {
const err9 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
validate20.errors = vErrors;
return errors === 0;
}
validate20.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};
