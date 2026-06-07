package registry

import "testing"

func TestMergeModelsCatalogPreservesLocalKilocode(t *testing.T) {
	current := &staticModelsJSON{
		Kilocode: []*ModelInfo{{ID: "moonshotai/kimi-k2.6"}},
		Kimi:     []*ModelInfo{{ID: "kimi-k2.6"}},
	}
	remote := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2.6"}},
	}

	merged := mergeModelsCatalog(current, remote)
	if merged == nil {
		t.Fatal("expected merged catalog")
	}
	if len(merged.Kilocode) != 1 || merged.Kilocode[0].ID != "moonshotai/kimi-k2.6" {
		t.Fatalf("expected local kilocode models to be preserved, got %#v", merged.Kilocode)
	}
	if len(merged.Kimi) != 1 || merged.Kimi[0].ID != "kimi-k2.6" {
		t.Fatalf("expected remote kimi models, got %#v", merged.Kimi)
	}
}

func TestMergeModelsCatalogPrefersRemoteKilocode(t *testing.T) {
	current := &staticModelsJSON{
		Kilocode: []*ModelInfo{{ID: "old-model"}},
	}
	remote := &staticModelsJSON{
		Kilocode: []*ModelInfo{{ID: "new-model"}},
	}

	merged := mergeModelsCatalog(current, remote)
	if len(merged.Kilocode) != 1 || merged.Kilocode[0].ID != "new-model" {
		t.Fatalf("expected remote kilocode models to win, got %#v", merged.Kilocode)
	}
}
