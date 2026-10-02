/* eslint-disable */
// @ts-nocheck
/**
 * GENERATED FILE: do not edit. The evaluator.v1 and evaluator-draft.v1 schemas
 * (contracts/schema/evaluator*.v1.schema.json) compiled by ajv standalone
 * (src/lib/evaluator/generateValidator.ts). Regenerate with
 * `node scripts/gen-evaluator-validator.ts`; evaluatorValidator.gen.test.ts fails when this file is stale.
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
export const validateEvaluatorSpec = validate20;
const schema31 = {"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://github.com/timkush1/TrustAgent/contracts/schema/evaluator.v1.schema.json","title":"EvaluatorSpec","description":"A versioned evaluator (decision spec): one independent Jev question plus how its answer becomes a risk level (SPEC §2.8). Copied verbatim from the spec; titles and descriptions are the only additions. Hard rules that JSON Schema cannot express (risk_weights keys are option keys, risk_levels < criteria count, escalate_from < warn_at <= critical_at, backticked references resolve, no question ids in text) are enforced by the Go/TS validators and, for committed files, by validate.mjs.","type":"object","additionalProperties":false,"required":["spec_version","id","version","name","category","state_fields","question","evidence","signal","outcomes"],"properties":{"spec_version":{"const":"evaluator/v1"},"id":{"type":"string","pattern":"^[a-z][a-z0-9_]{1,47}$"},"version":{"type":"integer","minimum":1},"name":{"type":"string","minLength":1,"maxLength":80},"description":{"type":"string","maxLength":600},"category":{"enum":["grounding","safety","compliance","quality","security","custom"]},"builtin":{"type":"boolean","default":false},"verdict_role":{"enum":["unsupported","contradicted"]},"state_fields":{"type":"array","minItems":1,"uniqueItems":true,"items":{"enum":["user_request","assistant_response","evidence","system_instructions","rules"]}},"params":{"type":"object","maxProperties":8,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{1,31}$"},"additionalProperties":{"type":"object","additionalProperties":false,"required":["default"],"properties":{"default":{"type":"string","maxLength":2000},"label":{"type":"string","maxLength":80},"help":{"type":"string","maxLength":300}}}},"question":{"oneOf":[{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"noul"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"object","additionalProperties":false,"required":["true","false"],"properties":{"true":{"$ref":"#/$defs/text"},"false":{"$ref":"#/$defs/text"}}}}},{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"choice"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"object","minProperties":2,"maxProperties":255,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{2,63}$","not":{"enum":["yes","no","other","unknown","none","option1"]}},"additionalProperties":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/option"},{"$ref":"#/$defs/text"}]}}}},{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"score"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"array","minItems":2,"maxItems":10,"items":{"$ref":"#/$defs/text"}}}}]},"evidence":{"type":"object","additionalProperties":false,"required":["requires"],"properties":{"requires":{"enum":["required","optional","none"]},"min_chars":{"type":"integer","minimum":0,"default":40}}},"signal":{"type":"object","additionalProperties":false,"properties":{"invert":{"type":"boolean","default":false},"risk_weights":{"type":"object","minProperties":1,"additionalProperties":{"type":"number","minimum":0,"maximum":1}},"risk_levels":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"integer","minimum":0,"maximum":9}}}},"outcomes":{"type":"object","additionalProperties":false,"required":["warn_at"],"properties":{"warn_at":{"type":"number","minimum":0,"maximum":1},"critical_at":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"min_confidence":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"block_eligible":{"type":"boolean","default":false},"requires_corroboration":{"type":"array","uniqueItems":true,"items":{"type":"string","pattern":"^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"},"default":[]},"implies":{"type":"array","uniqueItems":true,"items":{"type":"string","pattern":"^[a-z][a-z0-9_]{1,47}$"},"default":[]}}},"escalation":{"type":"object","additionalProperties":false,"properties":{"escalate_from":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"numeric_or_date":{"type":"boolean","default":false},"on_error":{"enum":["escalate","ignore"],"default":"escalate"}}},"threshold_basis":{"type":"string","maxLength":400},"tags":{"type":"array","items":{"type":"string","maxLength":32},"maxItems":10},"parent":{"type":["string","null"],"pattern":"^[a-z][a-z0-9_]{1,47}@[0-9]+$"},"changelog":{"type":"string","maxLength":2000}},"allOf":[{"if":{"properties":{"question":{"properties":{"type":{"const":"choice"}}}}},"then":{"properties":{"signal":{"required":["risk_weights"]}}}},{"if":{"properties":{"question":{"properties":{"type":{"const":"score"}}}}},"then":{"properties":{"signal":{"required":["risk_levels"]}}}},{"if":{"properties":{"question":{"properties":{"type":{"const":"noul"}}}}},"then":{"properties":{"signal":{"not":{"anyOf":[{"required":["risk_weights"]},{"required":["risk_levels"]}]}}}}},{"if":{"properties":{"evidence":{"properties":{"requires":{"enum":["required","optional"]}}}}},"then":{"properties":{"state_fields":{"contains":{"const":"evidence"}}}}}],"$defs":{"text":{"title":"EvaluatorText","anyOf":[{"type":"string","minLength":1,"maxLength":4000},{"type":"array","maxItems":20,"items":{"$ref":"#/$defs/text"}},{"type":"object","maxProperties":20,"propertyNames":{"pattern":"^[\\x20-\\x7e]{1,64}$"},"additionalProperties":{"$ref":"#/$defs/text"}}]},"option":{"title":"EvaluatorOption","type":"object","additionalProperties":false,"required":["what"],"properties":{"what":{"type":"string","maxLength":600},"not_for":{"type":"string","maxLength":600},"examples":{"type":"array","items":{"type":"string","maxLength":300},"maxItems":2}}}}};
const schema33 = {"title":"EvaluatorOption","type":"object","additionalProperties":false,"required":["what"],"properties":{"what":{"type":"string","maxLength":600},"not_for":{"type":"string","maxLength":600},"examples":{"type":"array","items":{"type":"string","maxLength":300},"maxItems":2}}};
const func1 = Object.prototype.hasOwnProperty;
const func2 = ucs2length;
const func0 = equal;
const pattern4 = new RegExp("^[a-z][a-z0-9_]{1,47}$", "u");
const pattern5 = new RegExp("^[a-z][a-z0-9_]{1,31}$", "u");
const pattern7 = new RegExp("^[a-z][a-z0-9_]{2,63}$", "u");
const pattern8 = new RegExp("^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$", "u");
const pattern10 = new RegExp("^[a-z][a-z0-9_]{1,47}@[0-9]+$", "u");
const schema32 = {"title":"EvaluatorText","anyOf":[{"type":"string","minLength":1,"maxLength":4000},{"type":"array","maxItems":20,"items":{"$ref":"#/$defs/text"}},{"type":"object","maxProperties":20,"propertyNames":{"pattern":"^[\\x20-\\x7e]{1,64}$"},"additionalProperties":{"$ref":"#/$defs/text"}}]};
const wrapper0 = {validate: validate21};
const pattern6 = new RegExp("^[\\x20-\\x7e]{1,64}$", "u");

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
const _errs0 = errors;
let valid0 = false;
const _errs1 = errors;
if(typeof data === "string"){
if(func2(data) > 4000){
const err0 = {instancePath,schemaPath:"#/anyOf/0/maxLength",keyword:"maxLength",params:{limit: 4000},message:"must NOT have more than 4000 characters"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
if(func2(data) < 1){
const err1 = {instancePath,schemaPath:"#/anyOf/0/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
else {
const err2 = {instancePath,schemaPath:"#/anyOf/0/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
var _valid0 = _errs1 === errors;
valid0 = valid0 || _valid0;
const _errs3 = errors;
if(Array.isArray(data)){
if(data.length > 20){
const err3 = {instancePath,schemaPath:"#/anyOf/1/maxItems",keyword:"maxItems",params:{limit: 20},message:"must NOT have more than 20 items"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
const len0 = data.length;
for(let i0=0; i0<len0; i0++){
if(!(wrapper0.validate(data[i0], {instancePath:instancePath+"/" + i0,parentData:data,parentDataProperty:i0,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? wrapper0.validate.errors : vErrors.concat(wrapper0.validate.errors);
errors = vErrors.length;
}
}
}
else {
const err4 = {instancePath,schemaPath:"#/anyOf/1/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
var _valid0 = _errs3 === errors;
valid0 = valid0 || _valid0;
if(_valid0){
var items1 = true;
}
const _errs6 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(Object.keys(data).length > 20){
const err5 = {instancePath,schemaPath:"#/anyOf/2/maxProperties",keyword:"maxProperties",params:{limit: 20},message:"must NOT have more than 20 properties"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
for(const key0 in data){
const _errs8 = errors;
if(typeof key0 === "string"){
if(!pattern6.test(key0)){
const err6 = {instancePath,schemaPath:"#/anyOf/2/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[\\x20-\\x7e]{1,64}$"},message:"must match pattern \""+"^[\\x20-\\x7e]{1,64}$"+"\"",propertyName:key0};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
var valid3 = _errs8 === errors;
if(!valid3){
const err7 = {instancePath,schemaPath:"#/anyOf/2/propertyNames",keyword:"propertyNames",params:{propertyName: key0},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
for(const key1 in data){
if(!(wrapper0.validate(data[key1], {instancePath:instancePath+"/" + key1.replace(/~/g, "~0").replace(/\//g, "~1"),parentData:data,parentDataProperty:key1,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? wrapper0.validate.errors : vErrors.concat(wrapper0.validate.errors);
errors = vErrors.length;
}
}
}
else {
const err8 = {instancePath,schemaPath:"#/anyOf/2/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err8];
}
else {
vErrors.push(err8);
}
errors++;
}
var _valid0 = _errs6 === errors;
valid0 = valid0 || _valid0;
if(_valid0){
var props2 = true;
}
if(!valid0){
const err9 = {instancePath,schemaPath:"#/anyOf",keyword:"anyOf",params:{},message:"must match a schema in anyOf"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
else {
errors = _errs0;
if(vErrors !== null){
if(_errs0){
vErrors.length = _errs0;
}
else {
vErrors = null;
}
}
}
validate21.errors = vErrors;
evaluated0.props = props2;
evaluated0.items = items1;
return errors === 0;
}
validate21.evaluated = {"dynamicProps":true,"dynamicItems":true};


function validate20(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
/*# sourceURL="https://github.com/timkush1/TrustAgent/contracts/schema/evaluator.v1.schema.json" */;
let vErrors = null;
let errors = 0;
const evaluated0 = validate20.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
const _errs2 = errors;
let valid1 = true;
const _errs3 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data0 = data.question;
if(data0 && typeof data0 == "object" && !Array.isArray(data0)){
if(data0.type !== undefined){
if("choice" !== data0.type){
const err0 = {};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
}
}
}
var _valid0 = _errs3 === errors;
errors = _errs2;
if(vErrors !== null){
if(_errs2){
vErrors.length = _errs2;
}
else {
vErrors = null;
}
}
if(_valid0){
const _errs6 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data2 = data.signal;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
if(data2.risk_weights === undefined){
const err1 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/0/then/properties/signal/required",keyword:"required",params:{missingProperty: "risk_weights"},message:"must have required property '"+"risk_weights"+"'"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
}
}
var _valid0 = _errs6 === errors;
valid1 = _valid0;
if(valid1){
var props0 = {};
props0.signal = true;
props0.question = true;
}
}
if(!valid1){
const err2 = {instancePath,schemaPath:"#/allOf/0/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
const _errs9 = errors;
let valid5 = true;
const _errs10 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data3 = data.question;
if(data3 && typeof data3 == "object" && !Array.isArray(data3)){
if(data3.type !== undefined){
if("score" !== data3.type){
const err3 = {};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
}
}
}
var _valid1 = _errs10 === errors;
errors = _errs9;
if(vErrors !== null){
if(_errs9){
vErrors.length = _errs9;
}
else {
vErrors = null;
}
}
if(_valid1){
const _errs13 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data5 = data.signal;
if(data5 && typeof data5 == "object" && !Array.isArray(data5)){
if(data5.risk_levels === undefined){
const err4 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/1/then/properties/signal/required",keyword:"required",params:{missingProperty: "risk_levels"},message:"must have required property '"+"risk_levels"+"'"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
}
}
var _valid1 = _errs13 === errors;
valid5 = _valid1;
if(valid5){
var props1 = {};
props1.signal = true;
props1.question = true;
}
}
if(!valid5){
const err5 = {instancePath,schemaPath:"#/allOf/1/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
if(props0 !== true && props1 !== undefined){
if(props1 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props1);
}
}
const _errs16 = errors;
let valid9 = true;
const _errs17 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data6 = data.question;
if(data6 && typeof data6 == "object" && !Array.isArray(data6)){
if(data6.type !== undefined){
if("noul" !== data6.type){
const err6 = {};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
}
}
}
var _valid2 = _errs17 === errors;
errors = _errs16;
if(vErrors !== null){
if(_errs16){
vErrors.length = _errs16;
}
else {
vErrors = null;
}
}
if(_valid2){
const _errs20 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data8 = data.signal;
const _errs22 = errors;
const _errs23 = errors;
const _errs24 = errors;
let valid14 = false;
const _errs25 = errors;
if(data8 && typeof data8 == "object" && !Array.isArray(data8)){
let missing0;
if((data8.risk_weights === undefined) && (missing0 = "risk_weights")){
const err7 = {};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
var _valid3 = _errs25 === errors;
valid14 = valid14 || _valid3;
const _errs26 = errors;
if(data8 && typeof data8 == "object" && !Array.isArray(data8)){
let missing1;
if((data8.risk_levels === undefined) && (missing1 = "risk_levels")){
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
var _valid3 = _errs26 === errors;
valid14 = valid14 || _valid3;
if(!valid14){
const err9 = {};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
else {
errors = _errs24;
if(vErrors !== null){
if(_errs24){
vErrors.length = _errs24;
}
else {
vErrors = null;
}
}
}
var valid13 = _errs23 === errors;
if(valid13){
const err10 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/2/then/properties/signal/not",keyword:"not",params:{},message:"must NOT be valid"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
else {
errors = _errs22;
if(vErrors !== null){
if(_errs22){
vErrors.length = _errs22;
}
else {
vErrors = null;
}
}
}
}
}
var _valid2 = _errs20 === errors;
valid9 = _valid2;
if(valid9){
var props2 = {};
props2.signal = true;
props2.question = true;
}
}
if(!valid9){
const err11 = {instancePath,schemaPath:"#/allOf/2/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
if(props0 !== true && props2 !== undefined){
if(props2 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props2);
}
}
const _errs28 = errors;
let valid15 = true;
const _errs29 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.evidence !== undefined){
let data9 = data.evidence;
if(data9 && typeof data9 == "object" && !Array.isArray(data9)){
if(data9.requires !== undefined){
let data10 = data9.requires;
if(!((data10 === "required") || (data10 === "optional"))){
const err12 = {};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
}
}
}
var _valid4 = _errs29 === errors;
errors = _errs28;
if(vErrors !== null){
if(_errs28){
vErrors.length = _errs28;
}
else {
vErrors = null;
}
}
if(_valid4){
const _errs32 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.state_fields !== undefined){
let data11 = data.state_fields;
if(Array.isArray(data11)){
const _errs34 = errors;
const len0 = data11.length;
for(let i0=0; i0<len0; i0++){
const _errs35 = errors;
if("evidence" !== data11[i0]){
const err13 = {instancePath:instancePath+"/state_fields/" + i0,schemaPath:"#/allOf/3/then/properties/state_fields/contains/const",keyword:"const",params:{allowedValue: "evidence"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
var valid19 = _errs35 === errors;
if(valid19){
break;
}
}
if(!valid19){
const err14 = {instancePath:instancePath+"/state_fields",schemaPath:"#/allOf/3/then/properties/state_fields/contains",keyword:"contains",params:{minContains: 1},message:"must contain at least 1 valid item(s)"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
else {
errors = _errs34;
if(vErrors !== null){
if(_errs34){
vErrors.length = _errs34;
}
else {
vErrors = null;
}
}
}
}
}
}
var _valid4 = _errs32 === errors;
valid15 = _valid4;
if(valid15){
var props3 = {};
props3.state_fields = true;
props3.evidence = true;
}
}
if(!valid15){
const err15 = {instancePath,schemaPath:"#/allOf/3/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
if(props0 !== true && props3 !== undefined){
if(props3 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props3);
}
}
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.spec_version === undefined){
const err16 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "spec_version"},message:"must have required property '"+"spec_version"+"'"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
if(data.id === undefined){
const err17 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "id"},message:"must have required property '"+"id"+"'"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
if(data.version === undefined){
const err18 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "version"},message:"must have required property '"+"version"+"'"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
if(data.name === undefined){
const err19 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "name"},message:"must have required property '"+"name"+"'"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
if(data.category === undefined){
const err20 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "category"},message:"must have required property '"+"category"+"'"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
if(data.state_fields === undefined){
const err21 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "state_fields"},message:"must have required property '"+"state_fields"+"'"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
if(data.question === undefined){
const err22 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "question"},message:"must have required property '"+"question"+"'"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
if(data.evidence === undefined){
const err23 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "evidence"},message:"must have required property '"+"evidence"+"'"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
if(data.signal === undefined){
const err24 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "signal"},message:"must have required property '"+"signal"+"'"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
if(data.outcomes === undefined){
const err25 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "outcomes"},message:"must have required property '"+"outcomes"+"'"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
for(const key0 in data){
if(!(func1.call(schema31.properties, key0))){
const err26 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
if(data.spec_version !== undefined){
if("evaluator/v1" !== data.spec_version){
const err27 = {instancePath:instancePath+"/spec_version",schemaPath:"#/properties/spec_version/const",keyword:"const",params:{allowedValue: "evaluator/v1"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
if(data.id !== undefined){
let data14 = data.id;
if(typeof data14 === "string"){
if(!pattern4.test(data14)){
const err28 = {instancePath:instancePath+"/id",schemaPath:"#/properties/id/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,47}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,47}$"+"\""};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
}
else {
const err29 = {instancePath:instancePath+"/id",schemaPath:"#/properties/id/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
}
if(data.version !== undefined){
let data15 = data.version;
if(!(((typeof data15 == "number") && (!(data15 % 1) && !isNaN(data15))) && (isFinite(data15)))){
const err30 = {instancePath:instancePath+"/version",schemaPath:"#/properties/version/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
if((typeof data15 == "number") && (isFinite(data15))){
if(data15 < 1 || isNaN(data15)){
const err31 = {instancePath:instancePath+"/version",schemaPath:"#/properties/version/minimum",keyword:"minimum",params:{comparison: ">=", limit: 1},message:"must be >= 1"};
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
if(data.name !== undefined){
let data16 = data.name;
if(typeof data16 === "string"){
if(func2(data16) > 80){
const err32 = {instancePath:instancePath+"/name",schemaPath:"#/properties/name/maxLength",keyword:"maxLength",params:{limit: 80},message:"must NOT have more than 80 characters"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
if(func2(data16) < 1){
const err33 = {instancePath:instancePath+"/name",schemaPath:"#/properties/name/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
}
else {
const err34 = {instancePath:instancePath+"/name",schemaPath:"#/properties/name/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
if(data.description !== undefined){
let data17 = data.description;
if(typeof data17 === "string"){
if(func2(data17) > 600){
const err35 = {instancePath:instancePath+"/description",schemaPath:"#/properties/description/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
}
else {
const err36 = {instancePath:instancePath+"/description",schemaPath:"#/properties/description/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
if(data.category !== undefined){
let data18 = data.category;
if(!((((((data18 === "grounding") || (data18 === "safety")) || (data18 === "compliance")) || (data18 === "quality")) || (data18 === "security")) || (data18 === "custom"))){
const err37 = {instancePath:instancePath+"/category",schemaPath:"#/properties/category/enum",keyword:"enum",params:{allowedValues: schema31.properties.category.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
}
if(data.builtin !== undefined){
if(typeof data.builtin !== "boolean"){
const err38 = {instancePath:instancePath+"/builtin",schemaPath:"#/properties/builtin/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err38];
}
else {
vErrors.push(err38);
}
errors++;
}
}
if(data.verdict_role !== undefined){
let data20 = data.verdict_role;
if(!((data20 === "unsupported") || (data20 === "contradicted"))){
const err39 = {instancePath:instancePath+"/verdict_role",schemaPath:"#/properties/verdict_role/enum",keyword:"enum",params:{allowedValues: schema31.properties.verdict_role.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err39];
}
else {
vErrors.push(err39);
}
errors++;
}
}
if(data.state_fields !== undefined){
let data21 = data.state_fields;
if(Array.isArray(data21)){
if(data21.length < 1){
const err40 = {instancePath:instancePath+"/state_fields",schemaPath:"#/properties/state_fields/minItems",keyword:"minItems",params:{limit: 1},message:"must NOT have fewer than 1 items"};
if(vErrors === null){
vErrors = [err40];
}
else {
vErrors.push(err40);
}
errors++;
}
const len1 = data21.length;
for(let i1=0; i1<len1; i1++){
let data22 = data21[i1];
if(!(((((data22 === "user_request") || (data22 === "assistant_response")) || (data22 === "evidence")) || (data22 === "system_instructions")) || (data22 === "rules"))){
const err41 = {instancePath:instancePath+"/state_fields/" + i1,schemaPath:"#/properties/state_fields/items/enum",keyword:"enum",params:{allowedValues: schema31.properties.state_fields.items.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err41];
}
else {
vErrors.push(err41);
}
errors++;
}
}
let i2 = data21.length;
let j0;
if(i2 > 1){
outer0:
for(;i2--;){
for(j0 = i2; j0--;){
if(func0(data21[i2], data21[j0])){
const err42 = {instancePath:instancePath+"/state_fields",schemaPath:"#/properties/state_fields/uniqueItems",keyword:"uniqueItems",params:{i: i2, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i2+" are identical)"};
if(vErrors === null){
vErrors = [err42];
}
else {
vErrors.push(err42);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err43 = {instancePath:instancePath+"/state_fields",schemaPath:"#/properties/state_fields/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err43];
}
else {
vErrors.push(err43);
}
errors++;
}
}
if(data.params !== undefined){
let data23 = data.params;
if(data23 && typeof data23 == "object" && !Array.isArray(data23)){
if(Object.keys(data23).length > 8){
const err44 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/maxProperties",keyword:"maxProperties",params:{limit: 8},message:"must NOT have more than 8 properties"};
if(vErrors === null){
vErrors = [err44];
}
else {
vErrors.push(err44);
}
errors++;
}
for(const key1 in data23){
const _errs55 = errors;
if(typeof key1 === "string"){
if(!pattern5.test(key1)){
const err45 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,31}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,31}$"+"\"",propertyName:key1};
if(vErrors === null){
vErrors = [err45];
}
else {
vErrors.push(err45);
}
errors++;
}
}
var valid24 = _errs55 === errors;
if(!valid24){
const err46 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/propertyNames",keyword:"propertyNames",params:{propertyName: key1},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err46];
}
else {
vErrors.push(err46);
}
errors++;
}
}
for(const key2 in data23){
let data24 = data23[key2];
if(data24 && typeof data24 == "object" && !Array.isArray(data24)){
if(data24.default === undefined){
const err47 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/params/additionalProperties/required",keyword:"required",params:{missingProperty: "default"},message:"must have required property '"+"default"+"'"};
if(vErrors === null){
vErrors = [err47];
}
else {
vErrors.push(err47);
}
errors++;
}
for(const key3 in data24){
if(!(((key3 === "default") || (key3 === "label")) || (key3 === "help"))){
const err48 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/params/additionalProperties/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key3},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err48];
}
else {
vErrors.push(err48);
}
errors++;
}
}
if(data24.default !== undefined){
let data25 = data24.default;
if(typeof data25 === "string"){
if(func2(data25) > 2000){
const err49 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/default",schemaPath:"#/properties/params/additionalProperties/properties/default/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
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
const err50 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/default",schemaPath:"#/properties/params/additionalProperties/properties/default/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err50];
}
else {
vErrors.push(err50);
}
errors++;
}
}
if(data24.label !== undefined){
let data26 = data24.label;
if(typeof data26 === "string"){
if(func2(data26) > 80){
const err51 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/label",schemaPath:"#/properties/params/additionalProperties/properties/label/maxLength",keyword:"maxLength",params:{limit: 80},message:"must NOT have more than 80 characters"};
if(vErrors === null){
vErrors = [err51];
}
else {
vErrors.push(err51);
}
errors++;
}
}
else {
const err52 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/label",schemaPath:"#/properties/params/additionalProperties/properties/label/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err52];
}
else {
vErrors.push(err52);
}
errors++;
}
}
if(data24.help !== undefined){
let data27 = data24.help;
if(typeof data27 === "string"){
if(func2(data27) > 300){
const err53 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/help",schemaPath:"#/properties/params/additionalProperties/properties/help/maxLength",keyword:"maxLength",params:{limit: 300},message:"must NOT have more than 300 characters"};
if(vErrors === null){
vErrors = [err53];
}
else {
vErrors.push(err53);
}
errors++;
}
}
else {
const err54 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/help",schemaPath:"#/properties/params/additionalProperties/properties/help/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err54];
}
else {
vErrors.push(err54);
}
errors++;
}
}
}
else {
const err55 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/params/additionalProperties/type",keyword:"type",params:{type: "object"},message:"must be object"};
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
else {
const err56 = {instancePath:instancePath+"/params",schemaPath:"#/properties/params/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err56];
}
else {
vErrors.push(err56);
}
errors++;
}
}
if(data.question !== undefined){
let data28 = data.question;
const _errs67 = errors;
let valid27 = false;
let passing0 = null;
const _errs68 = errors;
if(data28 && typeof data28 == "object" && !Array.isArray(data28)){
if(data28.type === undefined){
const err57 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/0/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err57];
}
else {
vErrors.push(err57);
}
errors++;
}
if(data28.instructions === undefined){
const err58 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/0/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err58];
}
else {
vErrors.push(err58);
}
errors++;
}
if(data28.criteria === undefined){
const err59 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/0/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err59];
}
else {
vErrors.push(err59);
}
errors++;
}
for(const key4 in data28){
if(!(((key4 === "type") || (key4 === "instructions")) || (key4 === "criteria"))){
const err60 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/0/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key4},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err60];
}
else {
vErrors.push(err60);
}
errors++;
}
}
if(data28.type !== undefined){
if("noul" !== data28.type){
const err61 = {instancePath:instancePath+"/question/type",schemaPath:"#/properties/question/oneOf/0/properties/type/const",keyword:"const",params:{allowedValue: "noul"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err61];
}
else {
vErrors.push(err61);
}
errors++;
}
}
if(data28.instructions !== undefined){
if(!(validate21(data28.instructions, {instancePath:instancePath+"/question/instructions",parentData:data28,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data28.criteria !== undefined){
let data31 = data28.criteria;
if(data31 && typeof data31 == "object" && !Array.isArray(data31)){
if(data31.true === undefined){
const err62 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/0/properties/criteria/required",keyword:"required",params:{missingProperty: "true"},message:"must have required property '"+"true"+"'"};
if(vErrors === null){
vErrors = [err62];
}
else {
vErrors.push(err62);
}
errors++;
}
if(data31.false === undefined){
const err63 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/0/properties/criteria/required",keyword:"required",params:{missingProperty: "false"},message:"must have required property '"+"false"+"'"};
if(vErrors === null){
vErrors = [err63];
}
else {
vErrors.push(err63);
}
errors++;
}
for(const key5 in data31){
if(!((key5 === "true") || (key5 === "false"))){
const err64 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/0/properties/criteria/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key5},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err64];
}
else {
vErrors.push(err64);
}
errors++;
}
}
if(data31.true !== undefined){
if(!(validate21(data31.true, {instancePath:instancePath+"/question/criteria/true",parentData:data31,parentDataProperty:"true",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data31.false !== undefined){
if(!(validate21(data31.false, {instancePath:instancePath+"/question/criteria/false",parentData:data31,parentDataProperty:"false",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
}
else {
const err65 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/0/properties/criteria/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err65];
}
else {
vErrors.push(err65);
}
errors++;
}
}
}
else {
const err66 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/0/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err66];
}
else {
vErrors.push(err66);
}
errors++;
}
var _valid5 = _errs68 === errors;
if(_valid5){
valid27 = true;
passing0 = 0;
var props7 = true;
}
const _errs78 = errors;
if(data28 && typeof data28 == "object" && !Array.isArray(data28)){
if(data28.type === undefined){
const err67 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/1/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err67];
}
else {
vErrors.push(err67);
}
errors++;
}
if(data28.instructions === undefined){
const err68 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/1/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err68];
}
else {
vErrors.push(err68);
}
errors++;
}
if(data28.criteria === undefined){
const err69 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/1/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err69];
}
else {
vErrors.push(err69);
}
errors++;
}
for(const key6 in data28){
if(!(((key6 === "type") || (key6 === "instructions")) || (key6 === "criteria"))){
const err70 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/1/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key6},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err70];
}
else {
vErrors.push(err70);
}
errors++;
}
}
if(data28.type !== undefined){
if("choice" !== data28.type){
const err71 = {instancePath:instancePath+"/question/type",schemaPath:"#/properties/question/oneOf/1/properties/type/const",keyword:"const",params:{allowedValue: "choice"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err71];
}
else {
vErrors.push(err71);
}
errors++;
}
}
if(data28.instructions !== undefined){
if(!(validate21(data28.instructions, {instancePath:instancePath+"/question/instructions",parentData:data28,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data28.criteria !== undefined){
let data36 = data28.criteria;
if(data36 && typeof data36 == "object" && !Array.isArray(data36)){
if(Object.keys(data36).length > 255){
const err72 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/maxProperties",keyword:"maxProperties",params:{limit: 255},message:"must NOT have more than 255 properties"};
if(vErrors === null){
vErrors = [err72];
}
else {
vErrors.push(err72);
}
errors++;
}
if(Object.keys(data36).length < 2){
const err73 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/minProperties",keyword:"minProperties",params:{limit: 2},message:"must NOT have fewer than 2 properties"};
if(vErrors === null){
vErrors = [err73];
}
else {
vErrors.push(err73);
}
errors++;
}
for(const key7 in data36){
const _errs85 = errors;
const _errs86 = errors;
const _errs87 = errors;
if(!((((((key7 === "yes") || (key7 === "no")) || (key7 === "other")) || (key7 === "unknown")) || (key7 === "none")) || (key7 === "option1"))){
const err74 = {};
if(vErrors === null){
vErrors = [err74];
}
else {
vErrors.push(err74);
}
errors++;
}
var valid32 = _errs87 === errors;
if(valid32){
const err75 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/propertyNames/not",keyword:"not",params:{},message:"must NOT be valid",propertyName:key7};
if(vErrors === null){
vErrors = [err75];
}
else {
vErrors.push(err75);
}
errors++;
}
else {
errors = _errs86;
if(vErrors !== null){
if(_errs86){
vErrors.length = _errs86;
}
else {
vErrors = null;
}
}
}
if(typeof key7 === "string"){
if(!pattern7.test(key7)){
const err76 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{2,63}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{2,63}$"+"\"",propertyName:key7};
if(vErrors === null){
vErrors = [err76];
}
else {
vErrors.push(err76);
}
errors++;
}
}
var valid31 = _errs85 === errors;
if(!valid31){
const err77 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/propertyNames",keyword:"propertyNames",params:{propertyName: key7},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err77];
}
else {
vErrors.push(err77);
}
errors++;
}
}
for(const key8 in data36){
let data37 = data36[key8];
const _errs90 = errors;
let valid34 = false;
const _errs91 = errors;
if(data37 !== null){
const err78 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/question/oneOf/1/properties/criteria/additionalProperties/anyOf/0/type",keyword:"type",params:{type: "null"},message:"must be null"};
if(vErrors === null){
vErrors = [err78];
}
else {
vErrors.push(err78);
}
errors++;
}
var _valid6 = _errs91 === errors;
valid34 = valid34 || _valid6;
const _errs93 = errors;
if(data37 && typeof data37 == "object" && !Array.isArray(data37)){
if(data37.what === undefined){
const err79 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/required",keyword:"required",params:{missingProperty: "what"},message:"must have required property '"+"what"+"'"};
if(vErrors === null){
vErrors = [err79];
}
else {
vErrors.push(err79);
}
errors++;
}
for(const key9 in data37){
if(!(((key9 === "what") || (key9 === "not_for")) || (key9 === "examples"))){
const err80 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key9},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err80];
}
else {
vErrors.push(err80);
}
errors++;
}
}
if(data37.what !== undefined){
let data38 = data37.what;
if(typeof data38 === "string"){
if(func2(data38) > 600){
const err81 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/what",schemaPath:"#/$defs/option/properties/what/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
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
const err82 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/what",schemaPath:"#/$defs/option/properties/what/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err82];
}
else {
vErrors.push(err82);
}
errors++;
}
}
if(data37.not_for !== undefined){
let data39 = data37.not_for;
if(typeof data39 === "string"){
if(func2(data39) > 600){
const err83 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/not_for",schemaPath:"#/$defs/option/properties/not_for/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err83];
}
else {
vErrors.push(err83);
}
errors++;
}
}
else {
const err84 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/not_for",schemaPath:"#/$defs/option/properties/not_for/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err84];
}
else {
vErrors.push(err84);
}
errors++;
}
}
if(data37.examples !== undefined){
let data40 = data37.examples;
if(Array.isArray(data40)){
if(data40.length > 2){
const err85 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples",schemaPath:"#/$defs/option/properties/examples/maxItems",keyword:"maxItems",params:{limit: 2},message:"must NOT have more than 2 items"};
if(vErrors === null){
vErrors = [err85];
}
else {
vErrors.push(err85);
}
errors++;
}
const len2 = data40.length;
for(let i3=0; i3<len2; i3++){
let data41 = data40[i3];
if(typeof data41 === "string"){
if(func2(data41) > 300){
const err86 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples/" + i3,schemaPath:"#/$defs/option/properties/examples/items/maxLength",keyword:"maxLength",params:{limit: 300},message:"must NOT have more than 300 characters"};
if(vErrors === null){
vErrors = [err86];
}
else {
vErrors.push(err86);
}
errors++;
}
}
else {
const err87 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples/" + i3,schemaPath:"#/$defs/option/properties/examples/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err87];
}
else {
vErrors.push(err87);
}
errors++;
}
}
}
else {
const err88 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples",schemaPath:"#/$defs/option/properties/examples/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err88];
}
else {
vErrors.push(err88);
}
errors++;
}
}
}
else {
const err89 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err89];
}
else {
vErrors.push(err89);
}
errors++;
}
var _valid6 = _errs93 === errors;
valid34 = valid34 || _valid6;
if(_valid6){
var props9 = true;
}
const _errs105 = errors;
if(!(validate21(data37, {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),parentData:data36,parentDataProperty:key8,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
else {
var props10 = validate21.evaluated.props;
}
var _valid6 = _errs105 === errors;
valid34 = valid34 || _valid6;
if(_valid6){
if(props9 !== true && props10 !== undefined){
if(props10 === true){
props9 = true;
}
else {
props9 = props9 || {};
Object.assign(props9, props10);
}
}
}
if(!valid34){
const err90 = {instancePath:instancePath+"/question/criteria/" + key8.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/question/oneOf/1/properties/criteria/additionalProperties/anyOf",keyword:"anyOf",params:{},message:"must match a schema in anyOf"};
if(vErrors === null){
vErrors = [err90];
}
else {
vErrors.push(err90);
}
errors++;
}
else {
errors = _errs90;
if(vErrors !== null){
if(_errs90){
vErrors.length = _errs90;
}
else {
vErrors = null;
}
}
}
}
}
else {
const err91 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/1/properties/criteria/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err91];
}
else {
vErrors.push(err91);
}
errors++;
}
}
}
else {
const err92 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/1/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err92];
}
else {
vErrors.push(err92);
}
errors++;
}
var _valid5 = _errs78 === errors;
if(_valid5 && valid27){
valid27 = false;
passing0 = [passing0, 1];
}
else {
if(_valid5){
valid27 = true;
passing0 = 1;
if(props7 !== true){
props7 = true;
}
}
const _errs106 = errors;
if(data28 && typeof data28 == "object" && !Array.isArray(data28)){
if(data28.type === undefined){
const err93 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/2/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err93];
}
else {
vErrors.push(err93);
}
errors++;
}
if(data28.instructions === undefined){
const err94 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/2/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err94];
}
else {
vErrors.push(err94);
}
errors++;
}
if(data28.criteria === undefined){
const err95 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/2/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err95];
}
else {
vErrors.push(err95);
}
errors++;
}
for(const key10 in data28){
if(!(((key10 === "type") || (key10 === "instructions")) || (key10 === "criteria"))){
const err96 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/2/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key10},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err96];
}
else {
vErrors.push(err96);
}
errors++;
}
}
if(data28.type !== undefined){
if("score" !== data28.type){
const err97 = {instancePath:instancePath+"/question/type",schemaPath:"#/properties/question/oneOf/2/properties/type/const",keyword:"const",params:{allowedValue: "score"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err97];
}
else {
vErrors.push(err97);
}
errors++;
}
}
if(data28.instructions !== undefined){
if(!(validate21(data28.instructions, {instancePath:instancePath+"/question/instructions",parentData:data28,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data28.criteria !== undefined){
let data44 = data28.criteria;
if(Array.isArray(data44)){
if(data44.length > 10){
const err98 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/2/properties/criteria/maxItems",keyword:"maxItems",params:{limit: 10},message:"must NOT have more than 10 items"};
if(vErrors === null){
vErrors = [err98];
}
else {
vErrors.push(err98);
}
errors++;
}
if(data44.length < 2){
const err99 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/2/properties/criteria/minItems",keyword:"minItems",params:{limit: 2},message:"must NOT have fewer than 2 items"};
if(vErrors === null){
vErrors = [err99];
}
else {
vErrors.push(err99);
}
errors++;
}
const len3 = data44.length;
for(let i4=0; i4<len3; i4++){
if(!(validate21(data44[i4], {instancePath:instancePath+"/question/criteria/" + i4,parentData:data44,parentDataProperty:i4,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
}
else {
const err100 = {instancePath:instancePath+"/question/criteria",schemaPath:"#/properties/question/oneOf/2/properties/criteria/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err100];
}
else {
vErrors.push(err100);
}
errors++;
}
}
}
else {
const err101 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf/2/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err101];
}
else {
vErrors.push(err101);
}
errors++;
}
var _valid5 = _errs106 === errors;
if(_valid5 && valid27){
valid27 = false;
passing0 = [passing0, 2];
}
else {
if(_valid5){
valid27 = true;
passing0 = 2;
if(props7 !== true){
props7 = true;
}
}
}
}
if(!valid27){
const err102 = {instancePath:instancePath+"/question",schemaPath:"#/properties/question/oneOf",keyword:"oneOf",params:{passingSchemas: passing0},message:"must match exactly one schema in oneOf"};
if(vErrors === null){
vErrors = [err102];
}
else {
vErrors.push(err102);
}
errors++;
}
else {
errors = _errs67;
if(vErrors !== null){
if(_errs67){
vErrors.length = _errs67;
}
else {
vErrors = null;
}
}
}
}
if(data.evidence !== undefined){
let data46 = data.evidence;
if(data46 && typeof data46 == "object" && !Array.isArray(data46)){
if(data46.requires === undefined){
const err103 = {instancePath:instancePath+"/evidence",schemaPath:"#/properties/evidence/required",keyword:"required",params:{missingProperty: "requires"},message:"must have required property '"+"requires"+"'"};
if(vErrors === null){
vErrors = [err103];
}
else {
vErrors.push(err103);
}
errors++;
}
for(const key11 in data46){
if(!((key11 === "requires") || (key11 === "min_chars"))){
const err104 = {instancePath:instancePath+"/evidence",schemaPath:"#/properties/evidence/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key11},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err104];
}
else {
vErrors.push(err104);
}
errors++;
}
}
if(data46.requires !== undefined){
let data47 = data46.requires;
if(!(((data47 === "required") || (data47 === "optional")) || (data47 === "none"))){
const err105 = {instancePath:instancePath+"/evidence/requires",schemaPath:"#/properties/evidence/properties/requires/enum",keyword:"enum",params:{allowedValues: schema31.properties.evidence.properties.requires.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err105];
}
else {
vErrors.push(err105);
}
errors++;
}
}
if(data46.min_chars !== undefined){
let data48 = data46.min_chars;
if(!(((typeof data48 == "number") && (!(data48 % 1) && !isNaN(data48))) && (isFinite(data48)))){
const err106 = {instancePath:instancePath+"/evidence/min_chars",schemaPath:"#/properties/evidence/properties/min_chars/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err106];
}
else {
vErrors.push(err106);
}
errors++;
}
if((typeof data48 == "number") && (isFinite(data48))){
if(data48 < 0 || isNaN(data48)){
const err107 = {instancePath:instancePath+"/evidence/min_chars",schemaPath:"#/properties/evidence/properties/min_chars/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err107];
}
else {
vErrors.push(err107);
}
errors++;
}
}
}
}
else {
const err108 = {instancePath:instancePath+"/evidence",schemaPath:"#/properties/evidence/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err108];
}
else {
vErrors.push(err108);
}
errors++;
}
}
if(data.signal !== undefined){
let data49 = data.signal;
if(data49 && typeof data49 == "object" && !Array.isArray(data49)){
for(const key12 in data49){
if(!(((key12 === "invert") || (key12 === "risk_weights")) || (key12 === "risk_levels"))){
const err109 = {instancePath:instancePath+"/signal",schemaPath:"#/properties/signal/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key12},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err109];
}
else {
vErrors.push(err109);
}
errors++;
}
}
if(data49.invert !== undefined){
if(typeof data49.invert !== "boolean"){
const err110 = {instancePath:instancePath+"/signal/invert",schemaPath:"#/properties/signal/properties/invert/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err110];
}
else {
vErrors.push(err110);
}
errors++;
}
}
if(data49.risk_weights !== undefined){
let data51 = data49.risk_weights;
if(data51 && typeof data51 == "object" && !Array.isArray(data51)){
if(Object.keys(data51).length < 1){
const err111 = {instancePath:instancePath+"/signal/risk_weights",schemaPath:"#/properties/signal/properties/risk_weights/minProperties",keyword:"minProperties",params:{limit: 1},message:"must NOT have fewer than 1 properties"};
if(vErrors === null){
vErrors = [err111];
}
else {
vErrors.push(err111);
}
errors++;
}
for(const key13 in data51){
let data52 = data51[key13];
if((typeof data52 == "number") && (isFinite(data52))){
if(data52 > 1 || isNaN(data52)){
const err112 = {instancePath:instancePath+"/signal/risk_weights/" + key13.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/signal/properties/risk_weights/additionalProperties/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err112];
}
else {
vErrors.push(err112);
}
errors++;
}
if(data52 < 0 || isNaN(data52)){
const err113 = {instancePath:instancePath+"/signal/risk_weights/" + key13.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/signal/properties/risk_weights/additionalProperties/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err113];
}
else {
vErrors.push(err113);
}
errors++;
}
}
else {
const err114 = {instancePath:instancePath+"/signal/risk_weights/" + key13.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/properties/signal/properties/risk_weights/additionalProperties/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err114];
}
else {
vErrors.push(err114);
}
errors++;
}
}
}
else {
const err115 = {instancePath:instancePath+"/signal/risk_weights",schemaPath:"#/properties/signal/properties/risk_weights/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err115];
}
else {
vErrors.push(err115);
}
errors++;
}
}
if(data49.risk_levels !== undefined){
let data53 = data49.risk_levels;
if(Array.isArray(data53)){
if(data53.length < 1){
const err116 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"#/properties/signal/properties/risk_levels/minItems",keyword:"minItems",params:{limit: 1},message:"must NOT have fewer than 1 items"};
if(vErrors === null){
vErrors = [err116];
}
else {
vErrors.push(err116);
}
errors++;
}
const len4 = data53.length;
for(let i5=0; i5<len4; i5++){
let data54 = data53[i5];
if(!(((typeof data54 == "number") && (!(data54 % 1) && !isNaN(data54))) && (isFinite(data54)))){
const err117 = {instancePath:instancePath+"/signal/risk_levels/" + i5,schemaPath:"#/properties/signal/properties/risk_levels/items/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err117];
}
else {
vErrors.push(err117);
}
errors++;
}
if((typeof data54 == "number") && (isFinite(data54))){
if(data54 > 9 || isNaN(data54)){
const err118 = {instancePath:instancePath+"/signal/risk_levels/" + i5,schemaPath:"#/properties/signal/properties/risk_levels/items/maximum",keyword:"maximum",params:{comparison: "<=", limit: 9},message:"must be <= 9"};
if(vErrors === null){
vErrors = [err118];
}
else {
vErrors.push(err118);
}
errors++;
}
if(data54 < 0 || isNaN(data54)){
const err119 = {instancePath:instancePath+"/signal/risk_levels/" + i5,schemaPath:"#/properties/signal/properties/risk_levels/items/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err119];
}
else {
vErrors.push(err119);
}
errors++;
}
}
}
let i6 = data53.length;
let j1;
if(i6 > 1){
const indices0 = {};
for(;i6--;){
let item0 = data53[i6];
if(!(((typeof item0 == "number") && (!(item0 % 1) && !isNaN(item0))) && (isFinite(item0)))){
continue;
}
if(typeof indices0[item0] == "number"){
j1 = indices0[item0];
const err120 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"#/properties/signal/properties/risk_levels/uniqueItems",keyword:"uniqueItems",params:{i: i6, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i6+" are identical)"};
if(vErrors === null){
vErrors = [err120];
}
else {
vErrors.push(err120);
}
errors++;
break;
}
indices0[item0] = i6;
}
}
}
else {
const err121 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"#/properties/signal/properties/risk_levels/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err121];
}
else {
vErrors.push(err121);
}
errors++;
}
}
}
else {
const err122 = {instancePath:instancePath+"/signal",schemaPath:"#/properties/signal/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err122];
}
else {
vErrors.push(err122);
}
errors++;
}
}
if(data.outcomes !== undefined){
let data55 = data.outcomes;
if(data55 && typeof data55 == "object" && !Array.isArray(data55)){
if(data55.warn_at === undefined){
const err123 = {instancePath:instancePath+"/outcomes",schemaPath:"#/properties/outcomes/required",keyword:"required",params:{missingProperty: "warn_at"},message:"must have required property '"+"warn_at"+"'"};
if(vErrors === null){
vErrors = [err123];
}
else {
vErrors.push(err123);
}
errors++;
}
for(const key14 in data55){
if(!((((((key14 === "warn_at") || (key14 === "critical_at")) || (key14 === "min_confidence")) || (key14 === "block_eligible")) || (key14 === "requires_corroboration")) || (key14 === "implies"))){
const err124 = {instancePath:instancePath+"/outcomes",schemaPath:"#/properties/outcomes/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key14},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err124];
}
else {
vErrors.push(err124);
}
errors++;
}
}
if(data55.warn_at !== undefined){
let data56 = data55.warn_at;
if((typeof data56 == "number") && (isFinite(data56))){
if(data56 > 1 || isNaN(data56)){
const err125 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"#/properties/outcomes/properties/warn_at/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err125];
}
else {
vErrors.push(err125);
}
errors++;
}
if(data56 < 0 || isNaN(data56)){
const err126 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"#/properties/outcomes/properties/warn_at/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err126];
}
else {
vErrors.push(err126);
}
errors++;
}
}
else {
const err127 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"#/properties/outcomes/properties/warn_at/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err127];
}
else {
vErrors.push(err127);
}
errors++;
}
}
if(data55.critical_at !== undefined){
let data57 = data55.critical_at;
if((!((typeof data57 == "number") && (isFinite(data57)))) && (data57 !== null)){
const err128 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"#/properties/outcomes/properties/critical_at/type",keyword:"type",params:{type: schema31.properties.outcomes.properties.critical_at.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err128];
}
else {
vErrors.push(err128);
}
errors++;
}
if((typeof data57 == "number") && (isFinite(data57))){
if(data57 > 1 || isNaN(data57)){
const err129 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"#/properties/outcomes/properties/critical_at/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err129];
}
else {
vErrors.push(err129);
}
errors++;
}
if(data57 < 0 || isNaN(data57)){
const err130 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"#/properties/outcomes/properties/critical_at/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err130];
}
else {
vErrors.push(err130);
}
errors++;
}
}
}
if(data55.min_confidence !== undefined){
let data58 = data55.min_confidence;
if((!((typeof data58 == "number") && (isFinite(data58)))) && (data58 !== null)){
const err131 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"#/properties/outcomes/properties/min_confidence/type",keyword:"type",params:{type: schema31.properties.outcomes.properties.min_confidence.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err131];
}
else {
vErrors.push(err131);
}
errors++;
}
if((typeof data58 == "number") && (isFinite(data58))){
if(data58 > 1 || isNaN(data58)){
const err132 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"#/properties/outcomes/properties/min_confidence/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err132];
}
else {
vErrors.push(err132);
}
errors++;
}
if(data58 < 0 || isNaN(data58)){
const err133 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"#/properties/outcomes/properties/min_confidence/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err133];
}
else {
vErrors.push(err133);
}
errors++;
}
}
}
if(data55.block_eligible !== undefined){
if(typeof data55.block_eligible !== "boolean"){
const err134 = {instancePath:instancePath+"/outcomes/block_eligible",schemaPath:"#/properties/outcomes/properties/block_eligible/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err134];
}
else {
vErrors.push(err134);
}
errors++;
}
}
if(data55.requires_corroboration !== undefined){
let data60 = data55.requires_corroboration;
if(Array.isArray(data60)){
const len5 = data60.length;
for(let i7=0; i7<len5; i7++){
let data61 = data60[i7];
if(typeof data61 === "string"){
if(!pattern8.test(data61)){
const err135 = {instancePath:instancePath+"/outcomes/requires_corroboration/" + i7,schemaPath:"#/properties/outcomes/properties/requires_corroboration/items/pattern",keyword:"pattern",params:{pattern: "^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"},message:"must match pattern \""+"^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"+"\""};
if(vErrors === null){
vErrors = [err135];
}
else {
vErrors.push(err135);
}
errors++;
}
}
else {
const err136 = {instancePath:instancePath+"/outcomes/requires_corroboration/" + i7,schemaPath:"#/properties/outcomes/properties/requires_corroboration/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err136];
}
else {
vErrors.push(err136);
}
errors++;
}
}
let i8 = data60.length;
let j2;
if(i8 > 1){
const indices1 = {};
for(;i8--;){
let item1 = data60[i8];
if(typeof item1 !== "string"){
continue;
}
if(typeof indices1[item1] == "number"){
j2 = indices1[item1];
const err137 = {instancePath:instancePath+"/outcomes/requires_corroboration",schemaPath:"#/properties/outcomes/properties/requires_corroboration/uniqueItems",keyword:"uniqueItems",params:{i: i8, j: j2},message:"must NOT have duplicate items (items ## "+j2+" and "+i8+" are identical)"};
if(vErrors === null){
vErrors = [err137];
}
else {
vErrors.push(err137);
}
errors++;
break;
}
indices1[item1] = i8;
}
}
}
else {
const err138 = {instancePath:instancePath+"/outcomes/requires_corroboration",schemaPath:"#/properties/outcomes/properties/requires_corroboration/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err138];
}
else {
vErrors.push(err138);
}
errors++;
}
}
if(data55.implies !== undefined){
let data62 = data55.implies;
if(Array.isArray(data62)){
const len6 = data62.length;
for(let i9=0; i9<len6; i9++){
let data63 = data62[i9];
if(typeof data63 === "string"){
if(!pattern4.test(data63)){
const err139 = {instancePath:instancePath+"/outcomes/implies/" + i9,schemaPath:"#/properties/outcomes/properties/implies/items/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,47}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,47}$"+"\""};
if(vErrors === null){
vErrors = [err139];
}
else {
vErrors.push(err139);
}
errors++;
}
}
else {
const err140 = {instancePath:instancePath+"/outcomes/implies/" + i9,schemaPath:"#/properties/outcomes/properties/implies/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err140];
}
else {
vErrors.push(err140);
}
errors++;
}
}
let i10 = data62.length;
let j3;
if(i10 > 1){
const indices2 = {};
for(;i10--;){
let item2 = data62[i10];
if(typeof item2 !== "string"){
continue;
}
if(typeof indices2[item2] == "number"){
j3 = indices2[item2];
const err141 = {instancePath:instancePath+"/outcomes/implies",schemaPath:"#/properties/outcomes/properties/implies/uniqueItems",keyword:"uniqueItems",params:{i: i10, j: j3},message:"must NOT have duplicate items (items ## "+j3+" and "+i10+" are identical)"};
if(vErrors === null){
vErrors = [err141];
}
else {
vErrors.push(err141);
}
errors++;
break;
}
indices2[item2] = i10;
}
}
}
else {
const err142 = {instancePath:instancePath+"/outcomes/implies",schemaPath:"#/properties/outcomes/properties/implies/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err142];
}
else {
vErrors.push(err142);
}
errors++;
}
}
}
else {
const err143 = {instancePath:instancePath+"/outcomes",schemaPath:"#/properties/outcomes/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err143];
}
else {
vErrors.push(err143);
}
errors++;
}
}
if(data.escalation !== undefined){
let data64 = data.escalation;
if(data64 && typeof data64 == "object" && !Array.isArray(data64)){
for(const key15 in data64){
if(!(((key15 === "escalate_from") || (key15 === "numeric_or_date")) || (key15 === "on_error"))){ // check-forbidden: ignore (the spec field escalation.numeric_or_date)
const err144 = {instancePath:instancePath+"/escalation",schemaPath:"#/properties/escalation/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key15},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err144];
}
else {
vErrors.push(err144);
}
errors++;
}
}
if(data64.escalate_from !== undefined){
let data65 = data64.escalate_from;
if((!((typeof data65 == "number") && (isFinite(data65)))) && (data65 !== null)){
const err145 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"#/properties/escalation/properties/escalate_from/type",keyword:"type",params:{type: schema31.properties.escalation.properties.escalate_from.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err145];
}
else {
vErrors.push(err145);
}
errors++;
}
if((typeof data65 == "number") && (isFinite(data65))){
if(data65 > 1 || isNaN(data65)){
const err146 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"#/properties/escalation/properties/escalate_from/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err146];
}
else {
vErrors.push(err146);
}
errors++;
}
if(data65 < 0 || isNaN(data65)){
const err147 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"#/properties/escalation/properties/escalate_from/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err147];
}
else {
vErrors.push(err147);
}
errors++;
}
}
}
if(data64.numeric_or_date !== undefined){
if(typeof data64.numeric_or_date !== "boolean"){
const err148 = {instancePath:instancePath+"/escalation/numeric_or_date",schemaPath:"#/properties/escalation/properties/numeric_or_date/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err148];
}
else {
vErrors.push(err148);
}
errors++;
}
}
if(data64.on_error !== undefined){
let data67 = data64.on_error;
if(!((data67 === "escalate") || (data67 === "ignore"))){
const err149 = {instancePath:instancePath+"/escalation/on_error",schemaPath:"#/properties/escalation/properties/on_error/enum",keyword:"enum",params:{allowedValues: schema31.properties.escalation.properties.on_error.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err149];
}
else {
vErrors.push(err149);
}
errors++;
}
}
}
else {
const err150 = {instancePath:instancePath+"/escalation",schemaPath:"#/properties/escalation/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err150];
}
else {
vErrors.push(err150);
}
errors++;
}
}
if(data.threshold_basis !== undefined){
let data68 = data.threshold_basis;
if(typeof data68 === "string"){
if(func2(data68) > 400){
const err151 = {instancePath:instancePath+"/threshold_basis",schemaPath:"#/properties/threshold_basis/maxLength",keyword:"maxLength",params:{limit: 400},message:"must NOT have more than 400 characters"};
if(vErrors === null){
vErrors = [err151];
}
else {
vErrors.push(err151);
}
errors++;
}
}
else {
const err152 = {instancePath:instancePath+"/threshold_basis",schemaPath:"#/properties/threshold_basis/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err152];
}
else {
vErrors.push(err152);
}
errors++;
}
}
if(data.tags !== undefined){
let data69 = data.tags;
if(Array.isArray(data69)){
if(data69.length > 10){
const err153 = {instancePath:instancePath+"/tags",schemaPath:"#/properties/tags/maxItems",keyword:"maxItems",params:{limit: 10},message:"must NOT have more than 10 items"};
if(vErrors === null){
vErrors = [err153];
}
else {
vErrors.push(err153);
}
errors++;
}
const len7 = data69.length;
for(let i11=0; i11<len7; i11++){
let data70 = data69[i11];
if(typeof data70 === "string"){
if(func2(data70) > 32){
const err154 = {instancePath:instancePath+"/tags/" + i11,schemaPath:"#/properties/tags/items/maxLength",keyword:"maxLength",params:{limit: 32},message:"must NOT have more than 32 characters"};
if(vErrors === null){
vErrors = [err154];
}
else {
vErrors.push(err154);
}
errors++;
}
}
else {
const err155 = {instancePath:instancePath+"/tags/" + i11,schemaPath:"#/properties/tags/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err155];
}
else {
vErrors.push(err155);
}
errors++;
}
}
}
else {
const err156 = {instancePath:instancePath+"/tags",schemaPath:"#/properties/tags/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err156];
}
else {
vErrors.push(err156);
}
errors++;
}
}
if(data.parent !== undefined){
let data71 = data.parent;
if((typeof data71 !== "string") && (data71 !== null)){
const err157 = {instancePath:instancePath+"/parent",schemaPath:"#/properties/parent/type",keyword:"type",params:{type: schema31.properties.parent.type},message:"must be string,null"};
if(vErrors === null){
vErrors = [err157];
}
else {
vErrors.push(err157);
}
errors++;
}
if(typeof data71 === "string"){
if(!pattern10.test(data71)){
const err158 = {instancePath:instancePath+"/parent",schemaPath:"#/properties/parent/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,47}@[0-9]+$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,47}@[0-9]+$"+"\""};
if(vErrors === null){
vErrors = [err158];
}
else {
vErrors.push(err158);
}
errors++;
}
}
}
if(data.changelog !== undefined){
let data72 = data.changelog;
if(typeof data72 === "string"){
if(func2(data72) > 2000){
const err159 = {instancePath:instancePath+"/changelog",schemaPath:"#/properties/changelog/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
if(vErrors === null){
vErrors = [err159];
}
else {
vErrors.push(err159);
}
errors++;
}
}
else {
const err160 = {instancePath:instancePath+"/changelog",schemaPath:"#/properties/changelog/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err160];
}
else {
vErrors.push(err160);
}
errors++;
}
}
}
else {
const err161 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err161];
}
else {
vErrors.push(err161);
}
errors++;
}
validate20.errors = vErrors;
return errors === 0;
}
validate20.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};

export const validateEvaluatorDraft = validate29;
const schema34 = {"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://github.com/timkush1/TrustAgent/contracts/schema/evaluator-draft.v1.schema.json","title":"EvaluatorDraft","description":"An LLM-drafted evaluator from Studio (SPEC §2.8): evaluator.v1 without id, version, builtin, parent and changelog, plus rationale, split_suggestions and exactly 6 test samples. Field schemas are referenced from evaluator.v1 so the two cannot drift; the cross-field rules are repeated here.","type":"object","additionalProperties":false,"required":["spec_version","name","category","state_fields","question","evidence","signal","outcomes","rationale","test_samples"],"properties":{"spec_version":{"$ref":"evaluator.v1.schema.json#/properties/spec_version"},"name":{"$ref":"evaluator.v1.schema.json#/properties/name"},"description":{"$ref":"evaluator.v1.schema.json#/properties/description"},"category":{"$ref":"evaluator.v1.schema.json#/properties/category"},"verdict_role":{"$ref":"evaluator.v1.schema.json#/properties/verdict_role"},"state_fields":{"$ref":"evaluator.v1.schema.json#/properties/state_fields"},"params":{"$ref":"evaluator.v1.schema.json#/properties/params"},"question":{"$ref":"evaluator.v1.schema.json#/properties/question"},"evidence":{"$ref":"evaluator.v1.schema.json#/properties/evidence"},"signal":{"$ref":"evaluator.v1.schema.json#/properties/signal"},"outcomes":{"$ref":"evaluator.v1.schema.json#/properties/outcomes"},"escalation":{"$ref":"evaluator.v1.schema.json#/properties/escalation"},"threshold_basis":{"$ref":"evaluator.v1.schema.json#/properties/threshold_basis"},"tags":{"$ref":"evaluator.v1.schema.json#/properties/tags"},"rationale":{"type":"string","minLength":1,"maxLength":600,"description":"Why the drafter chose this question shape (shown in Studio)."},"split_suggestions":{"type":"array","maxItems":5,"items":{"type":"string","minLength":1,"maxLength":600},"description":"Suggestions to split an over-broad check into narrower evaluators."},"test_samples":{"type":"array","minItems":6,"maxItems":6,"items":{"type":"object","additionalProperties":false,"required":["assistant_response","should_fire"],"properties":{"user_request":{"type":"string","maxLength":4000},"assistant_response":{"type":"string","minLength":1,"maxLength":8000},"evidence":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":8000}},"should_fire":{"type":"boolean"}}}}},"allOf":[{"if":{"properties":{"question":{"properties":{"type":{"const":"choice"}}}}},"then":{"properties":{"signal":{"required":["risk_weights"]}}}},{"if":{"properties":{"question":{"properties":{"type":{"const":"score"}}}}},"then":{"properties":{"signal":{"required":["risk_levels"]}}}},{"if":{"properties":{"question":{"properties":{"type":{"const":"noul"}}}}},"then":{"properties":{"signal":{"not":{"anyOf":[{"required":["risk_weights"]},{"required":["risk_levels"]}]}}}}},{"if":{"properties":{"evidence":{"properties":{"requires":{"enum":["required","optional"]}}}}},"then":{"properties":{"state_fields":{"contains":{"const":"evidence"}}}}}]};
const schema35 = {"const":"evaluator/v1"};
const schema36 = {"type":"string","minLength":1,"maxLength":80};
const schema37 = {"type":"string","maxLength":600};
const schema38 = {"enum":["grounding","safety","compliance","quality","security","custom"]};
const schema39 = {"enum":["unsupported","contradicted"]};
const schema40 = {"type":"array","minItems":1,"uniqueItems":true,"items":{"enum":["user_request","assistant_response","evidence","system_instructions","rules"]}};
const schema41 = {"type":"object","maxProperties":8,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{1,31}$"},"additionalProperties":{"type":"object","additionalProperties":false,"required":["default"],"properties":{"default":{"type":"string","maxLength":2000},"label":{"type":"string","maxLength":80},"help":{"type":"string","maxLength":300}}}};
const schema44 = {"type":"object","additionalProperties":false,"required":["requires"],"properties":{"requires":{"enum":["required","optional","none"]},"min_chars":{"type":"integer","minimum":0,"default":40}}};
const schema45 = {"type":"object","additionalProperties":false,"properties":{"invert":{"type":"boolean","default":false},"risk_weights":{"type":"object","minProperties":1,"additionalProperties":{"type":"number","minimum":0,"maximum":1}},"risk_levels":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"integer","minimum":0,"maximum":9}}}};
const schema46 = {"type":"object","additionalProperties":false,"required":["warn_at"],"properties":{"warn_at":{"type":"number","minimum":0,"maximum":1},"critical_at":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"min_confidence":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"block_eligible":{"type":"boolean","default":false},"requires_corroboration":{"type":"array","uniqueItems":true,"items":{"type":"string","pattern":"^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"},"default":[]},"implies":{"type":"array","uniqueItems":true,"items":{"type":"string","pattern":"^[a-z][a-z0-9_]{1,47}$"},"default":[]}}};
const schema47 = {"type":"object","additionalProperties":false,"properties":{"escalate_from":{"type":["number","null"],"minimum":0,"maximum":1,"default":null},"numeric_or_date":{"type":"boolean","default":false},"on_error":{"enum":["escalate","ignore"],"default":"escalate"}}};
const schema48 = {"type":"string","maxLength":400};
const schema49 = {"type":"array","items":{"type":"string","maxLength":32},"maxItems":10};
const schema42 = {"oneOf":[{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"noul"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"object","additionalProperties":false,"required":["true","false"],"properties":{"true":{"$ref":"#/$defs/text"},"false":{"$ref":"#/$defs/text"}}}}},{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"choice"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"object","minProperties":2,"maxProperties":255,"propertyNames":{"pattern":"^[a-z][a-z0-9_]{2,63}$","not":{"enum":["yes","no","other","unknown","none","option1"]}},"additionalProperties":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/option"},{"$ref":"#/$defs/text"}]}}}},{"type":"object","additionalProperties":false,"required":["type","instructions","criteria"],"properties":{"type":{"const":"score"},"instructions":{"$ref":"#/$defs/text"},"criteria":{"type":"array","minItems":2,"maxItems":10,"items":{"$ref":"#/$defs/text"}}}}]};

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
const _errs0 = errors;
let valid0 = false;
let passing0 = null;
const _errs1 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.type === undefined){
const err0 = {instancePath,schemaPath:"#/oneOf/0/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
if(data.instructions === undefined){
const err1 = {instancePath,schemaPath:"#/oneOf/0/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
if(data.criteria === undefined){
const err2 = {instancePath,schemaPath:"#/oneOf/0/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
for(const key0 in data){
if(!(((key0 === "type") || (key0 === "instructions")) || (key0 === "criteria"))){
const err3 = {instancePath,schemaPath:"#/oneOf/0/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
if(data.type !== undefined){
if("noul" !== data.type){
const err4 = {instancePath:instancePath+"/type",schemaPath:"#/oneOf/0/properties/type/const",keyword:"const",params:{allowedValue: "noul"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
if(data.instructions !== undefined){
if(!(validate21(data.instructions, {instancePath:instancePath+"/instructions",parentData:data,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data.criteria !== undefined){
let data2 = data.criteria;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
if(data2.true === undefined){
const err5 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/0/properties/criteria/required",keyword:"required",params:{missingProperty: "true"},message:"must have required property '"+"true"+"'"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
if(data2.false === undefined){
const err6 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/0/properties/criteria/required",keyword:"required",params:{missingProperty: "false"},message:"must have required property '"+"false"+"'"};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
for(const key1 in data2){
if(!((key1 === "true") || (key1 === "false"))){
const err7 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/0/properties/criteria/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key1},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
if(data2.true !== undefined){
if(!(validate21(data2.true, {instancePath:instancePath+"/criteria/true",parentData:data2,parentDataProperty:"true",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data2.false !== undefined){
if(!(validate21(data2.false, {instancePath:instancePath+"/criteria/false",parentData:data2,parentDataProperty:"false",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
}
else {
const err8 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/0/properties/criteria/type",keyword:"type",params:{type: "object"},message:"must be object"};
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
const err9 = {instancePath,schemaPath:"#/oneOf/0/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
var _valid0 = _errs1 === errors;
if(_valid0){
valid0 = true;
passing0 = 0;
var props3 = true;
}
const _errs11 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.type === undefined){
const err10 = {instancePath,schemaPath:"#/oneOf/1/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
if(data.instructions === undefined){
const err11 = {instancePath,schemaPath:"#/oneOf/1/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
if(data.criteria === undefined){
const err12 = {instancePath,schemaPath:"#/oneOf/1/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
for(const key2 in data){
if(!(((key2 === "type") || (key2 === "instructions")) || (key2 === "criteria"))){
const err13 = {instancePath,schemaPath:"#/oneOf/1/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key2},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
}
if(data.type !== undefined){
if("choice" !== data.type){
const err14 = {instancePath:instancePath+"/type",schemaPath:"#/oneOf/1/properties/type/const",keyword:"const",params:{allowedValue: "choice"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
}
if(data.instructions !== undefined){
if(!(validate21(data.instructions, {instancePath:instancePath+"/instructions",parentData:data,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data.criteria !== undefined){
let data7 = data.criteria;
if(data7 && typeof data7 == "object" && !Array.isArray(data7)){
if(Object.keys(data7).length > 255){
const err15 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/maxProperties",keyword:"maxProperties",params:{limit: 255},message:"must NOT have more than 255 properties"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
if(Object.keys(data7).length < 2){
const err16 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/minProperties",keyword:"minProperties",params:{limit: 2},message:"must NOT have fewer than 2 properties"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
for(const key3 in data7){
const _errs18 = errors;
const _errs19 = errors;
const _errs20 = errors;
if(!((((((key3 === "yes") || (key3 === "no")) || (key3 === "other")) || (key3 === "unknown")) || (key3 === "none")) || (key3 === "option1"))){
const err17 = {};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
var valid5 = _errs20 === errors;
if(valid5){
const err18 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/propertyNames/not",keyword:"not",params:{},message:"must NOT be valid",propertyName:key3};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
else {
errors = _errs19;
if(vErrors !== null){
if(_errs19){
vErrors.length = _errs19;
}
else {
vErrors = null;
}
}
}
if(typeof key3 === "string"){
if(!pattern7.test(key3)){
const err19 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{2,63}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{2,63}$"+"\"",propertyName:key3};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
}
var valid4 = _errs18 === errors;
if(!valid4){
const err20 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/propertyNames",keyword:"propertyNames",params:{propertyName: key3},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
}
for(const key4 in data7){
let data8 = data7[key4];
const _errs23 = errors;
let valid7 = false;
const _errs24 = errors;
if(data8 !== null){
const err21 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/oneOf/1/properties/criteria/additionalProperties/anyOf/0/type",keyword:"type",params:{type: "null"},message:"must be null"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
var _valid1 = _errs24 === errors;
valid7 = valid7 || _valid1;
const _errs26 = errors;
if(data8 && typeof data8 == "object" && !Array.isArray(data8)){
if(data8.what === undefined){
const err22 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/required",keyword:"required",params:{missingProperty: "what"},message:"must have required property '"+"what"+"'"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
for(const key5 in data8){
if(!(((key5 === "what") || (key5 === "not_for")) || (key5 === "examples"))){
const err23 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key5},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
}
if(data8.what !== undefined){
let data9 = data8.what;
if(typeof data9 === "string"){
if(func2(data9) > 600){
const err24 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/what",schemaPath:"#/$defs/option/properties/what/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
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
const err25 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/what",schemaPath:"#/$defs/option/properties/what/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
}
if(data8.not_for !== undefined){
let data10 = data8.not_for;
if(typeof data10 === "string"){
if(func2(data10) > 600){
const err26 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/not_for",schemaPath:"#/$defs/option/properties/not_for/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
else {
const err27 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/not_for",schemaPath:"#/$defs/option/properties/not_for/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
if(data8.examples !== undefined){
let data11 = data8.examples;
if(Array.isArray(data11)){
if(data11.length > 2){
const err28 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples",schemaPath:"#/$defs/option/properties/examples/maxItems",keyword:"maxItems",params:{limit: 2},message:"must NOT have more than 2 items"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
const len0 = data11.length;
for(let i0=0; i0<len0; i0++){
let data12 = data11[i0];
if(typeof data12 === "string"){
if(func2(data12) > 300){
const err29 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples/" + i0,schemaPath:"#/$defs/option/properties/examples/items/maxLength",keyword:"maxLength",params:{limit: 300},message:"must NOT have more than 300 characters"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
}
else {
const err30 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples/" + i0,schemaPath:"#/$defs/option/properties/examples/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
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
const err31 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1")+"/examples",schemaPath:"#/$defs/option/properties/examples/type",keyword:"type",params:{type: "array"},message:"must be array"};
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
else {
const err32 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/$defs/option/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
var _valid1 = _errs26 === errors;
valid7 = valid7 || _valid1;
if(_valid1){
var props5 = true;
}
const _errs38 = errors;
if(!(validate21(data8, {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),parentData:data7,parentDataProperty:key4,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
else {
var props6 = validate21.evaluated.props;
}
var _valid1 = _errs38 === errors;
valid7 = valid7 || _valid1;
if(_valid1){
if(props5 !== true && props6 !== undefined){
if(props6 === true){
props5 = true;
}
else {
props5 = props5 || {};
Object.assign(props5, props6);
}
}
}
if(!valid7){
const err33 = {instancePath:instancePath+"/criteria/" + key4.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"#/oneOf/1/properties/criteria/additionalProperties/anyOf",keyword:"anyOf",params:{},message:"must match a schema in anyOf"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
else {
errors = _errs23;
if(vErrors !== null){
if(_errs23){
vErrors.length = _errs23;
}
else {
vErrors = null;
}
}
}
}
}
else {
const err34 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/1/properties/criteria/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
}
else {
const err35 = {instancePath,schemaPath:"#/oneOf/1/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
var _valid0 = _errs11 === errors;
if(_valid0 && valid0){
valid0 = false;
passing0 = [passing0, 1];
}
else {
if(_valid0){
valid0 = true;
passing0 = 1;
if(props3 !== true){
props3 = true;
}
}
const _errs39 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.type === undefined){
const err36 = {instancePath,schemaPath:"#/oneOf/2/required",keyword:"required",params:{missingProperty: "type"},message:"must have required property '"+"type"+"'"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
if(data.instructions === undefined){
const err37 = {instancePath,schemaPath:"#/oneOf/2/required",keyword:"required",params:{missingProperty: "instructions"},message:"must have required property '"+"instructions"+"'"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
}
if(data.criteria === undefined){
const err38 = {instancePath,schemaPath:"#/oneOf/2/required",keyword:"required",params:{missingProperty: "criteria"},message:"must have required property '"+"criteria"+"'"};
if(vErrors === null){
vErrors = [err38];
}
else {
vErrors.push(err38);
}
errors++;
}
for(const key6 in data){
if(!(((key6 === "type") || (key6 === "instructions")) || (key6 === "criteria"))){
const err39 = {instancePath,schemaPath:"#/oneOf/2/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key6},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err39];
}
else {
vErrors.push(err39);
}
errors++;
}
}
if(data.type !== undefined){
if("score" !== data.type){
const err40 = {instancePath:instancePath+"/type",schemaPath:"#/oneOf/2/properties/type/const",keyword:"const",params:{allowedValue: "score"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err40];
}
else {
vErrors.push(err40);
}
errors++;
}
}
if(data.instructions !== undefined){
if(!(validate21(data.instructions, {instancePath:instancePath+"/instructions",parentData:data,parentDataProperty:"instructions",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
if(data.criteria !== undefined){
let data15 = data.criteria;
if(Array.isArray(data15)){
if(data15.length > 10){
const err41 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/2/properties/criteria/maxItems",keyword:"maxItems",params:{limit: 10},message:"must NOT have more than 10 items"};
if(vErrors === null){
vErrors = [err41];
}
else {
vErrors.push(err41);
}
errors++;
}
if(data15.length < 2){
const err42 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/2/properties/criteria/minItems",keyword:"minItems",params:{limit: 2},message:"must NOT have fewer than 2 items"};
if(vErrors === null){
vErrors = [err42];
}
else {
vErrors.push(err42);
}
errors++;
}
const len1 = data15.length;
for(let i1=0; i1<len1; i1++){
if(!(validate21(data15[i1], {instancePath:instancePath+"/criteria/" + i1,parentData:data15,parentDataProperty:i1,rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate21.errors : vErrors.concat(validate21.errors);
errors = vErrors.length;
}
}
}
else {
const err43 = {instancePath:instancePath+"/criteria",schemaPath:"#/oneOf/2/properties/criteria/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err43];
}
else {
vErrors.push(err43);
}
errors++;
}
}
}
else {
const err44 = {instancePath,schemaPath:"#/oneOf/2/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err44];
}
else {
vErrors.push(err44);
}
errors++;
}
var _valid0 = _errs39 === errors;
if(_valid0 && valid0){
valid0 = false;
passing0 = [passing0, 2];
}
else {
if(_valid0){
valid0 = true;
passing0 = 2;
if(props3 !== true){
props3 = true;
}
}
}
}
if(!valid0){
const err45 = {instancePath,schemaPath:"#/oneOf",keyword:"oneOf",params:{passingSchemas: passing0},message:"must match exactly one schema in oneOf"};
if(vErrors === null){
vErrors = [err45];
}
else {
vErrors.push(err45);
}
errors++;
}
else {
errors = _errs0;
if(vErrors !== null){
if(_errs0){
vErrors.length = _errs0;
}
else {
vErrors = null;
}
}
}
validate30.errors = vErrors;
evaluated0.props = props3;
return errors === 0;
}
validate30.evaluated = {"dynamicProps":true,"dynamicItems":false};


function validate29(data, {instancePath="", parentData, parentDataProperty, rootData=data, dynamicAnchors={}}={}){
/*# sourceURL="https://github.com/timkush1/TrustAgent/contracts/schema/evaluator-draft.v1.schema.json" */;
let vErrors = null;
let errors = 0;
const evaluated0 = validate29.evaluated;
if(evaluated0.dynamicProps){
evaluated0.props = undefined;
}
if(evaluated0.dynamicItems){
evaluated0.items = undefined;
}
const _errs2 = errors;
let valid1 = true;
const _errs3 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data0 = data.question;
if(data0 && typeof data0 == "object" && !Array.isArray(data0)){
if(data0.type !== undefined){
if("choice" !== data0.type){
const err0 = {};
if(vErrors === null){
vErrors = [err0];
}
else {
vErrors.push(err0);
}
errors++;
}
}
}
}
}
var _valid0 = _errs3 === errors;
errors = _errs2;
if(vErrors !== null){
if(_errs2){
vErrors.length = _errs2;
}
else {
vErrors = null;
}
}
if(_valid0){
const _errs6 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data2 = data.signal;
if(data2 && typeof data2 == "object" && !Array.isArray(data2)){
if(data2.risk_weights === undefined){
const err1 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/0/then/properties/signal/required",keyword:"required",params:{missingProperty: "risk_weights"},message:"must have required property '"+"risk_weights"+"'"};
if(vErrors === null){
vErrors = [err1];
}
else {
vErrors.push(err1);
}
errors++;
}
}
}
}
var _valid0 = _errs6 === errors;
valid1 = _valid0;
if(valid1){
var props0 = {};
props0.signal = true;
props0.question = true;
}
}
if(!valid1){
const err2 = {instancePath,schemaPath:"#/allOf/0/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err2];
}
else {
vErrors.push(err2);
}
errors++;
}
const _errs9 = errors;
let valid5 = true;
const _errs10 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data3 = data.question;
if(data3 && typeof data3 == "object" && !Array.isArray(data3)){
if(data3.type !== undefined){
if("score" !== data3.type){
const err3 = {};
if(vErrors === null){
vErrors = [err3];
}
else {
vErrors.push(err3);
}
errors++;
}
}
}
}
}
var _valid1 = _errs10 === errors;
errors = _errs9;
if(vErrors !== null){
if(_errs9){
vErrors.length = _errs9;
}
else {
vErrors = null;
}
}
if(_valid1){
const _errs13 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data5 = data.signal;
if(data5 && typeof data5 == "object" && !Array.isArray(data5)){
if(data5.risk_levels === undefined){
const err4 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/1/then/properties/signal/required",keyword:"required",params:{missingProperty: "risk_levels"},message:"must have required property '"+"risk_levels"+"'"};
if(vErrors === null){
vErrors = [err4];
}
else {
vErrors.push(err4);
}
errors++;
}
}
}
}
var _valid1 = _errs13 === errors;
valid5 = _valid1;
if(valid5){
var props1 = {};
props1.signal = true;
props1.question = true;
}
}
if(!valid5){
const err5 = {instancePath,schemaPath:"#/allOf/1/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err5];
}
else {
vErrors.push(err5);
}
errors++;
}
if(props0 !== true && props1 !== undefined){
if(props1 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props1);
}
}
const _errs16 = errors;
let valid9 = true;
const _errs17 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.question !== undefined){
let data6 = data.question;
if(data6 && typeof data6 == "object" && !Array.isArray(data6)){
if(data6.type !== undefined){
if("noul" !== data6.type){
const err6 = {};
if(vErrors === null){
vErrors = [err6];
}
else {
vErrors.push(err6);
}
errors++;
}
}
}
}
}
var _valid2 = _errs17 === errors;
errors = _errs16;
if(vErrors !== null){
if(_errs16){
vErrors.length = _errs16;
}
else {
vErrors = null;
}
}
if(_valid2){
const _errs20 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.signal !== undefined){
let data8 = data.signal;
const _errs22 = errors;
const _errs23 = errors;
const _errs24 = errors;
let valid14 = false;
const _errs25 = errors;
if(data8 && typeof data8 == "object" && !Array.isArray(data8)){
let missing0;
if((data8.risk_weights === undefined) && (missing0 = "risk_weights")){
const err7 = {};
if(vErrors === null){
vErrors = [err7];
}
else {
vErrors.push(err7);
}
errors++;
}
}
var _valid3 = _errs25 === errors;
valid14 = valid14 || _valid3;
const _errs26 = errors;
if(data8 && typeof data8 == "object" && !Array.isArray(data8)){
let missing1;
if((data8.risk_levels === undefined) && (missing1 = "risk_levels")){
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
var _valid3 = _errs26 === errors;
valid14 = valid14 || _valid3;
if(!valid14){
const err9 = {};
if(vErrors === null){
vErrors = [err9];
}
else {
vErrors.push(err9);
}
errors++;
}
else {
errors = _errs24;
if(vErrors !== null){
if(_errs24){
vErrors.length = _errs24;
}
else {
vErrors = null;
}
}
}
var valid13 = _errs23 === errors;
if(valid13){
const err10 = {instancePath:instancePath+"/signal",schemaPath:"#/allOf/2/then/properties/signal/not",keyword:"not",params:{},message:"must NOT be valid"};
if(vErrors === null){
vErrors = [err10];
}
else {
vErrors.push(err10);
}
errors++;
}
else {
errors = _errs22;
if(vErrors !== null){
if(_errs22){
vErrors.length = _errs22;
}
else {
vErrors = null;
}
}
}
}
}
var _valid2 = _errs20 === errors;
valid9 = _valid2;
if(valid9){
var props2 = {};
props2.signal = true;
props2.question = true;
}
}
if(!valid9){
const err11 = {instancePath,schemaPath:"#/allOf/2/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err11];
}
else {
vErrors.push(err11);
}
errors++;
}
if(props0 !== true && props2 !== undefined){
if(props2 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props2);
}
}
const _errs28 = errors;
let valid15 = true;
const _errs29 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.evidence !== undefined){
let data9 = data.evidence;
if(data9 && typeof data9 == "object" && !Array.isArray(data9)){
if(data9.requires !== undefined){
let data10 = data9.requires;
if(!((data10 === "required") || (data10 === "optional"))){
const err12 = {};
if(vErrors === null){
vErrors = [err12];
}
else {
vErrors.push(err12);
}
errors++;
}
}
}
}
}
var _valid4 = _errs29 === errors;
errors = _errs28;
if(vErrors !== null){
if(_errs28){
vErrors.length = _errs28;
}
else {
vErrors = null;
}
}
if(_valid4){
const _errs32 = errors;
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.state_fields !== undefined){
let data11 = data.state_fields;
if(Array.isArray(data11)){
const _errs34 = errors;
const len0 = data11.length;
for(let i0=0; i0<len0; i0++){
const _errs35 = errors;
if("evidence" !== data11[i0]){
const err13 = {instancePath:instancePath+"/state_fields/" + i0,schemaPath:"#/allOf/3/then/properties/state_fields/contains/const",keyword:"const",params:{allowedValue: "evidence"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err13];
}
else {
vErrors.push(err13);
}
errors++;
}
var valid19 = _errs35 === errors;
if(valid19){
break;
}
}
if(!valid19){
const err14 = {instancePath:instancePath+"/state_fields",schemaPath:"#/allOf/3/then/properties/state_fields/contains",keyword:"contains",params:{minContains: 1},message:"must contain at least 1 valid item(s)"};
if(vErrors === null){
vErrors = [err14];
}
else {
vErrors.push(err14);
}
errors++;
}
else {
errors = _errs34;
if(vErrors !== null){
if(_errs34){
vErrors.length = _errs34;
}
else {
vErrors = null;
}
}
}
}
}
}
var _valid4 = _errs32 === errors;
valid15 = _valid4;
if(valid15){
var props3 = {};
props3.state_fields = true;
props3.evidence = true;
}
}
if(!valid15){
const err15 = {instancePath,schemaPath:"#/allOf/3/if",keyword:"if",params:{failingKeyword: "then"},message:"must match \"then\" schema"};
if(vErrors === null){
vErrors = [err15];
}
else {
vErrors.push(err15);
}
errors++;
}
if(props0 !== true && props3 !== undefined){
if(props3 === true){
props0 = true;
}
else {
props0 = props0 || {};
Object.assign(props0, props3);
}
}
if(data && typeof data == "object" && !Array.isArray(data)){
if(data.spec_version === undefined){
const err16 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "spec_version"},message:"must have required property '"+"spec_version"+"'"};
if(vErrors === null){
vErrors = [err16];
}
else {
vErrors.push(err16);
}
errors++;
}
if(data.name === undefined){
const err17 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "name"},message:"must have required property '"+"name"+"'"};
if(vErrors === null){
vErrors = [err17];
}
else {
vErrors.push(err17);
}
errors++;
}
if(data.category === undefined){
const err18 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "category"},message:"must have required property '"+"category"+"'"};
if(vErrors === null){
vErrors = [err18];
}
else {
vErrors.push(err18);
}
errors++;
}
if(data.state_fields === undefined){
const err19 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "state_fields"},message:"must have required property '"+"state_fields"+"'"};
if(vErrors === null){
vErrors = [err19];
}
else {
vErrors.push(err19);
}
errors++;
}
if(data.question === undefined){
const err20 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "question"},message:"must have required property '"+"question"+"'"};
if(vErrors === null){
vErrors = [err20];
}
else {
vErrors.push(err20);
}
errors++;
}
if(data.evidence === undefined){
const err21 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "evidence"},message:"must have required property '"+"evidence"+"'"};
if(vErrors === null){
vErrors = [err21];
}
else {
vErrors.push(err21);
}
errors++;
}
if(data.signal === undefined){
const err22 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "signal"},message:"must have required property '"+"signal"+"'"};
if(vErrors === null){
vErrors = [err22];
}
else {
vErrors.push(err22);
}
errors++;
}
if(data.outcomes === undefined){
const err23 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "outcomes"},message:"must have required property '"+"outcomes"+"'"};
if(vErrors === null){
vErrors = [err23];
}
else {
vErrors.push(err23);
}
errors++;
}
if(data.rationale === undefined){
const err24 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "rationale"},message:"must have required property '"+"rationale"+"'"};
if(vErrors === null){
vErrors = [err24];
}
else {
vErrors.push(err24);
}
errors++;
}
if(data.test_samples === undefined){
const err25 = {instancePath,schemaPath:"#/required",keyword:"required",params:{missingProperty: "test_samples"},message:"must have required property '"+"test_samples"+"'"};
if(vErrors === null){
vErrors = [err25];
}
else {
vErrors.push(err25);
}
errors++;
}
for(const key0 in data){
if(!(func1.call(schema34.properties, key0))){
const err26 = {instancePath,schemaPath:"#/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key0},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err26];
}
else {
vErrors.push(err26);
}
errors++;
}
}
if(data.spec_version !== undefined){
if("evaluator/v1" !== data.spec_version){
const err27 = {instancePath:instancePath+"/spec_version",schemaPath:"evaluator.v1.schema.json#/properties/spec_version/const",keyword:"const",params:{allowedValue: "evaluator/v1"},message:"must be equal to constant"};
if(vErrors === null){
vErrors = [err27];
}
else {
vErrors.push(err27);
}
errors++;
}
}
if(data.name !== undefined){
let data14 = data.name;
if(typeof data14 === "string"){
if(func2(data14) > 80){
const err28 = {instancePath:instancePath+"/name",schemaPath:"evaluator.v1.schema.json#/properties/name/maxLength",keyword:"maxLength",params:{limit: 80},message:"must NOT have more than 80 characters"};
if(vErrors === null){
vErrors = [err28];
}
else {
vErrors.push(err28);
}
errors++;
}
if(func2(data14) < 1){
const err29 = {instancePath:instancePath+"/name",schemaPath:"evaluator.v1.schema.json#/properties/name/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err29];
}
else {
vErrors.push(err29);
}
errors++;
}
}
else {
const err30 = {instancePath:instancePath+"/name",schemaPath:"evaluator.v1.schema.json#/properties/name/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err30];
}
else {
vErrors.push(err30);
}
errors++;
}
}
if(data.description !== undefined){
let data15 = data.description;
if(typeof data15 === "string"){
if(func2(data15) > 600){
const err31 = {instancePath:instancePath+"/description",schemaPath:"evaluator.v1.schema.json#/properties/description/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err31];
}
else {
vErrors.push(err31);
}
errors++;
}
}
else {
const err32 = {instancePath:instancePath+"/description",schemaPath:"evaluator.v1.schema.json#/properties/description/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err32];
}
else {
vErrors.push(err32);
}
errors++;
}
}
if(data.category !== undefined){
let data16 = data.category;
if(!((((((data16 === "grounding") || (data16 === "safety")) || (data16 === "compliance")) || (data16 === "quality")) || (data16 === "security")) || (data16 === "custom"))){
const err33 = {instancePath:instancePath+"/category",schemaPath:"evaluator.v1.schema.json#/properties/category/enum",keyword:"enum",params:{allowedValues: schema38.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err33];
}
else {
vErrors.push(err33);
}
errors++;
}
}
if(data.verdict_role !== undefined){
let data17 = data.verdict_role;
if(!((data17 === "unsupported") || (data17 === "contradicted"))){
const err34 = {instancePath:instancePath+"/verdict_role",schemaPath:"evaluator.v1.schema.json#/properties/verdict_role/enum",keyword:"enum",params:{allowedValues: schema39.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err34];
}
else {
vErrors.push(err34);
}
errors++;
}
}
if(data.state_fields !== undefined){
let data18 = data.state_fields;
if(Array.isArray(data18)){
if(data18.length < 1){
const err35 = {instancePath:instancePath+"/state_fields",schemaPath:"evaluator.v1.schema.json#/properties/state_fields/minItems",keyword:"minItems",params:{limit: 1},message:"must NOT have fewer than 1 items"};
if(vErrors === null){
vErrors = [err35];
}
else {
vErrors.push(err35);
}
errors++;
}
const len1 = data18.length;
for(let i1=0; i1<len1; i1++){
let data19 = data18[i1];
if(!(((((data19 === "user_request") || (data19 === "assistant_response")) || (data19 === "evidence")) || (data19 === "system_instructions")) || (data19 === "rules"))){
const err36 = {instancePath:instancePath+"/state_fields/" + i1,schemaPath:"evaluator.v1.schema.json#/properties/state_fields/items/enum",keyword:"enum",params:{allowedValues: schema40.items.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err36];
}
else {
vErrors.push(err36);
}
errors++;
}
}
let i2 = data18.length;
let j0;
if(i2 > 1){
outer0:
for(;i2--;){
for(j0 = i2; j0--;){
if(func0(data18[i2], data18[j0])){
const err37 = {instancePath:instancePath+"/state_fields",schemaPath:"evaluator.v1.schema.json#/properties/state_fields/uniqueItems",keyword:"uniqueItems",params:{i: i2, j: j0},message:"must NOT have duplicate items (items ## "+j0+" and "+i2+" are identical)"};
if(vErrors === null){
vErrors = [err37];
}
else {
vErrors.push(err37);
}
errors++;
break outer0;
}
}
}
}
}
else {
const err38 = {instancePath:instancePath+"/state_fields",schemaPath:"evaluator.v1.schema.json#/properties/state_fields/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err38];
}
else {
vErrors.push(err38);
}
errors++;
}
}
if(data.params !== undefined){
let data20 = data.params;
if(data20 && typeof data20 == "object" && !Array.isArray(data20)){
if(Object.keys(data20).length > 8){
const err39 = {instancePath:instancePath+"/params",schemaPath:"evaluator.v1.schema.json#/properties/params/maxProperties",keyword:"maxProperties",params:{limit: 8},message:"must NOT have more than 8 properties"};
if(vErrors === null){
vErrors = [err39];
}
else {
vErrors.push(err39);
}
errors++;
}
for(const key1 in data20){
const _errs56 = errors;
if(typeof key1 === "string"){
if(!pattern5.test(key1)){
const err40 = {instancePath:instancePath+"/params",schemaPath:"evaluator.v1.schema.json#/properties/params/propertyNames/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,31}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,31}$"+"\"",propertyName:key1};
if(vErrors === null){
vErrors = [err40];
}
else {
vErrors.push(err40);
}
errors++;
}
}
var valid31 = _errs56 === errors;
if(!valid31){
const err41 = {instancePath:instancePath+"/params",schemaPath:"evaluator.v1.schema.json#/properties/params/propertyNames",keyword:"propertyNames",params:{propertyName: key1},message:"property name must be valid"};
if(vErrors === null){
vErrors = [err41];
}
else {
vErrors.push(err41);
}
errors++;
}
}
for(const key2 in data20){
let data21 = data20[key2];
if(data21 && typeof data21 == "object" && !Array.isArray(data21)){
if(data21.default === undefined){
const err42 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/required",keyword:"required",params:{missingProperty: "default"},message:"must have required property '"+"default"+"'"};
if(vErrors === null){
vErrors = [err42];
}
else {
vErrors.push(err42);
}
errors++;
}
for(const key3 in data21){
if(!(((key3 === "default") || (key3 === "label")) || (key3 === "help"))){
const err43 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key3},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err43];
}
else {
vErrors.push(err43);
}
errors++;
}
}
if(data21.default !== undefined){
let data22 = data21.default;
if(typeof data22 === "string"){
if(func2(data22) > 2000){
const err44 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/default",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/default/maxLength",keyword:"maxLength",params:{limit: 2000},message:"must NOT have more than 2000 characters"};
if(vErrors === null){
vErrors = [err44];
}
else {
vErrors.push(err44);
}
errors++;
}
}
else {
const err45 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/default",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/default/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err45];
}
else {
vErrors.push(err45);
}
errors++;
}
}
if(data21.label !== undefined){
let data23 = data21.label;
if(typeof data23 === "string"){
if(func2(data23) > 80){
const err46 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/label",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/label/maxLength",keyword:"maxLength",params:{limit: 80},message:"must NOT have more than 80 characters"};
if(vErrors === null){
vErrors = [err46];
}
else {
vErrors.push(err46);
}
errors++;
}
}
else {
const err47 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/label",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/label/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err47];
}
else {
vErrors.push(err47);
}
errors++;
}
}
if(data21.help !== undefined){
let data24 = data21.help;
if(typeof data24 === "string"){
if(func2(data24) > 300){
const err48 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/help",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/help/maxLength",keyword:"maxLength",params:{limit: 300},message:"must NOT have more than 300 characters"};
if(vErrors === null){
vErrors = [err48];
}
else {
vErrors.push(err48);
}
errors++;
}
}
else {
const err49 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1")+"/help",schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/properties/help/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err49];
}
else {
vErrors.push(err49);
}
errors++;
}
}
}
else {
const err50 = {instancePath:instancePath+"/params/" + key2.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/params/additionalProperties/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err50];
}
else {
vErrors.push(err50);
}
errors++;
}
}
}
else {
const err51 = {instancePath:instancePath+"/params",schemaPath:"evaluator.v1.schema.json#/properties/params/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err51];
}
else {
vErrors.push(err51);
}
errors++;
}
}
if(data.question !== undefined){
if(!(validate30(data.question, {instancePath:instancePath+"/question",parentData:data,parentDataProperty:"question",rootData,dynamicAnchors}))){
vErrors = vErrors === null ? validate30.errors : vErrors.concat(validate30.errors);
errors = vErrors.length;
}
}
if(data.evidence !== undefined){
let data26 = data.evidence;
if(data26 && typeof data26 == "object" && !Array.isArray(data26)){
if(data26.requires === undefined){
const err52 = {instancePath:instancePath+"/evidence",schemaPath:"evaluator.v1.schema.json#/properties/evidence/required",keyword:"required",params:{missingProperty: "requires"},message:"must have required property '"+"requires"+"'"};
if(vErrors === null){
vErrors = [err52];
}
else {
vErrors.push(err52);
}
errors++;
}
for(const key4 in data26){
if(!((key4 === "requires") || (key4 === "min_chars"))){
const err53 = {instancePath:instancePath+"/evidence",schemaPath:"evaluator.v1.schema.json#/properties/evidence/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key4},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err53];
}
else {
vErrors.push(err53);
}
errors++;
}
}
if(data26.requires !== undefined){
let data27 = data26.requires;
if(!(((data27 === "required") || (data27 === "optional")) || (data27 === "none"))){
const err54 = {instancePath:instancePath+"/evidence/requires",schemaPath:"evaluator.v1.schema.json#/properties/evidence/properties/requires/enum",keyword:"enum",params:{allowedValues: schema44.properties.requires.enum},message:"must be equal to one of the allowed values"};
if(vErrors === null){
vErrors = [err54];
}
else {
vErrors.push(err54);
}
errors++;
}
}
if(data26.min_chars !== undefined){
let data28 = data26.min_chars;
if(!(((typeof data28 == "number") && (!(data28 % 1) && !isNaN(data28))) && (isFinite(data28)))){
const err55 = {instancePath:instancePath+"/evidence/min_chars",schemaPath:"evaluator.v1.schema.json#/properties/evidence/properties/min_chars/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err55];
}
else {
vErrors.push(err55);
}
errors++;
}
if((typeof data28 == "number") && (isFinite(data28))){
if(data28 < 0 || isNaN(data28)){
const err56 = {instancePath:instancePath+"/evidence/min_chars",schemaPath:"evaluator.v1.schema.json#/properties/evidence/properties/min_chars/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err56];
}
else {
vErrors.push(err56);
}
errors++;
}
}
}
}
else {
const err57 = {instancePath:instancePath+"/evidence",schemaPath:"evaluator.v1.schema.json#/properties/evidence/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err57];
}
else {
vErrors.push(err57);
}
errors++;
}
}
if(data.signal !== undefined){
let data29 = data.signal;
if(data29 && typeof data29 == "object" && !Array.isArray(data29)){
for(const key5 in data29){
if(!(((key5 === "invert") || (key5 === "risk_weights")) || (key5 === "risk_levels"))){
const err58 = {instancePath:instancePath+"/signal",schemaPath:"evaluator.v1.schema.json#/properties/signal/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key5},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err58];
}
else {
vErrors.push(err58);
}
errors++;
}
}
if(data29.invert !== undefined){
if(typeof data29.invert !== "boolean"){
const err59 = {instancePath:instancePath+"/signal/invert",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/invert/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err59];
}
else {
vErrors.push(err59);
}
errors++;
}
}
if(data29.risk_weights !== undefined){
let data31 = data29.risk_weights;
if(data31 && typeof data31 == "object" && !Array.isArray(data31)){
if(Object.keys(data31).length < 1){
const err60 = {instancePath:instancePath+"/signal/risk_weights",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_weights/minProperties",keyword:"minProperties",params:{limit: 1},message:"must NOT have fewer than 1 properties"};
if(vErrors === null){
vErrors = [err60];
}
else {
vErrors.push(err60);
}
errors++;
}
for(const key6 in data31){
let data32 = data31[key6];
if((typeof data32 == "number") && (isFinite(data32))){
if(data32 > 1 || isNaN(data32)){
const err61 = {instancePath:instancePath+"/signal/risk_weights/" + key6.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_weights/additionalProperties/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err61];
}
else {
vErrors.push(err61);
}
errors++;
}
if(data32 < 0 || isNaN(data32)){
const err62 = {instancePath:instancePath+"/signal/risk_weights/" + key6.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_weights/additionalProperties/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err62];
}
else {
vErrors.push(err62);
}
errors++;
}
}
else {
const err63 = {instancePath:instancePath+"/signal/risk_weights/" + key6.replace(/~/g, "~0").replace(/\//g, "~1"),schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_weights/additionalProperties/type",keyword:"type",params:{type: "number"},message:"must be number"};
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
else {
const err64 = {instancePath:instancePath+"/signal/risk_weights",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_weights/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err64];
}
else {
vErrors.push(err64);
}
errors++;
}
}
if(data29.risk_levels !== undefined){
let data33 = data29.risk_levels;
if(Array.isArray(data33)){
if(data33.length < 1){
const err65 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/minItems",keyword:"minItems",params:{limit: 1},message:"must NOT have fewer than 1 items"};
if(vErrors === null){
vErrors = [err65];
}
else {
vErrors.push(err65);
}
errors++;
}
const len2 = data33.length;
for(let i3=0; i3<len2; i3++){
let data34 = data33[i3];
if(!(((typeof data34 == "number") && (!(data34 % 1) && !isNaN(data34))) && (isFinite(data34)))){
const err66 = {instancePath:instancePath+"/signal/risk_levels/" + i3,schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/items/type",keyword:"type",params:{type: "integer"},message:"must be integer"};
if(vErrors === null){
vErrors = [err66];
}
else {
vErrors.push(err66);
}
errors++;
}
if((typeof data34 == "number") && (isFinite(data34))){
if(data34 > 9 || isNaN(data34)){
const err67 = {instancePath:instancePath+"/signal/risk_levels/" + i3,schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/items/maximum",keyword:"maximum",params:{comparison: "<=", limit: 9},message:"must be <= 9"};
if(vErrors === null){
vErrors = [err67];
}
else {
vErrors.push(err67);
}
errors++;
}
if(data34 < 0 || isNaN(data34)){
const err68 = {instancePath:instancePath+"/signal/risk_levels/" + i3,schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/items/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err68];
}
else {
vErrors.push(err68);
}
errors++;
}
}
}
let i4 = data33.length;
let j1;
if(i4 > 1){
const indices0 = {};
for(;i4--;){
let item0 = data33[i4];
if(!(((typeof item0 == "number") && (!(item0 % 1) && !isNaN(item0))) && (isFinite(item0)))){
continue;
}
if(typeof indices0[item0] == "number"){
j1 = indices0[item0];
const err69 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/uniqueItems",keyword:"uniqueItems",params:{i: i4, j: j1},message:"must NOT have duplicate items (items ## "+j1+" and "+i4+" are identical)"};
if(vErrors === null){
vErrors = [err69];
}
else {
vErrors.push(err69);
}
errors++;
break;
}
indices0[item0] = i4;
}
}
}
else {
const err70 = {instancePath:instancePath+"/signal/risk_levels",schemaPath:"evaluator.v1.schema.json#/properties/signal/properties/risk_levels/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err70];
}
else {
vErrors.push(err70);
}
errors++;
}
}
}
else {
const err71 = {instancePath:instancePath+"/signal",schemaPath:"evaluator.v1.schema.json#/properties/signal/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err71];
}
else {
vErrors.push(err71);
}
errors++;
}
}
if(data.outcomes !== undefined){
let data35 = data.outcomes;
if(data35 && typeof data35 == "object" && !Array.isArray(data35)){
if(data35.warn_at === undefined){
const err72 = {instancePath:instancePath+"/outcomes",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/required",keyword:"required",params:{missingProperty: "warn_at"},message:"must have required property '"+"warn_at"+"'"};
if(vErrors === null){
vErrors = [err72];
}
else {
vErrors.push(err72);
}
errors++;
}
for(const key7 in data35){
if(!((((((key7 === "warn_at") || (key7 === "critical_at")) || (key7 === "min_confidence")) || (key7 === "block_eligible")) || (key7 === "requires_corroboration")) || (key7 === "implies"))){
const err73 = {instancePath:instancePath+"/outcomes",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key7},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err73];
}
else {
vErrors.push(err73);
}
errors++;
}
}
if(data35.warn_at !== undefined){
let data36 = data35.warn_at;
if((typeof data36 == "number") && (isFinite(data36))){
if(data36 > 1 || isNaN(data36)){
const err74 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/warn_at/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err74];
}
else {
vErrors.push(err74);
}
errors++;
}
if(data36 < 0 || isNaN(data36)){
const err75 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/warn_at/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err75];
}
else {
vErrors.push(err75);
}
errors++;
}
}
else {
const err76 = {instancePath:instancePath+"/outcomes/warn_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/warn_at/type",keyword:"type",params:{type: "number"},message:"must be number"};
if(vErrors === null){
vErrors = [err76];
}
else {
vErrors.push(err76);
}
errors++;
}
}
if(data35.critical_at !== undefined){
let data37 = data35.critical_at;
if((!((typeof data37 == "number") && (isFinite(data37)))) && (data37 !== null)){
const err77 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/critical_at/type",keyword:"type",params:{type: schema46.properties.critical_at.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err77];
}
else {
vErrors.push(err77);
}
errors++;
}
if((typeof data37 == "number") && (isFinite(data37))){
if(data37 > 1 || isNaN(data37)){
const err78 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/critical_at/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err78];
}
else {
vErrors.push(err78);
}
errors++;
}
if(data37 < 0 || isNaN(data37)){
const err79 = {instancePath:instancePath+"/outcomes/critical_at",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/critical_at/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err79];
}
else {
vErrors.push(err79);
}
errors++;
}
}
}
if(data35.min_confidence !== undefined){
let data38 = data35.min_confidence;
if((!((typeof data38 == "number") && (isFinite(data38)))) && (data38 !== null)){
const err80 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/min_confidence/type",keyword:"type",params:{type: schema46.properties.min_confidence.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err80];
}
else {
vErrors.push(err80);
}
errors++;
}
if((typeof data38 == "number") && (isFinite(data38))){
if(data38 > 1 || isNaN(data38)){
const err81 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/min_confidence/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err81];
}
else {
vErrors.push(err81);
}
errors++;
}
if(data38 < 0 || isNaN(data38)){
const err82 = {instancePath:instancePath+"/outcomes/min_confidence",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/min_confidence/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err82];
}
else {
vErrors.push(err82);
}
errors++;
}
}
}
if(data35.block_eligible !== undefined){
if(typeof data35.block_eligible !== "boolean"){
const err83 = {instancePath:instancePath+"/outcomes/block_eligible",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/block_eligible/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err83];
}
else {
vErrors.push(err83);
}
errors++;
}
}
if(data35.requires_corroboration !== undefined){
let data40 = data35.requires_corroboration;
if(Array.isArray(data40)){
const len3 = data40.length;
for(let i5=0; i5<len3; i5++){
let data41 = data40[i5];
if(typeof data41 === "string"){
if(!pattern8.test(data41)){
const err84 = {instancePath:instancePath+"/outcomes/requires_corroboration/" + i5,schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/requires_corroboration/items/pattern",keyword:"pattern",params:{pattern: "^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"},message:"must match pattern \""+"^([a-z][a-z0-9_]{1,47}|det:[a-z_]+)$"+"\""};
if(vErrors === null){
vErrors = [err84];
}
else {
vErrors.push(err84);
}
errors++;
}
}
else {
const err85 = {instancePath:instancePath+"/outcomes/requires_corroboration/" + i5,schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/requires_corroboration/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err85];
}
else {
vErrors.push(err85);
}
errors++;
}
}
let i6 = data40.length;
let j2;
if(i6 > 1){
const indices1 = {};
for(;i6--;){
let item1 = data40[i6];
if(typeof item1 !== "string"){
continue;
}
if(typeof indices1[item1] == "number"){
j2 = indices1[item1];
const err86 = {instancePath:instancePath+"/outcomes/requires_corroboration",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/requires_corroboration/uniqueItems",keyword:"uniqueItems",params:{i: i6, j: j2},message:"must NOT have duplicate items (items ## "+j2+" and "+i6+" are identical)"};
if(vErrors === null){
vErrors = [err86];
}
else {
vErrors.push(err86);
}
errors++;
break;
}
indices1[item1] = i6;
}
}
}
else {
const err87 = {instancePath:instancePath+"/outcomes/requires_corroboration",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/requires_corroboration/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err87];
}
else {
vErrors.push(err87);
}
errors++;
}
}
if(data35.implies !== undefined){
let data42 = data35.implies;
if(Array.isArray(data42)){
const len4 = data42.length;
for(let i7=0; i7<len4; i7++){
let data43 = data42[i7];
if(typeof data43 === "string"){
if(!pattern4.test(data43)){
const err88 = {instancePath:instancePath+"/outcomes/implies/" + i7,schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/implies/items/pattern",keyword:"pattern",params:{pattern: "^[a-z][a-z0-9_]{1,47}$"},message:"must match pattern \""+"^[a-z][a-z0-9_]{1,47}$"+"\""};
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
const err89 = {instancePath:instancePath+"/outcomes/implies/" + i7,schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/implies/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err89];
}
else {
vErrors.push(err89);
}
errors++;
}
}
let i8 = data42.length;
let j3;
if(i8 > 1){
const indices2 = {};
for(;i8--;){
let item2 = data42[i8];
if(typeof item2 !== "string"){
continue;
}
if(typeof indices2[item2] == "number"){
j3 = indices2[item2];
const err90 = {instancePath:instancePath+"/outcomes/implies",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/implies/uniqueItems",keyword:"uniqueItems",params:{i: i8, j: j3},message:"must NOT have duplicate items (items ## "+j3+" and "+i8+" are identical)"};
if(vErrors === null){
vErrors = [err90];
}
else {
vErrors.push(err90);
}
errors++;
break;
}
indices2[item2] = i8;
}
}
}
else {
const err91 = {instancePath:instancePath+"/outcomes/implies",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/properties/implies/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err91];
}
else {
vErrors.push(err91);
}
errors++;
}
}
}
else {
const err92 = {instancePath:instancePath+"/outcomes",schemaPath:"evaluator.v1.schema.json#/properties/outcomes/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err92];
}
else {
vErrors.push(err92);
}
errors++;
}
}
if(data.escalation !== undefined){
let data44 = data.escalation;
if(data44 && typeof data44 == "object" && !Array.isArray(data44)){
for(const key8 in data44){
if(!(((key8 === "escalate_from") || (key8 === "numeric_or_date")) || (key8 === "on_error"))){ // check-forbidden: ignore (the spec field escalation.numeric_or_date)
const err93 = {instancePath:instancePath+"/escalation",schemaPath:"evaluator.v1.schema.json#/properties/escalation/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key8},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err93];
}
else {
vErrors.push(err93);
}
errors++;
}
}
if(data44.escalate_from !== undefined){
let data45 = data44.escalate_from;
if((!((typeof data45 == "number") && (isFinite(data45)))) && (data45 !== null)){
const err94 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"evaluator.v1.schema.json#/properties/escalation/properties/escalate_from/type",keyword:"type",params:{type: schema47.properties.escalate_from.type},message:"must be number,null"};
if(vErrors === null){
vErrors = [err94];
}
else {
vErrors.push(err94);
}
errors++;
}
if((typeof data45 == "number") && (isFinite(data45))){
if(data45 > 1 || isNaN(data45)){
const err95 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"evaluator.v1.schema.json#/properties/escalation/properties/escalate_from/maximum",keyword:"maximum",params:{comparison: "<=", limit: 1},message:"must be <= 1"};
if(vErrors === null){
vErrors = [err95];
}
else {
vErrors.push(err95);
}
errors++;
}
if(data45 < 0 || isNaN(data45)){
const err96 = {instancePath:instancePath+"/escalation/escalate_from",schemaPath:"evaluator.v1.schema.json#/properties/escalation/properties/escalate_from/minimum",keyword:"minimum",params:{comparison: ">=", limit: 0},message:"must be >= 0"};
if(vErrors === null){
vErrors = [err96];
}
else {
vErrors.push(err96);
}
errors++;
}
}
}
if(data44.numeric_or_date !== undefined){
if(typeof data44.numeric_or_date !== "boolean"){
const err97 = {instancePath:instancePath+"/escalation/numeric_or_date",schemaPath:"evaluator.v1.schema.json#/properties/escalation/properties/numeric_or_date/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err97];
}
else {
vErrors.push(err97);
}
errors++;
}
}
if(data44.on_error !== undefined){
let data47 = data44.on_error;
if(!((data47 === "escalate") || (data47 === "ignore"))){
const err98 = {instancePath:instancePath+"/escalation/on_error",schemaPath:"evaluator.v1.schema.json#/properties/escalation/properties/on_error/enum",keyword:"enum",params:{allowedValues: schema47.properties.on_error.enum},message:"must be equal to one of the allowed values"};
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
const err99 = {instancePath:instancePath+"/escalation",schemaPath:"evaluator.v1.schema.json#/properties/escalation/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err99];
}
else {
vErrors.push(err99);
}
errors++;
}
}
if(data.threshold_basis !== undefined){
let data48 = data.threshold_basis;
if(typeof data48 === "string"){
if(func2(data48) > 400){
const err100 = {instancePath:instancePath+"/threshold_basis",schemaPath:"evaluator.v1.schema.json#/properties/threshold_basis/maxLength",keyword:"maxLength",params:{limit: 400},message:"must NOT have more than 400 characters"};
if(vErrors === null){
vErrors = [err100];
}
else {
vErrors.push(err100);
}
errors++;
}
}
else {
const err101 = {instancePath:instancePath+"/threshold_basis",schemaPath:"evaluator.v1.schema.json#/properties/threshold_basis/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err101];
}
else {
vErrors.push(err101);
}
errors++;
}
}
if(data.tags !== undefined){
let data49 = data.tags;
if(Array.isArray(data49)){
if(data49.length > 10){
const err102 = {instancePath:instancePath+"/tags",schemaPath:"evaluator.v1.schema.json#/properties/tags/maxItems",keyword:"maxItems",params:{limit: 10},message:"must NOT have more than 10 items"};
if(vErrors === null){
vErrors = [err102];
}
else {
vErrors.push(err102);
}
errors++;
}
const len5 = data49.length;
for(let i9=0; i9<len5; i9++){
let data50 = data49[i9];
if(typeof data50 === "string"){
if(func2(data50) > 32){
const err103 = {instancePath:instancePath+"/tags/" + i9,schemaPath:"evaluator.v1.schema.json#/properties/tags/items/maxLength",keyword:"maxLength",params:{limit: 32},message:"must NOT have more than 32 characters"};
if(vErrors === null){
vErrors = [err103];
}
else {
vErrors.push(err103);
}
errors++;
}
}
else {
const err104 = {instancePath:instancePath+"/tags/" + i9,schemaPath:"evaluator.v1.schema.json#/properties/tags/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err104];
}
else {
vErrors.push(err104);
}
errors++;
}
}
}
else {
const err105 = {instancePath:instancePath+"/tags",schemaPath:"evaluator.v1.schema.json#/properties/tags/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err105];
}
else {
vErrors.push(err105);
}
errors++;
}
}
if(data.rationale !== undefined){
let data51 = data.rationale;
if(typeof data51 === "string"){
if(func2(data51) > 600){
const err106 = {instancePath:instancePath+"/rationale",schemaPath:"#/properties/rationale/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err106];
}
else {
vErrors.push(err106);
}
errors++;
}
if(func2(data51) < 1){
const err107 = {instancePath:instancePath+"/rationale",schemaPath:"#/properties/rationale/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err107];
}
else {
vErrors.push(err107);
}
errors++;
}
}
else {
const err108 = {instancePath:instancePath+"/rationale",schemaPath:"#/properties/rationale/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err108];
}
else {
vErrors.push(err108);
}
errors++;
}
}
if(data.split_suggestions !== undefined){
let data52 = data.split_suggestions;
if(Array.isArray(data52)){
if(data52.length > 5){
const err109 = {instancePath:instancePath+"/split_suggestions",schemaPath:"#/properties/split_suggestions/maxItems",keyword:"maxItems",params:{limit: 5},message:"must NOT have more than 5 items"};
if(vErrors === null){
vErrors = [err109];
}
else {
vErrors.push(err109);
}
errors++;
}
const len6 = data52.length;
for(let i10=0; i10<len6; i10++){
let data53 = data52[i10];
if(typeof data53 === "string"){
if(func2(data53) > 600){
const err110 = {instancePath:instancePath+"/split_suggestions/" + i10,schemaPath:"#/properties/split_suggestions/items/maxLength",keyword:"maxLength",params:{limit: 600},message:"must NOT have more than 600 characters"};
if(vErrors === null){
vErrors = [err110];
}
else {
vErrors.push(err110);
}
errors++;
}
if(func2(data53) < 1){
const err111 = {instancePath:instancePath+"/split_suggestions/" + i10,schemaPath:"#/properties/split_suggestions/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err111];
}
else {
vErrors.push(err111);
}
errors++;
}
}
else {
const err112 = {instancePath:instancePath+"/split_suggestions/" + i10,schemaPath:"#/properties/split_suggestions/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err112];
}
else {
vErrors.push(err112);
}
errors++;
}
}
}
else {
const err113 = {instancePath:instancePath+"/split_suggestions",schemaPath:"#/properties/split_suggestions/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err113];
}
else {
vErrors.push(err113);
}
errors++;
}
}
if(data.test_samples !== undefined){
let data54 = data.test_samples;
if(Array.isArray(data54)){
if(data54.length > 6){
const err114 = {instancePath:instancePath+"/test_samples",schemaPath:"#/properties/test_samples/maxItems",keyword:"maxItems",params:{limit: 6},message:"must NOT have more than 6 items"};
if(vErrors === null){
vErrors = [err114];
}
else {
vErrors.push(err114);
}
errors++;
}
if(data54.length < 6){
const err115 = {instancePath:instancePath+"/test_samples",schemaPath:"#/properties/test_samples/minItems",keyword:"minItems",params:{limit: 6},message:"must NOT have fewer than 6 items"};
if(vErrors === null){
vErrors = [err115];
}
else {
vErrors.push(err115);
}
errors++;
}
const len7 = data54.length;
for(let i11=0; i11<len7; i11++){
let data55 = data54[i11];
if(data55 && typeof data55 == "object" && !Array.isArray(data55)){
if(data55.assistant_response === undefined){
const err116 = {instancePath:instancePath+"/test_samples/" + i11,schemaPath:"#/properties/test_samples/items/required",keyword:"required",params:{missingProperty: "assistant_response"},message:"must have required property '"+"assistant_response"+"'"};
if(vErrors === null){
vErrors = [err116];
}
else {
vErrors.push(err116);
}
errors++;
}
if(data55.should_fire === undefined){
const err117 = {instancePath:instancePath+"/test_samples/" + i11,schemaPath:"#/properties/test_samples/items/required",keyword:"required",params:{missingProperty: "should_fire"},message:"must have required property '"+"should_fire"+"'"};
if(vErrors === null){
vErrors = [err117];
}
else {
vErrors.push(err117);
}
errors++;
}
for(const key9 in data55){
if(!((((key9 === "user_request") || (key9 === "assistant_response")) || (key9 === "evidence")) || (key9 === "should_fire"))){
const err118 = {instancePath:instancePath+"/test_samples/" + i11,schemaPath:"#/properties/test_samples/items/additionalProperties",keyword:"additionalProperties",params:{additionalProperty: key9},message:"must NOT have additional properties"};
if(vErrors === null){
vErrors = [err118];
}
else {
vErrors.push(err118);
}
errors++;
}
}
if(data55.user_request !== undefined){
let data56 = data55.user_request;
if(typeof data56 === "string"){
if(func2(data56) > 4000){
const err119 = {instancePath:instancePath+"/test_samples/" + i11+"/user_request",schemaPath:"#/properties/test_samples/items/properties/user_request/maxLength",keyword:"maxLength",params:{limit: 4000},message:"must NOT have more than 4000 characters"};
if(vErrors === null){
vErrors = [err119];
}
else {
vErrors.push(err119);
}
errors++;
}
}
else {
const err120 = {instancePath:instancePath+"/test_samples/" + i11+"/user_request",schemaPath:"#/properties/test_samples/items/properties/user_request/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err120];
}
else {
vErrors.push(err120);
}
errors++;
}
}
if(data55.assistant_response !== undefined){
let data57 = data55.assistant_response;
if(typeof data57 === "string"){
if(func2(data57) > 8000){
const err121 = {instancePath:instancePath+"/test_samples/" + i11+"/assistant_response",schemaPath:"#/properties/test_samples/items/properties/assistant_response/maxLength",keyword:"maxLength",params:{limit: 8000},message:"must NOT have more than 8000 characters"};
if(vErrors === null){
vErrors = [err121];
}
else {
vErrors.push(err121);
}
errors++;
}
if(func2(data57) < 1){
const err122 = {instancePath:instancePath+"/test_samples/" + i11+"/assistant_response",schemaPath:"#/properties/test_samples/items/properties/assistant_response/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err122];
}
else {
vErrors.push(err122);
}
errors++;
}
}
else {
const err123 = {instancePath:instancePath+"/test_samples/" + i11+"/assistant_response",schemaPath:"#/properties/test_samples/items/properties/assistant_response/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err123];
}
else {
vErrors.push(err123);
}
errors++;
}
}
if(data55.evidence !== undefined){
let data58 = data55.evidence;
if(Array.isArray(data58)){
if(data58.length > 8){
const err124 = {instancePath:instancePath+"/test_samples/" + i11+"/evidence",schemaPath:"#/properties/test_samples/items/properties/evidence/maxItems",keyword:"maxItems",params:{limit: 8},message:"must NOT have more than 8 items"};
if(vErrors === null){
vErrors = [err124];
}
else {
vErrors.push(err124);
}
errors++;
}
const len8 = data58.length;
for(let i12=0; i12<len8; i12++){
let data59 = data58[i12];
if(typeof data59 === "string"){
if(func2(data59) > 8000){
const err125 = {instancePath:instancePath+"/test_samples/" + i11+"/evidence/" + i12,schemaPath:"#/properties/test_samples/items/properties/evidence/items/maxLength",keyword:"maxLength",params:{limit: 8000},message:"must NOT have more than 8000 characters"};
if(vErrors === null){
vErrors = [err125];
}
else {
vErrors.push(err125);
}
errors++;
}
if(func2(data59) < 1){
const err126 = {instancePath:instancePath+"/test_samples/" + i11+"/evidence/" + i12,schemaPath:"#/properties/test_samples/items/properties/evidence/items/minLength",keyword:"minLength",params:{limit: 1},message:"must NOT have fewer than 1 characters"};
if(vErrors === null){
vErrors = [err126];
}
else {
vErrors.push(err126);
}
errors++;
}
}
else {
const err127 = {instancePath:instancePath+"/test_samples/" + i11+"/evidence/" + i12,schemaPath:"#/properties/test_samples/items/properties/evidence/items/type",keyword:"type",params:{type: "string"},message:"must be string"};
if(vErrors === null){
vErrors = [err127];
}
else {
vErrors.push(err127);
}
errors++;
}
}
}
else {
const err128 = {instancePath:instancePath+"/test_samples/" + i11+"/evidence",schemaPath:"#/properties/test_samples/items/properties/evidence/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err128];
}
else {
vErrors.push(err128);
}
errors++;
}
}
if(data55.should_fire !== undefined){
if(typeof data55.should_fire !== "boolean"){
const err129 = {instancePath:instancePath+"/test_samples/" + i11+"/should_fire",schemaPath:"#/properties/test_samples/items/properties/should_fire/type",keyword:"type",params:{type: "boolean"},message:"must be boolean"};
if(vErrors === null){
vErrors = [err129];
}
else {
vErrors.push(err129);
}
errors++;
}
}
}
else {
const err130 = {instancePath:instancePath+"/test_samples/" + i11,schemaPath:"#/properties/test_samples/items/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err130];
}
else {
vErrors.push(err130);
}
errors++;
}
}
}
else {
const err131 = {instancePath:instancePath+"/test_samples",schemaPath:"#/properties/test_samples/type",keyword:"type",params:{type: "array"},message:"must be array"};
if(vErrors === null){
vErrors = [err131];
}
else {
vErrors.push(err131);
}
errors++;
}
}
}
else {
const err132 = {instancePath,schemaPath:"#/type",keyword:"type",params:{type: "object"},message:"must be object"};
if(vErrors === null){
vErrors = [err132];
}
else {
vErrors.push(err132);
}
errors++;
}
validate29.errors = vErrors;
return errors === 0;
}
validate29.evaluated = {"props":true,"dynamicProps":false,"dynamicItems":false};
