package moddata

import (
	"os"
	"path/filepath"
	"testing"
)

// Smoke test against the in-repo ATM10 fixture (has Mekanism). Skips if the
// fixture isn't present so it's safe to run anywhere.
func TestModLangSmoke(t *testing.T) {
	serverDir := filepath.Join("..", "..", "test-run", "data", "servers", "All the Mods 10 - ATM10")
	if _, err := os.Stat(filepath.Join(serverDir, "mods")); err != nil {
		t.Skip("ATM10 fixture not present; skipping")
	}
	dataDir := t.TempDir()

	res := HandleModLang(serverDir, "test-uuid", dataDir, map[string]interface{}{"modId": "mekanism"})
	if errStr, _ := res["error"].(string); errStr != "" {
		t.Fatalf("error: %s", errStr)
	}
	lang, _ := res["lang"].(map[string]string)
	if len(lang) == 0 {
		t.Fatalf("empty lang map")
	}
	t.Logf("extracted %d keys", len(lang))

	checks := []string{
		"configuration.mekanism.general.miner.max_radius",
		"block.mekanism.digital_miner",
		"miner.mekanism.radius",
	}
	for _, k := range checks {
		if v, ok := lang[k]; ok {
			t.Logf("  %s => %q", k, v)
		} else {
			t.Errorf("missing expected key %q", k)
		}
	}
	if _, dropped := lang["_mekanism_force_utf8"]; dropped {
		t.Errorf("metadata key _mekanism_force_utf8 should have been filtered")
	}

	// Second call should hit the disk cache.
	res2 := HandleModLang(serverDir, "test-uuid", dataDir, map[string]interface{}{"modId": "mekanism"})
	if cached, _ := res2["cached"].(bool); !cached {
		t.Errorf("expected second call to be cached")
	}
}

func TestModIndexSmoke(t *testing.T) {
	serverDir := filepath.Join("..", "..", "test-run", "data", "servers", "All the Mods 10 - ATM10")
	if _, err := os.Stat(filepath.Join(serverDir, "mods")); err != nil {
		t.Skip("ATM10 fixture not present; skipping")
	}
	dataDir := t.TempDir()

	build := HandleModIndexBuild(serverDir, "test-uuid", dataDir, map[string]interface{}{"force": true})
	if errStr, _ := build["error"].(string); errStr != "" {
		t.Fatalf("build error: %s", errStr)
	}
	itemCount, _ := build["itemCount"].(int)
	recipeCount, _ := build["recipeCount"].(int)
	t.Logf("indexed %d items, %d recipes", itemCount, recipeCount)
	if itemCount == 0 || recipeCount == 0 {
		t.Fatalf("expected nonzero items and recipes, got %d / %d", itemCount, recipeCount)
	}

	// Search for a known item.
	search := HandleModItemsSearch(serverDir, "test-uuid", dataDir, map[string]interface{}{"q": "enriched iron"})
	items, _ := search["items"].([]Item)
	var found *Item
	for i := range items {
		if items[i].ID == "mekanism:enriched_iron" {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("mekanism:enriched_iron not found in search results")
	}
	t.Logf("found %s (%q) hasIcon=%v", found.ID, found.Name, found.HasIcon)

	// Recipe lookup — enriched iron is made via metallurgic infusing.
	rec := HandleModItemRecipes(serverDir, "test-uuid", dataDir, map[string]interface{}{"id": "mekanism:enriched_iron"})
	makes, _ := rec["makes"].([]Recipe)
	if len(makes) == 0 {
		t.Fatalf("expected at least one recipe making enriched_iron")
	}
	var hasInfusing bool
	for _, r := range makes {
		t.Logf("  makes via %s: in=%v out=%v", r.Type, r.Inputs, r.Outputs)
		if r.Type == "mekanism:metallurgic_infusing" {
			hasInfusing = true
		}
	}
	if !hasInfusing {
		t.Errorf("expected a mekanism:metallurgic_infusing recipe for enriched_iron")
	}

	// The infusing recipe consumes tag c:ingots/iron — it should resolve to items.
	tagsMap, _ := rec["tags"].(map[string][]string)
	if items := tagsMap["c:ingots/iron"]; len(items) == 0 {
		t.Errorf("expected c:ingots/iron tag to resolve to member items, got none (tags=%d)", len(tagsMap))
	} else {
		t.Logf("c:ingots/iron resolves to %d items, e.g. %s", len(items), items[0])
	}

	// Icon should be retrievable as base64 (enriched_iron is a flat item).
	icon := HandleModIcon(serverDir, "test-uuid", dataDir, map[string]interface{}{"id": "mekanism:enriched_iron"})
	if content, _ := icon["content"].(string); content == "" {
		t.Logf("note: no icon resolved for enriched_iron (icon=%v)", icon)
	}
}
