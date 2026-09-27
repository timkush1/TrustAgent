// Package contracts embeds the TrustAgent contract files: JSON Schemas, the configuration key
// registry, evaluator templates, question sets, policies, rules, prompts, pricing, value claims,
// demo scenarios, UI copy, fixtures and golden vectors.
//
// Go is the reference implementation of the contracts (SPEC §7.6). backend-go depends on this
// module through a replace directive:
//
//	require github.com/truthtable/contracts v0.0.0
//	replace github.com/truthtable/contracts => ../contracts
//
// and reads files from FS with paths relative to the contracts directory, for example
// "config/keys.v1.json" or "schema/evaluator.v1.schema.json".
package contracts

import "embed"

// FS holds every contract directory. The all: prefix keeps the .gitkeep placeholders, so the
// embed pattern stays valid while a directory is still empty.
//
//go:embed all:schema all:config all:evaluators all:question-sets all:cascade all:policies all:rules
//go:embed all:prompts all:pricing all:claims all:demo all:ui all:fixtures all:vectors
//go:embed superseded.v1.json
var FS embed.FS

// SchemaBaseURI is the $id prefix of every schema in schema/. A schema's $id is
// SchemaBaseURI + "<name>.schema.json", so $ref values resolve against files in schema/.
const SchemaBaseURI = "https://github.com/timkush1/TrustAgent/contracts/schema/"
