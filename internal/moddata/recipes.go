package moddata

import (
	"encoding/json"
	"strings"
)

// Ingredient is a single item/tag reference with an optional count. Used for
// both recipe inputs and outputs (outputs always carry Item, never Tag).
type Ingredient struct {
	Item  string `json:"item,omitempty"`
	Tag   string `json:"tag,omitempty"`
	Count int    `json:"count,omitempty"`
}

// Recipe is a normalized, render-ready recipe.
type Recipe struct {
	Type    string                `json:"type"`
	Machine string                `json:"machine,omitempty"`
	Inputs  []Ingredient          `json:"inputs"`
	Outputs []Ingredient          `json:"outputs"`
	Pattern []string              `json:"pattern,omitempty"` // shaped crafting
	Key     map[string]Ingredient `json:"key,omitempty"`     // shaped crafting
	Auto    bool                  `json:"auto,omitempty"`    // matched by generic fallback
}

// recipeShape names the input/output fields for a known recipe type.
type recipeShape struct {
	inputs  []string
	outputs []string
}

// recipeRegistry maps known recipe types to their input/output field names.
// Unregistered types fall back to a generic field scan (see parseRecipe).
var recipeRegistry = map[string]recipeShape{
	// Vanilla cooking / cutting / smithing
	"minecraft:smelting":           {inputs: []string{"ingredient"}, outputs: []string{"result"}},
	"minecraft:blasting":           {inputs: []string{"ingredient"}, outputs: []string{"result"}},
	"minecraft:smoking":            {inputs: []string{"ingredient"}, outputs: []string{"result"}},
	"minecraft:campfire_cooking":   {inputs: []string{"ingredient"}, outputs: []string{"result"}},
	"minecraft:stonecutting":       {inputs: []string{"ingredient"}, outputs: []string{"result"}},
	"minecraft:smithing_transform": {inputs: []string{"template", "base", "addition"}, outputs: []string{"result"}},
	"minecraft:smithing_trim":      {inputs: []string{"template", "base", "addition"}, outputs: []string{"result"}},
	// Mekanism machines
	"mekanism:enriching":            {inputs: []string{"input"}, outputs: []string{"output"}},
	"mekanism:crushing":             {inputs: []string{"input"}, outputs: []string{"output"}},
	"mekanism:smelting":             {inputs: []string{"input"}, outputs: []string{"output"}},
	"mekanism:sawing":               {inputs: []string{"input"}, outputs: []string{"main_output", "secondary_output"}},
	"mekanism:compressing":          {inputs: []string{"item_input", "chemical_input"}, outputs: []string{"output"}},
	"mekanism:combining":            {inputs: []string{"main_input", "extra_input"}, outputs: []string{"output"}},
	"mekanism:purifying":            {inputs: []string{"item_input", "chemical_input"}, outputs: []string{"output"}},
	"mekanism:injecting":            {inputs: []string{"item_input", "chemical_input"}, outputs: []string{"output"}},
	"mekanism:metallurgic_infusing": {inputs: []string{"item_input", "chemical_input"}, outputs: []string{"output"}},
	"mekanism:chemical_conversion":  {inputs: []string{"input"}, outputs: []string{"output"}},
	"mekanism:pigment_extracting":   {inputs: []string{"input"}, outputs: []string{"output"}},
}

// recipeSkip lists non-craftable internal recipe types to ignore.
var recipeSkip = map[string]bool{
	"mekanism:mek_data":            true,
	"mekanism:bin_insert":          true,
	"mekanism:bin_extract":         true,
	"mekanism:clear_configuration": true,
}

// parseRecipe normalizes one recipe JSON document. Returns ok=false for
// skipped/unparseable/empty recipes or those gated on an absent mod.
func parseRecipe(data []byte, presentMods map[string]bool) (*Recipe, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return parseRecipeMap(m, presentMods)
}

func parseRecipeMap(m map[string]interface{}, presentMods map[string]bool) (*Recipe, bool) {
	// Unwrap NeoForge conditional wrappers.
	if inner, ok := m["recipe"].(map[string]interface{}); ok {
		if t, _ := m["type"].(string); t == "neoforge:conditional" || t == "forge:conditional" {
			return parseRecipeMap(inner, presentMods)
		}
	}
	if !conditionsMet(m["neoforge:conditions"], presentMods) {
		return nil, false
	}

	typ, _ := m["type"].(string)
	if typ == "" || recipeSkip[typ] {
		return nil, false
	}

	rec := &Recipe{Type: typ, Machine: machineLabel(typ)}

	switch typ {
	case "minecraft:crafting_shaped":
		if pat, ok := toStringSlice(m["pattern"]); ok {
			rec.Pattern = pat
		}
		rec.Key = map[string]Ingredient{}
		if key, ok := m["key"].(map[string]interface{}); ok {
			for sym, v := range key {
				var ings []Ingredient
				collectIngredients(v, &ings)
				if len(ings) > 0 {
					rec.Key[sym] = ings[0]
				}
			}
		}
		collectOutputs(m["result"], &rec.Outputs)
	case "minecraft:crafting_shapeless":
		collectIngredients(m["ingredients"], &rec.Inputs)
		collectOutputs(m["result"], &rec.Outputs)
	default:
		if shape, known := recipeRegistry[typ]; known {
			for _, f := range shape.inputs {
				collectIngredients(m[f], &rec.Inputs)
			}
			for _, f := range shape.outputs {
				collectOutputs(m[f], &rec.Outputs)
			}
		} else {
			// Generic fallback: result-ish fields are outputs, everything else
			// with item/tag refs are inputs.
			rec.Auto = true
			for k, v := range m {
				if k == "type" || strings.HasPrefix(k, "neoforge:") || strings.HasPrefix(k, "forge:") {
					continue
				}
				if strings.Contains(strings.ToLower(k), "result") || strings.Contains(strings.ToLower(k), "output") {
					collectOutputs(v, &rec.Outputs)
				} else {
					collectIngredients(v, &rec.Inputs)
				}
			}
		}
	}

	if len(rec.Inputs) == 0 && len(rec.Key) == 0 && len(rec.Outputs) == 0 {
		return nil, false
	}
	return rec, true
}

// conditionsMet returns false only when a mod_loaded condition names a mod that
// isn't installed. Other condition types are treated as satisfied.
func conditionsMet(v interface{}, presentMods map[string]bool) bool {
	conds, ok := v.([]interface{})
	if !ok {
		return true
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		if ctype == "neoforge:mod_loaded" || ctype == "forge:mod_loaded" {
			modid, _ := cm["modid"].(string)
			if modid != "" && !presentMods[modid] {
				return false
			}
		}
	}
	return true
}

// collectIngredients walks arbitrary JSON pulling out {item|tag} refs (with an
// optional count/amount). Doubles as the generic fallback's input scanner.
func collectIngredients(v interface{}, out *[]Ingredient) {
	switch t := v.(type) {
	case map[string]interface{}:
		ing := Ingredient{}
		if s, ok := t["item"].(string); ok {
			ing.Item = s
		}
		if s, ok := t["tag"].(string); ok {
			ing.Tag = s
		}
		if ing.Item != "" || ing.Tag != "" {
			ing.Count = intField(t, "count", "amount")
			*out = append(*out, ing)
			return
		}
		for _, vv := range t {
			collectIngredients(vv, out)
		}
	case []interface{}:
		for _, vv := range t {
			collectIngredients(vv, out)
		}
	case string:
		// Bare string ingredient (rare/legacy): treat as an item id.
		if strings.Contains(t, ":") {
			*out = append(*out, Ingredient{Item: t})
		}
	}
}

// collectOutputs walks JSON pulling out result refs, which use "id" (or "item").
func collectOutputs(v interface{}, out *[]Ingredient) {
	switch t := v.(type) {
	case map[string]interface{}:
		if s, ok := t["id"].(string); ok {
			*out = append(*out, Ingredient{Item: s, Count: intField(t, "count", "amount")})
			return
		}
		if s, ok := t["item"].(string); ok {
			*out = append(*out, Ingredient{Item: s, Count: intField(t, "count", "amount")})
			return
		}
		for _, vv := range t {
			collectOutputs(vv, out)
		}
	case []interface{}:
		for _, vv := range t {
			collectOutputs(vv, out)
		}
	case string:
		if strings.Contains(t, ":") {
			*out = append(*out, Ingredient{Item: t})
		}
	}
}

func intField(m map[string]interface{}, keys ...string) int {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int(f)
		}
	}
	return 0
}

func toStringSlice(v interface{}) ([]string, bool) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

// machineLabel turns "mekanism:metallurgic_infusing" into "Metallurgic Infusing".
func machineLabel(typ string) string {
	_, path := splitID(typ)
	return prettify(path)
}
