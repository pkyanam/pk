package state

// Model describes a single model entry in the provider catalog.
type Model struct {
	ID       string
	Provider string
	Context  int64
	Output   int64
	CostIn   float64
	CostOut  float64
	Disabled bool
}

// Catalog maps provider IDs to their models. This is the first real State in
// v2 (see docs/v2-architecture.md, "Provider catalog State"): sources such
// as a models.dev-style refresh, provider policy, and limits each register a
// transformation, and reload rebuilds the catalog deterministically.
type Catalog map[string][]Model

// NewCatalog returns a State seeded with an empty catalog.
func NewCatalog() *State[Catalog] {
	return New[Catalog](Catalog{})
}

// TransformCatalog turns a data source into a transformation. load is the
// source (e.g. a models.dev-style refresh); each Rebuild re-reads it, so a
// reload picks up the latest data. The incoming catalog is intentionally
// ignored: the data source replaces the catalog wholesale.
func TransformCatalog(load func() (Catalog, error)) Transform[Catalog] {
	return func(Catalog) (Catalog, error) {
		return load()
	}
}

// DisableProviders marks every model of the excluded providers Disabled. It
// is idempotent: applying it more than once has no additional effect.
func DisableProviders(excluded []string) Transform[Catalog] {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, p := range excluded {
		excludedSet[p] = struct{}{}
	}
	return func(cat Catalog) (Catalog, error) {
		out := cloneCatalog(cat)
		for provider, models := range out {
			if _, ok := excludedSet[provider]; !ok {
				continue
			}
			for i := range models {
				models[i].Disabled = true
			}
		}
		return out, nil
	}
}

// HalveLimits halves each model's context and output limits.
//
// The exact lesson from the OpenCode reload post
// (https://anoma.ly/notes/opencode-reloaded/): halving must run once per
// rebuild and must never accumulate. Because Rebuild always restarts from the
// base value and re-applies the transformation sequence from scratch, halving
// runs exactly once per rebuild — repeated rebuilds never compound it.
func HalveLimits() Transform[Catalog] {
	return func(cat Catalog) (Catalog, error) {
		out := cloneCatalog(cat)
		for _, models := range out {
			for i := range models {
				models[i].Context /= 2
				models[i].Output /= 2
			}
		}
		return out, nil
	}
}

// CleanupMissing removes providers that are not in known, so the catalog only
// reflects providers present in the current data source.
func CleanupMissing(known []string) Transform[Catalog] {
	knownSet := make(map[string]struct{}, len(known))
	for _, p := range known {
		knownSet[p] = struct{}{}
	}
	return func(cat Catalog) (Catalog, error) {
		out := cloneCatalog(cat)
		for provider := range out {
			if _, ok := knownSet[provider]; !ok {
				delete(out, provider)
			}
		}
		return out, nil
	}
}

// cloneCatalog returns a deep copy so transformations never mutate their
// input (which may be the State's base value).
func cloneCatalog(cat Catalog) Catalog {
	out := make(Catalog, len(cat))
	for provider, models := range cat {
		out[provider] = append([]Model(nil), models...)
	}
	return out
}